// Package power reads AC/battery state from sysfs and watches kernel uevents.
package power

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const sysfsRoot = "/sys/class/power_supply"

// Supply addresses one mains device and one battery device.
type Supply struct {
	AC  string // e.g. "AC0"
	Bat string // e.g. "BAT0"
}

// Reading is a point-in-time sample of the battery.
type Reading struct {
	OnAC       bool
	Percent    float64
	EnergyNow  int64 // microwatt-hours
	EnergyFull int64 // microwatt-hours
	PowerNow   int64 // microwatts; reads 0 while on mains
	Status     string
	Present    bool
}

// EnergyWh returns remaining energy in watt-hours.
func (r Reading) EnergyWh() float64 { return float64(r.EnergyNow) / 1e6 }

// PowerW returns instantaneous draw in watts.
func (r Reading) PowerW() float64 { return float64(r.PowerNow) / 1e6 }

// HealthPct is actual capacity against design capacity, or 0 if unknown.
func (r Reading) HealthPct(designUWh int64) float64 {
	if designUWh <= 0 {
		return 0
	}
	return float64(r.EnergyFull) / float64(designUWh) * 100
}

func readAttr(dev, attr string) (string, error) {
	b, err := os.ReadFile(filepath.Join(sysfsRoot, dev, attr))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func readInt(dev, attr string) int64 {
	s, err := readAttr(dev, attr)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// OnAC reports whether mains power is present. This is the authoritative bit:
// a two-byte read backed by an ACPI _PSR evaluation, cheap enough to poll at 1Hz
// and immune to udevd, dbus or upowerd being wedged.
func (s Supply) OnAC() (bool, error) {
	v, err := readAttr(s.AC, "online")
	if err != nil {
		return false, fmt.Errorf("read %s/online: %w", s.AC, err)
	}
	return v == "1", nil
}

// Read samples the full power state.
func (s Supply) Read() (Reading, error) {
	onAC, err := s.OnAC()
	if err != nil {
		return Reading{}, err
	}
	r := Reading{
		OnAC:       onAC,
		EnergyNow:  readInt(s.Bat, "energy_now"),
		EnergyFull: readInt(s.Bat, "energy_full"),
		PowerNow:   readInt(s.Bat, "power_now"),
	}
	if st, err := readAttr(s.Bat, "status"); err == nil {
		r.Status = st
	}
	if p, err := readAttr(s.Bat, "present"); err == nil {
		r.Present = p == "1"
	}
	// Prefer the kernel's own percentage; fall back to the energy ratio.
	if c, err := readAttr(s.Bat, "capacity"); err == nil {
		if n, err := strconv.ParseFloat(c, 64); err == nil {
			r.Percent = n
		}
	} else if r.EnergyFull > 0 {
		r.Percent = float64(r.EnergyNow) / float64(r.EnergyFull) * 100
	}
	return r, nil
}

// DesignEnergy returns energy_full_design in microwatt-hours.
func (s Supply) DesignEnergy() int64 { return readInt(s.Bat, "energy_full_design") }

// Detect finds the first mains and battery device in sysfs. Explicit names in
// the config always win; this is only the fallback for unconfigured hosts.
func Detect() (Supply, error) {
	entries, err := os.ReadDir(sysfsRoot)
	if err != nil {
		return Supply{}, fmt.Errorf("no %s: %w", sysfsRoot, err)
	}
	var s Supply
	for _, e := range entries {
		t, err := readAttr(e.Name(), "type")
		if err != nil {
			continue
		}
		switch t {
		case "Mains":
			if s.AC == "" {
				s.AC = e.Name()
			}
		case "Battery":
			// Skip peripheral batteries (mice, keyboards); they are not the
			// system battery and would give nonsense runtime estimates.
			if strings.HasPrefix(e.Name(), "hid") || strings.HasPrefix(e.Name(), "hidpp") {
				continue
			}
			if s.Bat == "" {
				s.Bat = e.Name()
			}
		}
	}
	if s.AC == "" {
		return s, fmt.Errorf("no Mains power supply found under %s", sysfsRoot)
	}
	if s.Bat == "" {
		return s, fmt.Errorf("no system Battery found under %s", sysfsRoot)
	}
	return s, nil
}
