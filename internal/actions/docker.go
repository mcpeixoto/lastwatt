package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Docker stops containers with a per-container grace period, recording only
// those that were genuinely running.
type Docker struct {
	Containers  []string
	StopTimeout int // seconds passed to `docker stop -t`
}

type dockerUndo struct {
	Running []string `json:"running"`
}

func (d *Docker) ID() string { return "docker:" + strings.Join(d.Containers, ",") }

func (d *Docker) Describe() string {
	return fmt.Sprintf("stop %d container(s) with -t %d: %s",
		len(d.Containers), d.StopTimeout, strings.Join(d.Containers, " "))
}

// runningSet returns the names of all currently running containers.
func runningSet(ctx context.Context) (map[string]bool, error) {
	out, err := run(ctx, 30*time.Second, "docker", "ps", "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			set[l] = true
		}
	}
	return set, nil
}

func (d *Docker) Apply(ctx context.Context, dry bool) (json.RawMessage, error) {
	live, err := runningSet(ctx)
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}
	u := dockerUndo{}
	var errs []string
	for _, name := range d.Containers {
		if !live[name] {
			continue
		}
		u.Running = append(u.Running, name)
		if dry {
			continue
		}
		// The grace period matters: poste.io runs an s6 tree with a Haraka mail
		// queue and Dovecot, and MySQL 5.7 needs a clean InnoDB shutdown. A
		// default 10s SIGTERM window is not enough for either.
		if _, err := run(ctx, time.Duration(d.StopTimeout+30)*time.Second,
			"docker", "stop", "-t", strconv.Itoa(d.StopTimeout), name); err != nil {
			errs = append(errs, err.Error())
		}
	}
	b, _ := json.Marshal(u)
	if len(errs) > 0 {
		return b, fmt.Errorf("docker stop: %s", strings.Join(errs, "; "))
	}
	return b, nil
}

func (d *Docker) Revert(ctx context.Context, undo json.RawMessage, dry bool) error {
	var u dockerUndo
	if len(undo) > 0 {
		if err := json.Unmarshal(undo, &u); err != nil {
			return fmt.Errorf("decode docker undo: %w", err)
		}
	}
	if dry {
		return nil
	}
	var errs []string
	// Start in the order they were listed. Config lists dependencies first, so
	// forward order brings infrastructure up before the things that need it.
	for _, name := range u.Running {
		if _, err := run(ctx, 180*time.Second, "docker", "start", name); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("docker start: %s", strings.Join(errs, "; "))
	}
	return nil
}
