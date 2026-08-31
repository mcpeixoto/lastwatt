// Package notify buffers outage events and delivers them once the network is
// back.
//
// The core constraint: when mains dies, the router usually dies with it, so
// nothing can be sent during the outage that matters most. Everything is
// therefore queued to disk and flushed when connectivity returns.
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Event is one queued notification.
type Event struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
}

// Queue is a disk-backed event buffer.
type Queue struct {
	path   string
	Events []Event `json:"events"`
}

// OpenQueue loads (or creates) the queue at path.
func OpenQueue(path string) *Queue {
	q := &Queue{path: path}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, q)
	}
	return q
}

// Add appends an event and persists immediately -- the machine may lose power
// at any moment, so an in-memory-only queue would routinely lose the very
// events worth reporting.
func (q *Queue) Add(kind, msg string) {
	q.Events = append(q.Events, Event{At: time.Now(), Kind: kind, Message: msg})
	q.save()
}

func (q *Queue) save() {
	if q.path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(q.path), 0o755)
	b, err := json.MarshalIndent(q, "", "  ")
	if err != nil {
		return
	}
	tmp := q.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, q.path)
	}
}

// Len returns the number of queued events.
func (q *Queue) Len() int { return len(q.Events) }

// Online reports whether outbound network appears usable.
func Online() bool {
	c, err := net.DialTimeout("tcp", "1.1.1.1:443", 3*time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// Flush posts the queued events to the webhook and clears them on success. If
// the webhook is unset the queue is simply cleared, since the events are
// already in the journal and the report.
func (q *Queue) Flush(webhookURL string) error {
	if len(q.Events) == 0 {
		return nil
	}
	if webhookURL == "" {
		q.Events = nil
		q.save()
		return nil
	}
	body, err := json.Marshal(map[string]any{
		"source": "lastwatt",
		"host":   hostname(),
		"events": q.Events,
	})
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return err // keep the queue for the next attempt
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %s", resp.Status)
	}
	q.Events = nil
	q.save()
	return nil
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

// Wall broadcasts to logged-in terminals. Best effort.
func Wall(msg string) {
	cmd := exec.Command("wall", "-n")
	cmd.Stdin = bytes.NewReader([]byte("lastwatt: " + msg + "\n"))
	_ = cmd.Run()
}

// Desktop sends a notification to the graphical session, if there is one. This
// matters because a tier that tears down the display manager would otherwise
// take a logged-in user's unsaved work with it without warning.
func Desktop(msg string) {
	uid, disp := activeSession()
	if uid == "" {
		return
	}
	cmd := exec.Command("systemd-run", "--quiet", "--collect",
		"--machine="+uid+"@.host", "--user", "--pipe",
		"notify-send", "-u", "critical", "lastwatt", msg)
	if err := cmd.Run(); err == nil {
		return
	}
	// Fall back to a direct invocation with an explicit bus address.
	c := exec.Command("sudo", "-u", "#"+uid, "env",
		"DISPLAY="+disp,
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/"+uid+"/bus",
		"notify-send", "-u", "critical", "lastwatt", msg)
	_ = c.Run()
}

// activeSession finds the uid and DISPLAY of the active graphical seat.
func activeSession() (uid, display string) {
	out, err := exec.Command("loginctl", "list-sessions", "--no-legend").Output()
	if err != nil {
		return "", ""
	}
	for _, line := range bytes.Split(out, []byte("\n")) {
		f := bytes.Fields(line)
		if len(f) < 3 {
			continue
		}
		id := string(f[0])
		props, err := exec.Command("loginctl", "show-session", id,
			"-p", "User", "-p", "Active", "-p", "Type", "-p", "Display").Output()
		if err != nil {
			continue
		}
		m := map[string]string{}
		for _, l := range bytes.Split(props, []byte("\n")) {
			k, v, ok := bytes.Cut(l, []byte("="))
			if ok {
				m[string(k)] = string(v)
			}
		}
		if m["Active"] == "yes" && (m["Type"] == "x11" || m["Type"] == "wayland") {
			d := m["Display"]
			if d == "" {
				d = ":0"
			}
			return m["User"], d
		}
	}
	return "", ""
}
