package power

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

// Event is a decoded kernel uevent for a power_supply device.
type Event struct {
	Action string // "change", "add", ...
	Name   string // POWER_SUPPLY_NAME, e.g. "AC0"
	Online string // POWER_SUPPLY_ONLINE, "0" or "1" (mains devices only)
	Vars   map[string]string
}

// Source describes how a transition was detected, for logging.
type Source string

const (
	SourceNetlink Source = "netlink"
	SourcePoll    Source = "poll"
)

// WatchUevents subscribes to kernel uevents on the NETLINK_KOBJECT_UEVENT
// socket and delivers power_supply events on ch.
//
// This talks to the kernel directly rather than installing a udev rule. That
// avoids udev's RUN+= sandbox and its 180s event timeout entirely, and keeps
// working even if systemd-udevd is wedged -- which is precisely the situation a
// load-shedding daemon needs to survive.
//
// Binding the kernel multicast group requires CAP_NET_ADMIN. Without it this
// returns an error and the caller is expected to fall back to polling alone.
func WatchUevents(ctx context.Context, ch chan<- Event) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_KOBJECT_UEVENT)
	if err != nil {
		return fmt.Errorf("netlink socket: %w", err)
	}
	defer unix.Close(fd)

	// A generous receive buffer: uevent storms (USB enclosure plug-in, for
	// instance) must not drop the AC transition we actually care about.
	_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUFFORCE, 1<<20)

	addr := &unix.SockaddrNetlink{
		Family: unix.AF_NETLINK,
		Groups: 1, // kernel-originated uevents
	}
	if err := unix.Bind(fd, addr); err != nil {
		return fmt.Errorf("netlink bind (needs CAP_NET_ADMIN): %w", err)
	}

	// Unblock the blocking Recvfrom below when the context is cancelled.
	go func() {
		<-ctx.Done()
		unix.Shutdown(fd, unix.SHUT_RDWR)
		unix.Close(fd)
	}()

	buf := make([]byte, 8192)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == unix.EINTR {
				continue
			}
			return fmt.Errorf("netlink recv: %w", err)
		}
		ev, ok := parseUevent(buf[:n])
		if !ok {
			continue
		}
		select {
		case ch <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// parseUevent decodes a NUL-separated uevent payload, keeping only
// power_supply events.
func parseUevent(b []byte) (Event, bool) {
	parts := strings.Split(string(b), "\x00")
	if len(parts) == 0 {
		return Event{}, false
	}
	ev := Event{Vars: make(map[string]string, len(parts))}
	// parts[0] is the summary line, e.g. "change@/devices/.../AC0".
	if i := strings.IndexByte(parts[0], '@'); i > 0 {
		ev.Action = parts[0][:i]
	}
	for _, p := range parts[1:] {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			continue
		}
		ev.Vars[k] = v
	}
	if ev.Vars["SUBSYSTEM"] != "power_supply" {
		return Event{}, false
	}
	if a := ev.Vars["ACTION"]; a != "" {
		ev.Action = a
	}
	ev.Name = ev.Vars["POWER_SUPPLY_NAME"]
	ev.Online = ev.Vars["POWER_SUPPLY_ONLINE"]
	return ev, true
}
