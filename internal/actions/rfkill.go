package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Rfkill soft-blocks radios, recording which were unblocked beforehand so a
// radio the operator had already disabled stays disabled after restore.
type Rfkill struct {
	Devices []string // "wifi", "bluetooth", ...
}

type rfkillUndo struct {
	Unblocked []string `json:"unblocked"`
}

func (r *Rfkill) ID() string { return "rfkill:" + strings.Join(r.Devices, ",") }

func (r *Rfkill) Describe() string {
	return fmt.Sprintf("soft-block radios: %s", strings.Join(r.Devices, " "))
}

// blockedTypes returns the set of rfkill types currently soft-blocked.
func blockedTypes(ctx context.Context) map[string]bool {
	res := map[string]bool{}
	out, err := run(ctx, 10*time.Second, "rfkill", "--noheadings", "--output", "TYPE,SOFT")
	if err != nil {
		return res
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "blocked" {
			res[f[0]] = true
		}
	}
	return res
}

func (r *Rfkill) Apply(ctx context.Context, dry bool) (json.RawMessage, error) {
	blocked := blockedTypes(ctx)
	u := rfkillUndo{}
	var errs []string
	for _, d := range r.Devices {
		if blocked[d] {
			continue // already off; leave it that way on restore
		}
		u.Unblocked = append(u.Unblocked, d)
		if dry {
			continue
		}
		if _, err := run(ctx, 15*time.Second, "rfkill", "block", d); err != nil {
			errs = append(errs, err.Error())
		}
	}
	b, _ := json.Marshal(u)
	if len(errs) > 0 {
		return b, fmt.Errorf("rfkill block: %s", strings.Join(errs, "; "))
	}
	return b, nil
}

func (r *Rfkill) Revert(ctx context.Context, undo json.RawMessage, dry bool) error {
	var u rfkillUndo
	if len(undo) > 0 {
		if err := json.Unmarshal(undo, &u); err != nil {
			return fmt.Errorf("decode rfkill undo: %w", err)
		}
	}
	if dry {
		return nil
	}
	var errs []string
	for _, d := range u.Unblocked {
		if _, err := run(ctx, 15*time.Second, "rfkill", "unblock", d); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("rfkill unblock: %s", strings.Join(errs, "; "))
	}
	return nil
}
