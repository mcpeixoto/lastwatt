package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/mcpeixoto/lastwatt/internal/awake"
	"github.com/mcpeixoto/lastwatt/internal/notify"
	"github.com/mcpeixoto/lastwatt/internal/policy"
	"github.com/mcpeixoto/lastwatt/internal/power"
	"github.com/mcpeixoto/lastwatt/internal/report"
	"github.com/mcpeixoto/lastwatt/internal/state"
)

type daemon struct {
	cfg    *policy.Config
	tiers  []policy.Tier
	supply power.Supply
	est    *power.Estimator
	st     *state.State
	queue  *notify.Queue
	log    *slog.Logger
	dryRun bool

	inhibit *awake.Inhibitor

	designUWh      int64
	outage         *report.Outage
	onBatterySince time.Time
	acStableSince  time.Time
}

func newDaemon(cfg *policy.Config, tiers []policy.Tier) (*daemon, error) {
	sup := power.Supply{AC: cfg.Power.AC, Bat: cfg.Power.Battery}
	if sup.AC == "" || sup.Bat == "" {
		detected, err := power.Detect()
		if err != nil {
			return nil, err
		}
		if sup.AC == "" {
			sup.AC = detected.AC
		}
		if sup.Bat == "" {
			sup.Bat = detected.Bat
		}
	}
	st, err := state.Load(cfg.General.StateFile)
	if err != nil {
		// Load returns a usable clean state alongside the error.
		fmt.Fprintf(os.Stderr, "lastwatt: %v\n", err)
	}
	return &daemon{
		cfg:       cfg,
		tiers:     tiers,
		supply:    sup,
		est:       power.NewEstimator(cfg.General.EWMAAlpha),
		st:        st,
		queue:     notify.OpenQueue(cfg.Notify.QueueFile),
		log:       slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})),
		designUWh: sup.DesignEnergy(),
	}, nil
}

// Run is the daemon loop.
func (d *daemon) Run(ctx context.Context) error {
	d.log.Info("starting",
		"version", version, "ac", d.supply.AC, "battery", d.supply.Bat,
		"tiers", len(d.tiers), "level", d.st.Level)

	// Take the sleep lock before anything else. A suspended host is
	// indistinguishable from a dead one to everything that depends on it, and
	// the daemon shedding load to keep the machine alive is exactly the wrong
	// thing to let sleep through. Never fatal: on a host without logind we log
	// it and carry on doing the job we can do.
	if d.cfg.General.SleepInhibited() {
		in, err := awake.Hold(ctx, d.cfg.General.InhibitWhat, "lastwatt keeps this host up; suspending would drop every service on it")
		d.inhibit = in
		if err != nil {
			d.log.Warn("could not block suspend; this host can still sleep", "err", err)
		} else {
			d.log.Info("blocking suspend", "what", d.cfg.General.InhibitWhat)
		}
		defer func() {
			if err := d.inhibit.Release(); err != nil {
				d.log.Warn("releasing sleep inhibitor", "err", err)
			}
		}()
	}

	// Reconcile on startup. If a previous instance was killed mid-outage and
	// mains has since returned, the machine is sitting shed with nobody to undo
	// it -- that must be repaired before anything else.
	if onAC, err := d.supply.OnAC(); err == nil && onAC && d.st.Level > 0 {
		d.log.Warn("found shed state while on mains; restoring", "level", d.st.Level)
		if err := d.RestoreTo(ctx, 0); err != nil {
			d.log.Error("startup restore failed", "err", err)
		}
	}

	// Primary trigger: kernel uevents. Falls back to polling alone if we lack
	// CAP_NET_ADMIN, which is the expected case when run unprivileged.
	events := make(chan power.Event, 32)
	go func() {
		if err := power.WatchUevents(ctx, events); err != nil && ctx.Err() == nil {
			d.log.Warn("netlink unavailable, polling only", "err", err)
		}
	}()

	tick := time.NewTicker(d.cfg.General.PollInterval.D())
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			d.log.Info("shutting down")
			return ctx.Err()
		case ev := <-events:
			// Only mains transitions matter. Battery devices emit a change
			// event on every capacity tick, which would otherwise wake this
			// loop constantly for nothing.
			if ev.Name != d.supply.AC {
				continue
			}
			d.log.Info("uevent", "device", ev.Name, "online", ev.Online, "action", ev.Action)
			d.step(ctx, power.SourceNetlink)
		case <-tick.C:
			d.step(ctx, power.SourcePoll)
		}
	}
}

