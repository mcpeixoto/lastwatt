// Package actions implements reversible power-shedding operations.
//
// Every action records the state it displaced and hands it back as an opaque
// JSON blob. That blob is journaled to disk before the action is applied, so a
// daemon that is killed mid-shed can still restore the machine exactly. This is
// the difference between "stop the containers" and "restore what was actually
// running" -- a blanket `docker start $(docker ps -aq)` would resurrect
// containers the operator deliberately stopped months ago.
package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Action is one reversible unit of work.
type Action interface {
	// ID is stable across runs and identifies this action in the state journal.
	ID() string
	// Describe is a one-line human summary, used by `lastwatt simulate`.
	Describe() string
	// Apply performs the action and returns an undo record. When dry is true it
	// must inspect and report but change nothing.
	Apply(ctx context.Context, dry bool) (json.RawMessage, error)
	// Revert restores the state captured in the undo record.
	Revert(ctx context.Context, undo json.RawMessage, dry bool) error
}

// Spec is the declarative config form of an action.
type Spec struct {
	Type string `toml:"type"`

	// systemd
	Units []string `toml:"units"`
	Mask  bool     `toml:"mask"`

	// docker
	Containers []string `toml:"containers"`
	StopSecs   int      `toml:"stop_timeout"`

	// sysfs
	Path  string `toml:"path"`
	Value string `toml:"value"`

	// rfkill
	Devices []string `toml:"devices"`

	// exec
	Command        string `toml:"command"`
	RestoreCommand string `toml:"restore_command"`

	// mount
	Mountpoint string `toml:"mountpoint"`
}

// Build turns a Spec into a concrete Action.
func Build(s Spec) (Action, error) {
	switch s.Type {
	case "systemd":
		if len(s.Units) == 0 {
			return nil, fmt.Errorf("systemd action needs units")
		}
		return &Systemd{Units: s.Units, DoMask: s.Mask}, nil
	case "docker":
		if len(s.Containers) == 0 {
			return nil, fmt.Errorf("docker action needs containers")
		}
		t := s.StopSecs
		if t <= 0 {
			t = 30
		}
		return &Docker{Containers: s.Containers, StopTimeout: t}, nil
	case "sysfs":
		if s.Path == "" || s.Value == "" {
			return nil, fmt.Errorf("sysfs action needs path and value")
		}
		return &Sysfs{Path: s.Path, Value: s.Value}, nil
	case "rfkill":
		if len(s.Devices) == 0 {
			return nil, fmt.Errorf("rfkill action needs devices")
		}
		return &Rfkill{Devices: s.Devices}, nil
	case "exec":
		if s.Command == "" {
			return nil, fmt.Errorf("exec action needs command")
		}
		return &Exec{Command: s.Command, RestoreCmd: s.RestoreCommand}, nil
	case "mount":
		if s.Mountpoint == "" {
			return nil, fmt.Errorf("mount action needs mountpoint")
		}
		return &Mount{Mountpoint: s.Mountpoint}, nil
	default:
		return nil, fmt.Errorf("unknown action type %q", s.Type)
	}
}

// run executes a command with a timeout and returns trimmed stdout.
func run(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("%s %s: %w (%s)", name, strings.Join(args, " "), err, s)
	}
	return s, nil
}
