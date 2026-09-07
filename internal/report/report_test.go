package report

import (
	"strings"
	"testing"
	"time"
)

// The 2026-09-05 outage on ubuntudesktop, which is what these tests are for.
//
// The machine lost mains at 15:56:08, ran 2h19m39s on battery, hit the floor
// and powered off at 18:15:47 with 3% left. Mains had NOT returned. The report
// said:
//
//	**Mains restored:** 2026-09-05 18:15:47
//	18:15:47  mains restored (battery 3.0%)
//
// which reads as "power came back and the machine shut down 17 seconds later"
// -- a race condition that never happened. The daemon log is unambiguous: the
// last thing it wrote was BATTERY FLOOR REACHED, and there is no MAINS RESTORED
// line anywhere in that boot.
//
// Cost of the wrong label: an hour of looking for a shutdown race in a daemon
// that had behaved perfectly.
func diedOnBattery() Outage {
	start := time.Date(2026, 9, 5, 15, 56, 8, 0, time.UTC)
	return Outage{
		Host:         "ubuntudesktop",
		Start:        start,
		End:          start.Add(2*time.Hour + 19*time.Minute + 39*time.Second),
		StartPercent: 100,
		EndPercent:   3,
		EnergyFullWh: 42.2,
		Survived:     false,
		Tiers: []TierEvent{
			{At: start.Add(time.Second), Level: 1, Name: "instant",
				Reason: "instant: mains lost", Shed: true},
		},
		Samples: []Sample{
			{At: start, Percent: 100, Watts: 58.3},
			{At: start.Add(2 * time.Hour), Percent: 3, Watts: 15.8},
		},
	}
}

func survived() Outage {
	o := diedOnBattery()
	o.Survived = true
	o.EndPercent = 62
	return o
}

func TestADeathDoesNotClaimMainsReturned(t *testing.T) {
	s := diedOnBattery().Render()
	if strings.Contains(s, "Mains restored") {
		t.Errorf("report claims mains returned when it did not:\n%s", s)
	}
	if strings.Contains(s, "mains restored") {
		t.Errorf("timeline line claims mains returned:\n%s", s)
	}
}

func TestADeathSaysItWasADeath(t *testing.T) {
	s := diedOnBattery().Render()
	for _, want := range []string{
		"Powered off at battery floor",
		"Mains had not returned",
		"Charge at shutdown",
		"battery floor reached — powered off",
		"The battery floor was reached",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in report:\n%s", want, s)
		}
	}
}

func TestTheReportDoesNotContradictItself(t *testing.T) {
	// Saying "mains restored" and "the battery floor was reached" in the same
	// report is a contradiction -- and that is exactly how it came out.
	s := diedOnBattery().Render()
	if strings.Contains(s, "restored") && strings.Contains(s, "floor was reached") {
		t.Errorf("self-contradictory report:\n%s", s)
	}
}

func TestASurvivedOutageStillSaysRestored(t *testing.T) {
	s := survived().Render()
	if !strings.Contains(s, "Mains restored") {
		t.Errorf("outage survived but the report does not say so:\n%s", s)
	}
	if !strings.Contains(s, "Charge at restore") {
		t.Errorf("outage survived but the table talks about shutdown:\n%s", s)
	}
	if strings.Contains(s, "battery floor") {
		t.Errorf("outage survived but the report mentions the floor:\n%s", s)
	}
}

func TestAnOngoingOutageHasNoOutcome(t *testing.T) {
	o := diedOnBattery()
	o.End = time.Time{}
	s := o.Render()
	if !strings.Contains(s, "ongoing") {
		t.Errorf("ongoing outage not reported as ongoing:\n%s", s)
	}
	if strings.Contains(s, "Powered off") || strings.Contains(s, "Mains restored") {
		t.Errorf("ongoing outage already announces an outcome:\n%s", s)
	}
}

func TestTheBatteryTableIsStillRight(t *testing.T) {
	s := diedOnBattery().Render()
	for _, want := range []string{
		"| Charge at outage start | 100.0% |",
		"| Charge at shutdown | 3.0% |",
		"| Consumed | 97.0% of pack |",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
}
