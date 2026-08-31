package policy

import (
	"fmt"
	"strings"
	"time"
)

// PowerState is the input to tier evaluation.
type PowerState struct {
	OnAC         bool
	OnBatteryFor time.Duration
	Percent      float64
	Remaining    time.Duration
	RemainingOK  bool // false while the draw estimate is still warming up
}

// Triggered reports whether a tier's conditions are met, and why.
func (t TierCfg) Triggered(s PowerState) (bool, string) {
	if s.OnAC {
		return false, ""
	}
	if t.Immediate {
		return true, "mains lost"
	}
	if t.After > 0 && s.OnBatteryFor >= t.After.D() {
		return true, fmt.Sprintf("on battery for %s (>= %s)",
			s.OnBatteryFor.Round(time.Second), t.After.D())
	}
	if t.BelowPercent > 0 && s.Percent > 0 && s.Percent < t.BelowPercent {
		return true, fmt.Sprintf("battery %.1f%% (< %.0f%%)", s.Percent, t.BelowPercent)
	}
	// Runtime-remaining is the most honest trigger, since percentage is
	// nonlinear -- but only once the EWMA has enough samples to be trusted.
	if t.BelowRemaining > 0 && s.RemainingOK && s.Remaining < t.BelowRemaining.D() {
		return true, fmt.Sprintf("~%s left (< %s)",
			s.Remaining.Round(time.Minute), t.BelowRemaining.D())
	}
	return false, ""
}

// TargetLevel returns how many tiers should be engaged for the given state.
//
// Tiers form a monotonic staircase: engaging tier 3 implies tiers 1 and 2 are
// also engaged, even if their individual conditions have not been met. That
// matters when an outage begins with an already-low battery -- the machine
// should land directly in the right posture rather than walking down one stage
// at a time while it drains.
func TargetLevel(tiers []Tier, s PowerState) (int, string) {
	if s.OnAC {
		return 0, "on mains"
	}
	level, reason := 0, ""
	for i, t := range tiers {
		if ok, why := t.Cfg.Triggered(s); ok {
			level = i + 1
			reason = fmt.Sprintf("%s: %s", t.Cfg.Name, why)
		}
	}
	if level == 0 {
		return 0, "on battery, no tier triggered yet"
	}
	return level, reason
}

// Summary renders the tier ladder for `lastwatt simulate`.
func Summary(tiers []Tier) string {
	var b strings.Builder
	for i, t := range tiers {
		var conds []string
		if t.Cfg.Immediate {
			conds = append(conds, "immediately on AC loss")
		}
		if t.Cfg.After > 0 {
			conds = append(conds, "after "+t.Cfg.After.D().String())
		}
		if t.Cfg.BelowPercent > 0 {
			conds = append(conds, fmt.Sprintf("below %.0f%%", t.Cfg.BelowPercent))
		}
		if t.Cfg.BelowRemaining > 0 {
			conds = append(conds, "below "+t.Cfg.BelowRemaining.D().String()+" left")
		}
		flag := ""
		if t.Cfg.Shutdown {
			flag = "  [TERMINAL: clean shutdown]"
		}
		fmt.Fprintf(&b, "tier %d  %-12s  %s%s\n", i+1, t.Cfg.Name, strings.Join(conds, " OR "), flag)
		for _, a := range t.Actions {
			fmt.Fprintf(&b, "          - %s\n", a.Describe())
		}
	}
	return b.String()
}
