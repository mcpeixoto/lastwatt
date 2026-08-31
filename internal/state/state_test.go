package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	now := time.Now().Truncate(time.Second)
	s := &State{Level: 2, OutageStart: &now, StartPercent: 87.5}
	s.Push(Entry{Tier: 1, TierName: "instant", ActionID: "sysfs:/x",
		Undo: json.RawMessage(`{"prev":"0"}`), At: now})

	if err := s.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Level != 2 || len(got.Entries) != 1 || got.StartPercent != 87.5 {
		t.Fatalf("round trip lost data: %+v", got)
	}
	if got.Entries[0].ActionID != "sysfs:/x" {
		t.Fatalf("bad entry: %+v", got.Entries[0])
	}
}

// A missing journal is the normal, un-shed state, not an error.
func TestLoadMissingFileIsClean(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if s.Level != 0 || len(s.Entries) != 0 {
		t.Fatalf("want clean state, got %+v", s)
	}
}

// Reverting must undo newest first, so later actions are unwound before the
// earlier ones they were layered on top of.
func TestPopTiersAboveReturnsNewestFirst(t *testing.T) {
	s := &State{}
	s.Push(Entry{Tier: 1, ActionID: "a"})
	s.Push(Entry{Tier: 2, ActionID: "b"})
	s.Push(Entry{Tier: 3, ActionID: "c"})

	popped := s.PopTiersAbove(1)
	if len(popped) != 2 {
		t.Fatalf("want 2 popped, got %d", len(popped))
	}
	if popped[0].ActionID != "c" || popped[1].ActionID != "b" {
		t.Fatalf("want newest-first [c b], got [%s %s]", popped[0].ActionID, popped[1].ActionID)
	}
	if len(s.Entries) != 1 || s.Entries[0].ActionID != "a" {
		t.Fatalf("tier 1 entry should remain, got %+v", s.Entries)
	}
}

func TestPopTiersAboveZeroTakesEverything(t *testing.T) {
	s := &State{}
	s.Push(Entry{Tier: 1, ActionID: "a"})
	s.Push(Entry{Tier: 2, ActionID: "b"})
	if got := s.PopTiersAbove(0); len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if len(s.Entries) != 0 {
		t.Fatalf("want no entries left, got %d", len(s.Entries))
	}
}

// A corrupt journal must not wedge the daemon: it returns a usable clean state
// alongside the error so the caller can carry on.
func TestCorruptJournalStillYieldsUsableState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := writeFile(path, "{not json"); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err == nil {
		t.Fatal("expected an error for corrupt json")
	}
	if s == nil || s.Level != 0 {
		t.Fatalf("want usable clean state, got %+v", s)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