// step samples power state and reconciles the shed level toward its target.
func (d *daemon) step(ctx context.Context, src power.Source) {
	r, err := d.supply.Read()
	if err != nil {
		d.log.Error("read power state", "err", err)
		return
	}
	now := time.Now()

	if r.OnAC {
		d.onBattery(false, r, now, src)
	} else {
		d.onBattery(true, r, now, src)
	}

	ps := d.powerState(r, now)
	target, reason := policy.TargetLevel(d.tiers, ps)

	switch {
	case target > d.st.Level:
		if err := d.ShedTo(ctx, target, reason); err != nil {
			d.log.Error("shed failed", "err", err)
		}
	case r.OnAC && d.st.Level > 0:
		// Hold off restoring until mains has been stable, so a flapping supply
		// does not repeatedly tear services up and down.
		if now.Sub(d.acStableSince) >= d.cfg.General.RestoreDelay.D() {
			if err := d.RestoreTo(ctx, 0); err != nil {
				d.log.Error("restore failed", "err", err)
			}
		}
	}
}

// onBattery handles the mains-present/absent edges.
func (d *daemon) onBattery(nowOnBattery bool, r power.Reading, now time.Time, src power.Source) {
	wasOnBattery := !d.onBatterySince.IsZero()

	if nowOnBattery && !wasOnBattery {
		d.onBatterySince = now
		d.acStableSince = time.Time{}
		d.est.Reset()
		host, _ := os.Hostname()
		d.outage = &report.Outage{
			Host:         host,
			Start:        now,
			StartPercent: r.Percent,
			EnergyFullWh: float64(r.EnergyFull) / 1e6,
		}
		d.st.OutageStart = &now
		d.st.StartPercent = r.Percent
		_ = d.st.Save(d.cfg.General.StateFile)
		msg := fmt.Sprintf("mains lost at %.1f%% battery (detected via %s)", r.Percent, src)
		d.log.Warn("MAINS LOST", "percent", r.Percent, "source", string(src))
		d.queue.Add("outage_start", msg)
		d.announce(msg)
		return
	}

	if !nowOnBattery && wasOnBattery {
		d.onBatterySince = time.Time{}
		d.acStableSince = now
		if d.outage != nil {
			d.outage.End = now
			d.outage.EndPercent = r.Percent
			d.outage.Survived = true
		}
		dur := "unknown"
		if d.st.OutageStart != nil {
			dur = now.Sub(*d.st.OutageStart).Round(time.Second).String()
		}
		msg := fmt.Sprintf("mains restored after %s, battery at %.1f%%", dur, r.Percent)
		d.log.Info("MAINS RESTORED", "percent", r.Percent, "duration", dur)
		d.queue.Add("outage_end", msg)
		d.announce(msg)
		return
	}

	if !nowOnBattery {
		if d.acStableSince.IsZero() {
			d.acStableSince = now
		}
		return
	}

	// On battery: accumulate draw samples for the runtime estimate.
	d.est.Add(r.PowerW())
	if d.outage != nil && r.PowerW() > 0 {
		d.outage.Samples = append(d.outage.Samples, report.Sample{
			At: now, Percent: r.Percent, Watts: r.PowerW(),
		})
	}
}

func (d *daemon) powerState(r power.Reading, now time.Time) policy.PowerState {
	ps := policy.PowerState{OnAC: r.OnAC, Percent: r.Percent}
	if !d.onBatterySince.IsZero() {
		ps.OnBatteryFor = now.Sub(d.onBatterySince)
	}
	if rem, ok := d.est.Remaining(r.EnergyWh()); ok {
		ps.Remaining, ps.RemainingOK = rem, true
	}
	return ps
}

