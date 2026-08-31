package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Exec runs an arbitrary shell command, with an optional restore command. This
// is the escape hatch for anything the typed actions do not cover.
type Exec struct {
	Command    string
	RestoreCmd string
}

func (e *Exec) ID() string { return "exec:" + e.Command }

func (e *Exec) Describe() string {
	if e.RestoreCmd == "" {
		return fmt.Sprintf("run %q (no restore command)", e.Command)
	}
	return fmt.Sprintf("run %q (restore: %q)", e.Command, e.RestoreCmd)
}

func (e *Exec) Apply(ctx context.Context, dry bool) (json.RawMessage, error) {
	if dry {
		return json.RawMessage(`{}`), nil
	}
	if _, err := run(ctx, 120*time.Second, "sh", "-c", e.Command); err != nil {
		return json.RawMessage(`{}`), err
	}
	return json.RawMessage(`{}`), nil
}

func (e *Exec) Revert(ctx context.Context, undo json.RawMessage, dry bool) error {
	if dry || e.RestoreCmd == "" {
		return nil
	}
	_, err := run(ctx, 120*time.Second, "sh", "-c", e.RestoreCmd)
	return err
}
