package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Mount unmounts a filesystem during shed and remounts it on restore.
//
// For a USB enclosure this is worth doing for two reasons: a spinning platter
// is a real drain, and an unmounted filesystem cannot be corrupted if the
// machine dies mid-write when the battery finally goes.
type Mount struct {
	Mountpoint string
}

type mountUndo struct {
	WasMounted bool `json:"was_mounted"`
}

func (m *Mount) ID() string { return "mount:" + m.Mountpoint }

func (m *Mount) Describe() string { return fmt.Sprintf("unmount %s", m.Mountpoint) }

func isMounted(ctx context.Context, mp string) bool {
	_, err := run(ctx, 10*time.Second, "mountpoint", "-q", mp)
	return err == nil
}

func (m *Mount) Apply(ctx context.Context, dry bool) (json.RawMessage, error) {
	u := mountUndo{WasMounted: isMounted(ctx, m.Mountpoint)}
	b, _ := json.Marshal(u)
	if dry || !u.WasMounted {
		return b, nil
	}
	// Sync first: an unmount that fails should still leave the data durable.
	_, _ = run(ctx, 60*time.Second, "sync")
	if _, err := run(ctx, 60*time.Second, "umount", m.Mountpoint); err != nil {
		return b, fmt.Errorf("umount %s: %w", m.Mountpoint, err)
	}
	return b, nil
}

func (m *Mount) Revert(ctx context.Context, undo json.RawMessage, dry bool) error {
	var u mountUndo
	if len(undo) > 0 {
		if err := json.Unmarshal(undo, &u); err != nil {
			return fmt.Errorf("decode mount undo: %w", err)
		}
	}
	if dry || !u.WasMounted || isMounted(ctx, m.Mountpoint) {
		return nil
	}
	_, err := run(ctx, 60*time.Second, "mount", m.Mountpoint)
	return err
}