// ShedTo engages every tier up to and including level.
func (d *daemon) ShedTo(ctx context.Context, level int, reason string) error {
	if level > len(d.tiers) {
		level = len(d.tiers)
	}
	for lv := d.st.Level + 1; lv <= level; lv++ {
		t := d.tiers[lv-1]

		if t.Cfg.Shutdown {
			return d.floor(ctx, lv, t, reason)
		}

		d.log.Warn("SHEDDING", "tier", lv, "name", t.Cfg.Name, "reason", reason)
		msg := fmt.Sprintf("shedding tier %d (%s) — %s", lv, t.Cfg.Name, reason)
		d.queue.Add("shed", msg)
		d.announce(msg)
		if d.outage != nil {
			d.outage.Tiers = append(d.outage.Tiers, report.TierEvent{
				At: time.Now(), Level: lv, Name: t.Cfg.Name, Reason: reason, Shed: true,
			})
		}

		for _, a := range t.Actions {
			undo, err := a.Apply(ctx, d.dryRun)
			// Record the undo data even on partial failure: whatever did change
			// must still be reversible.
			if undo != nil && !d.dryRun {
				d.st.Push(state.Entry{
					Tier: lv, TierName: t.Cfg.Name, ActionID: a.ID(),
					Undo: undo, At: time.Now(),
				})
			}
			if err != nil {
				d.log.Error("action failed", "tier", lv, "action", a.ID(), "err", err)
			} else {
				d.log.Info("applied", "tier", lv, "action", a.ID())
			}
		}

		if !d.dryRun {
			d.st.Level = lv
			if err := d.st.Save(d.cfg.General.StateFile); err != nil {
				d.log.Error("save state", "err", err)
			}
		}
	}
	return nil
}

// floor is the terminal tier: flush everything, then power off cleanly.
//
// This is the only irreversible action lastwatt takes, and it exists because a
// dirty battery-death risks the mail queue and the databases. It runs last and
// only once every cheaper option has been spent.
func (d *daemon) floor(ctx context.Context, lv int, t policy.Tier, reason string) error {
	d.log.Error("BATTERY FLOOR REACHED — shutting down cleanly", "tier", lv, "reason", reason)
	msg := fmt.Sprintf("battery floor reached (%s) — clean shutdown", reason)
	d.queue.Add("shutdown", msg)
	d.announce(msg)

	for _, a := range t.Actions {
		if _, err := a.Apply(ctx, d.dryRun); err != nil {
			d.log.Error("floor action failed", "action", a.ID(), "err", err)
		}
	}

	if d.outage != nil {
		d.outage.Survived = false
		d.outage.End = time.Now()
		if r, err := d.supply.Read(); err == nil {
			d.outage.EndPercent = r.Percent
		}
		if p, err := d.outage.Save(d.cfg.General.ReportDir); err == nil {
			d.log.Info("wrote outage report", "path", p)
		}
	}
	_ = d.st.Save(d.cfg.General.StateFile)

	if d.dryRun {
		d.log.Info("dry-run: would power off now")
		return nil
	}
	_, _ = exec.CommandContext(ctx, "sync").CombinedOutput()
	if out, err := exec.CommandContext(ctx, "systemctl", "poweroff").CombinedOutput(); err != nil {
		return fmt.Errorf("poweroff: %w (%s)", err, out)
	}
	return nil
}

// RestoreTo reverts every tier above level, newest action first.
func (d *daemon) RestoreTo(ctx context.Context, level int) error {
	if d.st.Level <= level {
		return nil
	}
	d.log.Info("RESTORING", "from", d.st.Level, "to", level)

	entries := d.st.PopTiersAbove(level)
	// Revert newest-first within a tier, but bring lower tiers back before
	// higher ones so infrastructure is up before the things that depend on it.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Tier < entries[j].Tier })

	byID := map[string]int{}
	for i, t := range d.tiers {
		for _, a := range t.Actions {
			byID[a.ID()] = i
		}
	}

	for _, e := range entries {
		idx, ok := byID[e.ActionID]
		if !ok {
			d.log.Warn("no action for journal entry; config changed?", "action", e.ActionID)
			continue
		}
		var found bool
		for _, a := range d.tiers[idx].Actions {
			if a.ID() != e.ActionID {
				continue
			}
			found = true
			if err := a.Revert(ctx, e.Undo, d.dryRun); err != nil {
				d.log.Error("revert failed", "action", a.ID(), "err", err)
			} else {
				d.log.Info("reverted", "tier", e.Tier, "action", a.ID())
			}
		}
		if !found {
			d.log.Warn("action vanished from config", "action", e.ActionID)
		}
	}

	if d.dryRun {
		return nil
	}

	d.st.Level = level
	if level == 0 {
		if d.outage != nil {
			if p, err := d.outage.Save(d.cfg.General.ReportDir); err == nil {
				d.log.Info("wrote outage report", "path", p)
				d.queue.Add("report", "outage report written to "+p)
			}
			d.outage = nil
		}
		d.st.Clear()
		// The network is back, so anything buffered during the outage can go out.
		if d.queue.Len() > 0 && notify.Online() {
			if err := d.queue.Flush(d.cfg.Notify.WebhookURL); err != nil {
				d.log.Warn("notification flush failed; keeping queue", "err", err)
			} else {
				d.log.Info("flushed queued notifications")
			}
		}
	}
	return d.st.Save(d.cfg.General.StateFile)
}

