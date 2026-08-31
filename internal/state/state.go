// Package state persists what lastwatt has shed, so a daemon that is killed
// mid-outage can still put the machine back exactly as it found it.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Entry records one applied action and the undo data needed to reverse it.
type Entry struct {
	Tier     int             `json:"tier"`
	TierName string          `json:"tier_name"`
	ActionID string          `json:"action_id"`
	Undo     json.RawMessage `json:"undo"`
	At       time.Time       `json:"at"`
}

// State is the on-disk journal.
type State struct {
	Version int `json:"version"`
	// Level is how many tiers are currently engaged.
	Level int `json:"level"`
	// Entries is an append-only stack; restore walks it in reverse.
	Entries []Entry `json:"entries"`

	OutageStart  *time.Time `json:"outage_start,omitempty"`
	LastShedAt   *time.Time `json:"last_shed_at,omitempty"`
	StartPercent float64    `json:"start_percent,omitempty"`
}

const currentVersion = 1

// Load reads the journal. A missing file is not an error -- it just means the
// machine is in its normal, un-shed state.
func Load(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &State{Version: currentVersion}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		// A corrupt journal must not wedge the daemon. Start clean and say so
		// loudly -- worst case is that restore misses something, which the
		// operator can fix, whereas refusing to start leaves the box shed.
		return &State{Version: currentVersion}, fmt.Errorf("corrupt state file %s (starting clean): %w", path, err)
	}
	if s.Version == 0 {
		s.Version = currentVersion
	}
	return &s, nil
}

// Save writes the journal atomically: temp file, fsync, rename. A half-written
// journal would be worse than none at all.
func (s *State) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".lastwatt-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Push appends an applied action.
func (s *State) Push(e Entry) {
	s.Entries = append(s.Entries, e)
	now := time.Now()
	s.LastShedAt = &now
}

// PopTiersAbove removes and returns entries for tiers above level, newest
// first, which is the order they must be reverted in.
func (s *State) PopTiersAbove(level int) []Entry {
	var popped []Entry
	keep := s.Entries[:0:0]
	for _, e := range s.Entries {
		if e.Tier > level {
			popped = append(popped, e)
		} else {
			keep = append(keep, e)
		}
	}
	s.Entries = keep
	// Reverse into newest-first order.
	for i, j := 0, len(popped)-1; i < j; i, j = i+1, j-1 {
		popped[i], popped[j] = popped[j], popped[i]
	}
	return popped
}

// Clear resets to the un-shed state.
func (s *State) Clear() {
	s.Level = 0
	s.Entries = nil
	s.OutageStart = nil
	s.LastShedAt = nil
	s.StartPercent = 0
}
