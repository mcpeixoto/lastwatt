package awake

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestReleaseIsSafeOnNilAndUnheld(t *testing.T) {
	var nilIn *Inhibitor
	if err := nilIn.Release(); err != nil {
		t.Fatalf("Release on nil: %v", err)
	}
	if nilIn.Held() {
		t.Fatal("nil inhibitor reports held")
	}
	in := &Inhibitor{}
	if err := in.Release(); err != nil {
		t.Fatalf("Release on unheld: %v", err)
	}
}

// Hold must return a usable Inhibitor even on failure, so callers can defer
// Release unconditionally. A power daemon that refuses to start because it
// cannot block suspend would be worse than one that sleeps.
func TestHoldAlwaysReturnsNonNil(t *testing.T) {
	in, err := Hold(context.Background(), DefaultWhat, "unit test")
	if in == nil {
		t.Fatal("Hold returned nil Inhibitor")
	}
	defer in.Release()
	if err != nil && !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unexpected error kind: %v", err)
	}
	if err != nil && in.Held() {
		t.Fatal("reports held after a failed Hold")
	}
}

func TestDescribeReflectsState(t *testing.T) {
	in := &Inhibitor{what: "sleep:idle"}
	if got := in.Describe(); !strings.Contains(got, "not held") {
		t.Fatalf("unheld Describe = %q", got)
	}
	in.held = true
	if got := in.Describe(); !strings.Contains(got, "sleep:idle") {
		t.Fatalf("held Describe = %q", got)
	}
}

// The real contract: while held, logind must list lastwatt as a sleep blocker,
// and it must be gone after Release. Skipped where logind is absent.
func TestHoldRegistersWithLogind(t *testing.T) {
	if _, err := exec.LookPath("systemd-inhibit"); err != nil {
		t.Skip("no systemd-inhibit on this host")
	}
	ctx := context.Background()
	in, err := Hold(ctx, DefaultWhat, "lastwatt unit test")
	if err != nil {
		t.Skipf("cannot take a lock here: %v", err)
	}
	if !in.Held() {
		t.Fatal("Hold succeeded but Held() is false")
	}
	who, err := Blockers(ctx)
	if err != nil {
		t.Skipf("cannot list blockers: %v", err)
	}
	found := false
	for _, w := range who {
		if strings.Contains(w, "lastwatt") {
			found = true
		}
	}
	if !found {
		t.Fatalf("lastwatt not among sleep blockers: %v", who)
	}
	if err := in.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if in.Held() {
		t.Fatal("still held after Release")
	}
}