// announce sends a tier change to anyone who might be sitting at the machine.
func (d *daemon) announce(msg string) {
	if d.cfg.Notify.Wall {
		notify.Wall(msg)
	}
	if d.cfg.Notify.Desktop {
		notify.Desktop(msg)
	}
}

// PrintStatus renders the current power picture.
func (d *daemon) PrintStatus(w io.Writer) error {
	r, err := d.supply.Read()
	if err != nil {
		return err
	}
	src := "mains"
	if !r.OnAC {
		src = "BATTERY"
	}
	fmt.Fprintf(w, "power source   : %s (%s / %s)\n", src, d.supply.AC, d.supply.Bat)
	fmt.Fprintf(w, "battery        : %.1f%%  %.1f Wh of %.1f Wh  [%s]\n",
		r.Percent, r.EnergyWh(), float64(r.EnergyFull)/1e6, r.Status)
	if d.designUWh > 0 {
		fmt.Fprintf(w, "battery health : %.1f%% of design (%.1f Wh of %.1f Wh)\n",
			r.HealthPct(d.designUWh), float64(r.EnergyFull)/1e6, float64(d.designUWh)/1e6)
	}
	if r.PowerNow > 0 {
		fmt.Fprintf(w, "draw           : %.1f W\n", r.PowerW())
		if hours := r.EnergyWh() / r.PowerW(); hours > 0 {
			fmt.Fprintf(w, "estimated left : %s\n", time.Duration(hours*float64(time.Hour)).Round(time.Minute))
		}
	} else {
		fmt.Fprintf(w, "draw           : not reported (this EC reports 0 W on mains)\n")
	}
	fmt.Fprintf(w, "shed level     : %d of %d\n", d.st.Level, len(d.tiers))
	if d.st.Level > 0 {
		for _, e := range d.st.Entries {
			fmt.Fprintf(w, "  tier %d %-12s %s\n", e.Tier, e.TierName, e.ActionID)
		}
	}
	if d.st.OutageStart != nil {
		fmt.Fprintf(w, "outage since   : %s (%s)\n",
			d.st.OutageStart.Format("15:04:05"),
			time.Since(*d.st.OutageStart).Round(time.Second))
	}
	if n := d.queue.Len(); n > 0 {
		fmt.Fprintf(w, "queued notices : %d (pending connectivity)\n", n)
	}
	return nil
}

// Simulate dry-runs the whole ladder without touching anything.
func (d *daemon) Simulate(ctx context.Context, w io.Writer) error {
	fmt.Fprintf(w, "config tiers (%d):\n\n%s\n", len(d.tiers), policy.Summary(d.tiers))
	fmt.Fprintln(w, "dry-run — inspecting what each action would actually do now:")
	for i, t := range d.tiers {
		fmt.Fprintf(w, "\ntier %d  %s\n", i+1, t.Cfg.Name)
		if t.Cfg.Shutdown {
			fmt.Fprintln(w, "  would sync and power off cleanly")
		}
		for _, a := range t.Actions {
			undo, err := a.Apply(ctx, true)
			switch {
			case err != nil:
				fmt.Fprintf(w, "  [ERR ] %s: %v\n", a.ID(), err)
			case len(undo) > 0 && string(undo) != "{}" && string(undo) != "null":
				fmt.Fprintf(w, "  [WOULD] %s\n         affects: %s\n", a.Describe(), string(undo))
			default:
				fmt.Fprintf(w, "  [NOOP] %s (nothing active to change)\n", a.Describe())
			}
		}
	}
	return nil
}

// PrintLastReport prints the newest outage report.
func (d *daemon) PrintLastReport(w io.Writer) error {
	entries, err := os.ReadDir(d.cfg.General.ReportDir)
	if err != nil {
		return fmt.Errorf("no reports in %s: %w", d.cfg.General.ReportDir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".md" {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return fmt.Errorf("no outage reports yet in %s", d.cfg.General.ReportDir)
	}
	sort.Strings(names)
	b, err := os.ReadFile(filepath.Join(d.cfg.General.ReportDir, names[len(names)-1]))
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}
