package policy

import (
	"testing"
	"time"
)

func tiers(cfgs ...TierCfg) []Tier {
	out := make([]Tier, 0, len(cfgs))
	for _, c := range cfgs {
		out = append(out, Tier{Cfg: c})
	}
	return out
}

func TestOnMainsNeverSheds(t *testing.T) {
	ts := tiers(TierCfg{Name: "a", Immediate: true})
	got, _ := TargetLevel(ts, PowerState{OnAC: true, Percent: 3})
	if got != 0 {
		t.Fatalf("on mains: want level 0, got %d", got)
	}
}

func TestImmediateTierFiresAtOnce(t *testing.T) {
	ts := tiers(TierCfg{Name: "instant", Immediate: true})
	got, why := TargetLevel(ts, PowerState{Percent: 100, OnBatteryFor: 0})
	if got != 1 {
		t.Fatalf("want level 1, got %d (%s)", got, why)
	}
}

func TestAfterTierWaitsForDebounce(t *testing.T) {
	ts := tiers(
		TierCfg{Name: "instant", Immediate: true},
		TierCfg{Name: "edge", After: Duration(90 * time.Second)},
	)
	// A brief flicker must not reach tier 2 -- that is the whole point of the
	// debounce, since tier 2 stops the mail server.
	got, _ := TargetLevel(ts, PowerState{Percent: 100, OnBatteryFor: 20 * time.Second})
	if got != 1 {
		t.Fatalf("at 20s: want level 1, got %d", got)
	}
	got, _ = TargetLevel(ts, PowerState{Percent: 100, OnBatteryFor: 91 * time.Second})
	if got != 2 {
		t.Fatalf("at 91s: want level 2, got %d", got)
	}
}

// An outage that begins with an already-low battery must land directly in the
// right posture rather than walking down one stage at a time while it drains.
func TestStaircaseSkipsAheadOnLowBattery(t *testing.T) {
	ts := tiers(
		TierCfg{Name: "instant", Immediate: true},
		TierCfg{Name: "edge", After: Duration(90 * time.Second)},
		TierCfg{Name: "stateful", BelowPercent: 50},
		TierCfg{Name: "desktop", BelowPercent: 30},
	)
	got, _ := TargetLevel(ts, PowerState{Percent: 12, OnBatteryFor: time.Second})
	if got != 4 {
		t.Fatalf("at 12%% battery: want level 4, got %d", got)
	}
}

func TestRemainingIgnoredUntilEstimateReady(t *testing.T) {
	ts := tiers(TierCfg{Name: "floor", BelowRemaining: Duration(8 * time.Minute)})
	// Draw estimate not yet warm: the tier must not fire on an untrusted number.
	got, _ := TargetLevel(ts, PowerState{Percent: 80, Remaining: time.Minute, RemainingOK: false})
	if got != 0 {
		t.Fatalf("estimate not ready: want level 0, got %d", got)
	}
	got, _ = TargetLevel(ts, PowerState{Percent: 80, Remaining: time.Minute, RemainingOK: true})
	if got != 1 {
		t.Fatalf("estimate ready: want level 1, got %d", got)
	}
}

// Percentage is nonlinear, so a healthy-looking charge with a heavy draw must
// still trip the floor on time-remaining alone.
func TestHighPercentButLowRuntimeStillTrips(t *testing.T) {
	ts := tiers(TierCfg{Name: "floor", BelowPercent: 5, BelowRemaining: Duration(8 * time.Minute)})
	got, why := TargetLevel(ts, PowerState{Percent: 40, Remaining: 3 * time.Minute, RemainingOK: true})
	if got != 1 {
		t.Fatalf("want level 1, got %d (%s)", got, why)
	}
}

func TestValidateRejectsTriggerlessTier(t *testing.T) {
	c := &Config{Tiers: []TierCfg{{Name: "x"}}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for tier with no trigger condition")
	}
}

func TestValidateRequiresShutdownTierLast(t *testing.T) {
	c := &Config{Tiers: []TierCfg{
		{Name: "floor", BelowPercent: 5, Shutdown: true},
		{Name: "edge", Immediate: true},
	}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error when shutdown tier is not last")
	}
}

func TestValidateRejectsDuplicateNames(t *testing.T) {
	c := &Config{Tiers: []TierCfg{
		{Name: "a", Immediate: true},
		{Name: "a", BelowPercent: 10},
	}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for duplicate tier names")
	}
}
