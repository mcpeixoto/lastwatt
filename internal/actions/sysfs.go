package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Sysfs writes a value to a sysfs attribute, recording the previous one.
//
// Deliberately scoped to things TLP does not manage. TLP re-asserts EPP, disk
// APM, wifi power-save, runtime-PM and USB autosuspend on every power_supply
// uevent -- including BAT0 capacity ticks, so it re-runs constantly, not just on
// transitions. Anything written here that TLP also owns would be silently
// reverted minutes later. Shed workloads, not TLP's tunables.
type Sysfs struct {
	Path  string
	Value string
}

type sysfsUndo struct {
	Prev string `json:"prev"`
	Had  bool   `json:"had"`
}

func (s *Sysfs) ID() string { return "sysfs:" + s.Path }

func (s *Sysfs) Describe() string { return fmt.Sprintf("write %q to %s", s.Value, s.Path) }

func (s *Sysfs) Apply(ctx context.Context, dry bool) (json.RawMessage, error) {
	u := sysfsUndo{}
	if b, err := os.ReadFile(s.Path); err == nil {
		u.Prev = strings.TrimSpace(string(b))
		u.Had = true
	}
	raw, _ := json.Marshal(u)
	if dry {
		return raw, nil
	}
	if err := os.WriteFile(s.Path, []byte(s.Value), 0o644); err != nil {
		return raw, fmt.Errorf("write %s: %w", s.Path, err)
	}
	return raw, nil
}

func (s *Sysfs) Revert(ctx context.Context, undo json.RawMessage, dry bool) error {
	var u sysfsUndo
	if len(undo) > 0 {
		if err := json.Unmarshal(undo, &u); err != nil {
			return fmt.Errorf("decode sysfs undo: %w", err)
		}
	}
	if dry || !u.Had {
		return nil
	}
	if err := os.WriteFile(s.Path, []byte(u.Prev), 0o644); err != nil {
		return fmt.Errorf("restore %s: %w", s.Path, err)
	}
	return nil
}
