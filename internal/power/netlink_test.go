package power

import "testing"

func TestParseUeventACTransition(t *testing.T) {
	raw := "change@/devices/LNXSYSTM:00/ACPI0003:00/power_supply/AC0\x00" +
		"ACTION=change\x00SUBSYSTEM=power_supply\x00" +
		"POWER_SUPPLY_NAME=AC0\x00POWER_SUPPLY_TYPE=Mains\x00POWER_SUPPLY_ONLINE=0\x00"
	ev, ok := parseUevent([]byte(raw))
	if !ok {
		t.Fatal("expected a power_supply event")
	}
	if ev.Name != "AC0" || ev.Online != "0" || ev.Action != "change" {
		t.Fatalf("bad decode: %+v", ev)
	}
}

// Battery devices emit a change event on every capacity tick. Those must be
// distinguishable from mains transitions or the daemon wakes constantly.
func TestParseUeventBatteryTick(t *testing.T) {
	raw := "change@/devices/.../power_supply/BAT0\x00" +
		"ACTION=change\x00SUBSYSTEM=power_supply\x00POWER_SUPPLY_NAME=BAT0\x00"
	ev, ok := parseUevent([]byte(raw))
	if !ok {
		t.Fatal("expected a power_supply event")
	}
	if ev.Name != "BAT0" {
		t.Fatalf("want BAT0, got %q", ev.Name)
	}
}

func TestParseUeventIgnoresOtherSubsystems(t *testing.T) {
	raw := "add@/devices/pci0000:00/usb1\x00ACTION=add\x00SUBSYSTEM=usb\x00"
	if _, ok := parseUevent([]byte(raw)); ok {
		t.Fatal("non-power_supply events must be filtered out")
	}
}
