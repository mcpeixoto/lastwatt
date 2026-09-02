// Package awake stops a machine that is meant to be a server from suspending
// itself.
//
// This exists because of a real outage: a box running as a home server went to
// sleep twice in one day -- 5 h 11 m and then 27 m -- and took every service
// and the remote-access tunnel down with it. Nothing had failed. logind simply
// said "The system will suspend now!" and everything else was a consequence.
//
// lastwatt already owns the machine's power behaviour on battery, so it is the
// right place to own this too: a host that sheds load to survive a mains outage
// has no business suspending while it is up.
//
// The mechanism is logind's own inhibitor lock rather than masking sleep.target,
// because a lock is reversible by construction -- it lives exactly as long as
// this process, and the machine returns to its normal policy the moment lastwatt
// stops. That matches how every other action here behaves. Masking is a
// permanent edit to the system that outlives the daemon and has to be undone by
// hand, so it is left to the operator.
package awake

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

// What identifies the logind operations to block. "sleep" covers suspend,
// hibernate and hybrid-sleep; "idle" additionally stops the idle timer from
// asking in the first place.
const DefaultWhat = "sleep:idle"

// Inhibitor holds a logind lock for as long as it is alive.
type Inhibitor struct {
	what string
	why  string

	mu   sync.Mutex
	cmd  *exec.Cmd
	held bool
}

// ErrUnsupported means logind's inhibit interface is not available, which is
// normal in a container or on a non-systemd host. Callers should degrade to a
// warning, never a fatal: refusing to run a power daemon because it cannot
// block suspend would be worse than the problem.
var ErrUnsupported = errors.New("systemd-inhibit not available")

// Hold takes the lock. The returned Inhibitor is always non-nil so Release is
// safe to defer even when the error is non-nil.
func Hold(ctx context.Context, what, why string) (*Inhibitor, error) {
	if what == "" {
		what = DefaultWhat
	}
	if why == "" {
		why = "lastwatt keeps this host up; suspending would drop every service on it"
	}
	in := &Inhibitor{what: what, why: why}

	if _, err := exec.LookPath("systemd-inhibit"); err != nil {
		return in, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}

	// `sleep infinity` is the held-open child: the lock lives as long as this
	// process tree, so a crash releases it too. Not bound to ctx on purpose --
	// Release is what ends it, so shutdown order stays explicit.
	cmd := exec.Command("systemd-inhibit",
		"--what="+what,
		"--who=lastwatt",
		"--why="+why,
		"--mode=block",
		"sleep", "infinity",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return in, fmt.Errorf("take inhibitor lock: %w", err)
	}
	in.cmd = cmd
	in.held = true
	return in, nil
}

// Held reports whether the lock is currently taken.
func (i *Inhibitor) Held() bool {
	if i == nil {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.held
}

// Describe renders the lock for logs and `lastwatt status`.
func (i *Inhibitor) Describe() string {
	if !i.Held() {
		return "sleep inhibitor: not held (this host can still suspend itself)"
	}
	return "sleep inhibitor: holding " + i.what
}

// Release drops the lock. Safe to call more than once, and on a nil receiver.
func (i *Inhibitor) Release() error {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.held || i.cmd == nil || i.cmd.Process == nil {
		i.held = false
		return nil
	}
	// Kill the group: systemd-inhibit holds the fd, `sleep` is its child.
	err := syscall.Kill(-i.cmd.Process.Pid, syscall.SIGTERM)
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		_ = i.cmd.Process.Kill()
	}
	_, _ = i.cmd.Process.Wait()
	i.held = false
	i.cmd = nil
	return nil
}

// Blockers lists who else is currently blocking sleep, so `lastwatt doctor` can
// show whether something other than us would stop a suspend. Best-effort: an
// error here is informational, never fatal.
func Blockers(ctx context.Context) ([]string, error) {
	if _, err := exec.LookPath("systemd-inhibit"); err != nil {
		return nil, ErrUnsupported
	}
	out, err := exec.CommandContext(ctx, "systemd-inhibit", "--list", "--no-legend").Output()
	if err != nil {
		return nil, err
	}
	var who []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, "sleep") && strings.Contains(line, "block") {
			who = append(who, strings.Fields(line)[0])
		}
	}
	return who, nil
}
