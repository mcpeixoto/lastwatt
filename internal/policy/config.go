// Package policy parses lastwatt's declarative configuration and decides which
// shed tier should be engaged for a given power state.
package policy

import (
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/mcpeixoto/lastwatt/internal/actions"
)

// Duration wraps time.Duration so TOML can carry "90s" / "5m" strings.
type Duration time.Duration

func (d *Duration) UnmarshalText(text []byte) error {
	if len(text) == 0 {
		*d = 0
		return nil
	}
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

func (d Duration) D() time.Duration { return time.Duration(d) }

// Config is the whole of lastwatt.toml.
type Config struct {
	General GeneralCfg `toml:"general"`
	Power   PowerCfg   `toml:"power"`
	Notify  NotifyCfg  `toml:"notify"`
	Tiers   []TierCfg  `toml:"tiers"`
}

type GeneralCfg struct {
	PollInterval Duration `toml:"poll_interval"`
	StateFile    string   `toml:"state_file"`
	ReportDir    string   `toml:"report_dir"`
	EWMAAlpha    float64  `toml:"ewma_alpha"`
	// RestoreDelay is how long mains must be stable before restoring. Guards
	// against a flapping supply repeatedly tearing services up and down.
	RestoreDelay Duration `toml:"restore_delay"`
}

type PowerCfg struct {
	AC      string `toml:"ac"`
	Battery string `toml:"battery"`
}

type NotifyCfg struct {
	// WebhookURL receives the queued outage summary once connectivity returns.
	WebhookURL string `toml:"webhook_url"`
	QueueFile  string `toml:"queue_file"`
	// Wall broadcasts tier changes to logged-in terminals.
	Wall bool `toml:"wall"`
	// Desktop sends a notify-send to the graphical session, if there is one.
	Desktop bool `toml:"desktop"`
}

// TierCfg is one shed stage. A tier engages when ANY of its thresholds is met.
type TierCfg struct {
	Name string `toml:"name"`
	// Immediate fires the tier the instant mains drops, with no delay. Reserve
	// it for actions that are cheap and invisible to running services.
	Immediate bool `toml:"immediate"`
	// After is sustained time on battery before the tier engages.
	After Duration `toml:"after"`
	// BelowPercent engages the tier below this battery percentage. 0 disables.
	BelowPercent float64 `toml:"below_percent"`
	// BelowRemaining engages the tier below this estimated runtime. 0 disables.
	BelowRemaining Duration `toml:"below_remaining"`
	// Shutdown makes this tier the terminal floor: flush, sync, power off.
	Shutdown bool `toml:"shutdown"`

	Actions []actions.Spec `toml:"actions"`
}

// Tier is a built tier with concrete actions.
type Tier struct {
	Cfg     TierCfg
	Actions []actions.Action
}

// Load reads and validates a config file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := toml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.General.PollInterval == 0 {
		c.General.PollInterval = Duration(time.Second)
	}
	if c.General.StateFile == "" {
		c.General.StateFile = "/var/lib/lastwatt/state.json"
	}
	if c.General.ReportDir == "" {
		c.General.ReportDir = "/var/lib/lastwatt/reports"
	}
	if c.General.EWMAAlpha == 0 {
		c.General.EWMAAlpha = 0.2
	}
	if c.General.RestoreDelay == 0 {
		c.General.RestoreDelay = Duration(5 * time.Second)
	}
	if c.Notify.QueueFile == "" {
		c.Notify.QueueFile = "/var/lib/lastwatt/notify-queue.json"
	}
}

// Validate catches config mistakes at load time rather than mid-outage.
func (c *Config) Validate() error {
	if len(c.Tiers) == 0 {
		return fmt.Errorf("no tiers defined")
	}
	seen := map[string]bool{}
	var shutdownIdx = -1
	for i, t := range c.Tiers {
		if t.Name == "" {
			return fmt.Errorf("tier %d has no name", i+1)
		}
		if seen[t.Name] {
			return fmt.Errorf("duplicate tier name %q", t.Name)
		}
		seen[t.Name] = true
		if !t.Immediate && t.After == 0 && t.BelowPercent == 0 && t.BelowRemaining == 0 && !t.Shutdown {
			return fmt.Errorf("tier %q has no trigger condition "+
				"(set immediate, after, below_percent, below_remaining or shutdown)", t.Name)
		}
		if t.BelowPercent < 0 || t.BelowPercent > 100 {
			return fmt.Errorf("tier %q: below_percent must be 0-100", t.Name)
		}
		if t.Shutdown {
			if shutdownIdx >= 0 {
				return fmt.Errorf("more than one shutdown tier (%q and %q)", c.Tiers[shutdownIdx].Name, t.Name)
			}
			shutdownIdx = i
		}
		for j, a := range t.Actions {
			if _, err := actions.Build(a); err != nil {
				return fmt.Errorf("tier %q action %d: %w", t.Name, j+1, err)
			}
		}
	}
	if shutdownIdx >= 0 && shutdownIdx != len(c.Tiers)-1 {
		return fmt.Errorf("shutdown tier %q must be the last tier", c.Tiers[shutdownIdx].Name)
	}
	return nil
}

// BuildTiers materialises the configured actions.
func (c *Config) BuildTiers() ([]Tier, error) {
	out := make([]Tier, 0, len(c.Tiers))
	for _, tc := range c.Tiers {
		t := Tier{Cfg: tc}
		for _, spec := range tc.Actions {
			a, err := actions.Build(spec)
			if err != nil {
				return nil, fmt.Errorf("tier %q: %w", tc.Name, err)
			}
			t.Actions = append(t.Actions, a)
		}
		out = append(out, t)
	}
	return out, nil
}
