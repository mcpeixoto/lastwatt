package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Systemd stops units, remembering which were actually active so that restore
// never starts something that was already deliberately down.
type Systemd struct {
	Units  []string
	DoMask bool // also mask, to stop timers re-triggering the unit mid-outage
}

type systemdUndo struct {
	Active []string `json:"active"`
	Masked []string `json:"masked"`
}

func (s *Systemd) ID() string { return "systemd:" + strings.Join(s.Units, ",") }

func (s *Systemd) Describe() string {
	verb := "stop"
	if s.DoMask {
		verb = "stop+mask"
	}
	return fmt.Sprintf("%s %d unit(s): %s", verb, len(s.Units), strings.Join(s.Units, " "))
}

func isActive(ctx context.Context, unit string) bool {
	out, _ := run(ctx, 10*time.Second, "systemctl", "is-active", unit)
	return out == "active" || out == "activating"
}

func (s *Systemd) Apply(ctx context.Context, dry bool) (json.RawMessage, error) {
	u := systemdUndo{}
	var errs []string

	// Snapshot the whole active set BEFORE stopping anything.
	//
	// Interleaving the check with the stop loses units to cascading systemd
	// dependencies: stopping cups.service also stops cups-browsed.service, so a
	// later is-active check reports it inactive and it never makes it into the
	// restore set. It then stays down forever after the outage. Found the hard
	// way on exactly that pair.
	for _, unit := range s.Units {
		if isActive(ctx, unit) {
			u.Active = append(u.Active, unit)
		}
	}
	if dry {
		b, _ := json.Marshal(u)
		return b, nil
	}

	for _, unit := range u.Active {
		if _, err := run(ctx, 120*time.Second, "systemctl", "stop", unit); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if s.DoMask {
			if _, err := run(ctx, 20*time.Second, "systemctl", "mask", "--runtime", unit); err == nil {
				u.Masked = append(u.Masked, unit)
			}
		}
	}
	b, _ := json.Marshal(u)
	if len(errs) > 0 {
		// Partial success is still recorded: whatever did stop must be restorable.
		return b, fmt.Errorf("systemd stop: %s", strings.Join(errs, "; "))
	}
	return b, nil
}

func (s *Systemd) Revert(ctx context.Context, undo json.RawMessage, dry bool) error {
	var u systemdUndo
	if len(undo) > 0 {
		if err := json.Unmarshal(undo, &u); err != nil {
			return fmt.Errorf("decode systemd undo: %w", err)
		}
	}
	if dry {
		return nil
	}
	var errs []string
	for _, unit := range u.Masked {
		if _, err := run(ctx, 20*time.Second, "systemctl", "unmask", "--runtime", unit); err != nil {
			errs = append(errs, err.Error())
		}
	}
	for _, unit := range u.Active {
		if _, err := run(ctx, 120*time.Second, "systemctl", "start", unit); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("systemd restore: %s", strings.Join(errs, "; "))
	}
	return nil
}
