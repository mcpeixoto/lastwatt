package power

import (
	"testing"
	"time"
)

// Many EC firmwares report 0 W while on mains. Folding those zeros into the
// average would make the runtime estimate meaningless.
func TestZeroSamplesIgnored(t *testing.T) {
	e := NewEstimator(0.2)
	for i := 0; i < 5; i++ {
		e.Add(0)
	}
	if e.Ready() {
		t.Fatal("zero samples should not make the estimator ready")
	}
	if got := e.DrawW(); got != 0 {
		t.Fatalf("want 0 W, got %v", got)
	}
}

func TestNotReadyUntilEnoughSamples(t *testing.T) {
	e := NewEstimator(0.5)
	e.Add(20)
	e.Add(20)
	if e.Ready() {
		t.Fatal("two samples should not be enough")
	}
	e.Add(20)
	if !e.Ready() {
		t.Fatal("three samples should be enough")
	}
}

func TestConvergesOnSteadyDraw(t *testing.T) {
	e := NewEstimator(0.5)
	for i := 0; i < 30; i++ {
		e.Add(12)
	}
	if got := e.DrawW(); got < 11.9 || got > 12.1 {
		t.Fatalf("want ~12 W, got %v", got)
	}
}

func TestRemaining(t *testing.T) {
	e := NewEstimator(0.5)
	for i := 0; i < 30; i++ {
		e.Add(10)
	}
	// 20 Wh at 10 W is 2 hours.
	d, ok := e.Remaining(20)
	if !ok {
		t.Fatal("estimate should be ready")
	}
	if d < 119*time.Minute || d > 121*time.Minute {
		t.Fatalf("want ~2h, got %v", d)
	}
}

func TestRemainingNotOKBeforeWarmup(t *testing.T) {
	e := NewEstimator(0.2)
	if _, ok := e.Remaining(20); ok {
		t.Fatal("should not report a runtime before warm-up")
	}
}

func TestResetClears(t *testing.T) {
	e := NewEstimator(0.5)
	for i := 0; i < 5; i++ {
		e.Add(30)
	}
	e.Reset()
	if e.Ready() || e.Samples() != 0 || e.PeakW() != 0 {
		t.Fatal("reset should clear all state")
	}
}
