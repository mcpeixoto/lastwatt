// Package report renders a post-mortem for a completed outage.
//
// The motivating experience: reconstructing an outage after the fact meant
// digging through journalctl for TLP re-apply lines and correlating them with
// upower's sparse history files. The daemon is already holding all of that data
// while it happens, so it may as well write the timeline itself.
package report

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Sample is one power reading taken during an outage.
type Sample struct {
	At      time.Time
	Percent float64
	Watts   float64
}

// TierEvent records a tier engaging or reverting.
type TierEvent struct {
	At     time.Time
	Level  int
	Name   string
	Reason string
	Shed   bool // true = engaged, false = restored
}

// Outage is everything observed between losing and regaining mains.
type Outage struct {
	Host         string
	Start        time.Time
	End          time.Time // zero if still ongoing
	StartPercent float64
	EndPercent   float64
	EnergyFullWh float64
	Samples      []Sample
	Tiers        []TierEvent
	Survived     bool // true if mains returned before the floor was reached
}

// Duration is how long the machine ran on battery.
func (o Outage) Duration() time.Duration {
	end := o.End
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(o.Start)
}

// DrawStats returns min, mean and max observed draw in watts.
func (o Outage) DrawStats() (min, mean, max float64) {
	var sum float64
	n := 0
	for _, s := range o.Samples {
		if s.Watts <= 0 {
			continue
		}
		if n == 0 || s.Watts < min {
			min = s.Watts
		}
		if s.Watts > max {
			max = s.Watts
		}
		sum += s.Watts
		n++
	}
	if n > 0 {
		mean = sum / float64(n)
	}
	return
}

// Render produces the Markdown post-mortem.
func (o Outage) Render() string {
	var b strings.Builder
	min, mean, max := o.DrawStats()

	fmt.Fprintf(&b, "# Power outage report — %s\n\n", o.Host)
	fmt.Fprintf(&b, "**Mains lost:** %s\n\n", o.Start.Format("2006-01-02 15:04:05 MST"))
	if o.End.IsZero() {
		fmt.Fprintf(&b, "**Status:** ongoing (%s so far)\n\n", o.Duration().Round(time.Second))
	} else {
		fmt.Fprintf(&b, "**Mains restored:** %s\n\n", o.End.Format("2006-01-02 15:04:05 MST"))
		fmt.Fprintf(&b, "**Ran on battery for:** %s\n\n", o.Duration().Round(time.Second))
	}

	b.WriteString("## Battery\n\n")
	fmt.Fprintf(&b, "| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| Charge at outage start | %.1f%% |\n", o.StartPercent)
	fmt.Fprintf(&b, "| Charge at restore | %.1f%% |\n", o.EndPercent)
	fmt.Fprintf(&b, "| Consumed | %.1f%% of pack |\n", o.StartPercent-o.EndPercent)
	if o.EnergyFullWh > 0 {
		fmt.Fprintf(&b, "| Energy used | %.1f Wh of %.1f Wh |\n",
			(o.StartPercent-o.EndPercent)/100*o.EnergyFullWh, o.EnergyFullWh)
	}
	if mean > 0 {
		fmt.Fprintf(&b, "| Draw (min / mean / max) | %.1f / %.1f / %.1f W |\n", min, mean, max)
	}
	if !o.Survived && !o.End.IsZero() {
		b.WriteString("\n**The battery floor was reached — the machine shut down cleanly.**\n")
	}

	if len(o.Tiers) > 0 {
		b.WriteString("\n## Timeline\n\n```\n")
		fmt.Fprintf(&b, "%s  mains lost (battery %.1f%%)\n",
			o.Start.Format("15:04:05"), o.StartPercent)
		for _, t := range o.Tiers {
			verb := "shed  "
			if !t.Shed {
				verb = "restore"
			}
			fmt.Fprintf(&b, "%s  %s tier %d (%s) — %s\n",
				t.At.Format("15:04:05"), verb, t.Level, t.Name, t.Reason)
		}
		if !o.End.IsZero() {
			fmt.Fprintf(&b, "%s  mains restored (battery %.1f%%)\n",
				o.End.Format("15:04:05"), o.EndPercent)
		}
		b.WriteString("```\n")
	}

	// A sparse drain curve: one line per 5% of charge lost, which keeps a
	// multi-hour outage readable without dumping thousands of samples.
	if len(o.Samples) > 1 {
		b.WriteString("\n## Drain curve\n\n```\n")
		last := 1000.0
		for _, s := range o.Samples {
			if last-s.Percent < 5 && s.Percent != o.Samples[len(o.Samples)-1].Percent {
				continue
			}
			fmt.Fprintf(&b, "%s  %5.1f%%  %5.1f W\n", s.At.Format("15:04:05"), s.Percent, s.Watts)
			last = s.Percent
		}
		b.WriteString("```\n")
	}

	return b.String()
}

// Save writes the report into dir and returns its path.
func (o Outage) Save(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("outage-%s.md", o.Start.Format("20060102-150405"))
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(o.Render()), 0o644); err != nil {
		return "", err
	}
	return p, nil
}
