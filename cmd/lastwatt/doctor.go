package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// doctor audits a machine for the power-management hazards that silently defeat
// load shedding. Every check here came from a real misconfiguration found on
// the first host lastwatt ran on.
func doctor() int {
	problems := 0
	warn := func(title, detail, fix string) {
		problems++
		fmt.Printf("  [!] %s\n      %s\n      fix: %s\n\n", title, detail, fix)
	}
	ok := func(title string) { fmt.Printf("  [ok] %s\n", title) }

	fmt.Print("lastwatt doctor — auditing power-management configuration\n\n")

	// 1. A critical-power action that cannot succeed is worse than none: the
	//    machine wedges half-suspended and drains the rest of the battery awake.
	if b, err := os.ReadFile("/etc/UPower/UPower.conf"); err == nil {
		conf := string(b)
		action := ""
		for _, l := range strings.Split(conf, "\n") {
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "CriticalPowerAction=") {
				action = strings.TrimPrefix(l, "CriticalPowerAction=")
			}
		}
		switch action {
		case "HybridSleep", "Hibernate":
			if !hibernationConfigured() {
				warn("UPower will try "+action+" but hibernation is not configured",
					"resume= is unset on the kernel cmdline and /sys/power/resume reads 0:0, so the attempt hangs half-suspended and drains the battery awake.",
					"set CriticalPowerAction=PowerOff in /etc/UPower/UPower.conf")
			} else {
				ok("UPower critical action is " + action + " and hibernation is configured")
			}
		case "PowerOff":
			ok("UPower critical action is PowerOff")
		case "":
			ok("UPower critical action not overridden")
		}
	}

	// 2. A lid-close suspend on an always-on server is an unintended power-off
	//    path, and GNOME's inhibitor overrides logind's HandleLidSwitch=ignore.
	for _, key := range []string{"lid-close-ac-action", "lid-close-battery-action"} {
		out, err := exec.Command("gsettings", "get",
			"org.gnome.settings-daemon.plugins.power", key).Output()
		if err != nil {
			continue
		}
		v := strings.Trim(strings.TrimSpace(string(out)), "'")
		if v == "suspend" || v == "hibernate" {
			warn("GNOME "+key+" is '"+v+"'",
				"GNOME takes a logind inhibitor, so this overrides HandleLidSwitch=ignore and closing the lid will suspend the machine.",
				"gsettings set org.gnome.settings-daemon.plugins.power "+key+" 'nothing'")
		} else {
			ok("GNOME " + key + " is '" + v + "'")
		}
	}

	// 3. A discrete GPU held awake by the persistence daemon is commonly the
	//    single largest idle draw on a laptop-as-server.
	if _, err := exec.LookPath("nvidia-smi"); err == nil {
		out, _ := exec.Command("nvidia-smi",
			"--query-gpu=power.draw,persistence_mode", "--format=csv,noheader").Output()
		s := strings.TrimSpace(string(out))
		if s != "" {
			if isActive("nvidia-persistenced") {
				warn("nvidia-persistenced is running (GPU reports "+s+")",
					"The persistence daemon holds a handle on the device, which defeats runtime D3cold suspend and can cost around 10 W at idle.",
					"systemctl disable --now nvidia-persistenced")
			} else {
				ok("nvidia-persistenced not running (GPU: " + s + ")")
			}
		}
	}

	// 4. A crashlooping container burns CPU indefinitely and blocks deep C-states.
	if _, err := exec.LookPath("docker"); err == nil {
		out, _ := exec.Command("docker", "ps", "--format", "{{.Names}}\t{{.Status}}").Output()
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			name, status, ok2 := strings.Cut(l, "\t")
			if !ok2 {
				continue
			}
			if strings.Contains(status, "Restarting") {
				warn("container "+name+" is restarting ("+status+")",
					"A crashloop keeps the CPU out of deep C-states around the clock and wastes several watts continuously.",
					"fix the container, or bind it to its storage with BindsTo= so it stops cleanly instead of looping")
			}
		}
	}

	// 5. hd-idle shipping its Debian default never spins anything down.
	if b, err := os.ReadFile("/etc/default/hd-idle"); err == nil {
		if strings.Contains(string(b), `HD_IDLE_OPTS="-h"`) {
			warn("hd-idle is configured with the default HD_IDLE_OPTS=\"-h\"",
				"That prints usage and exits, so the service is dead and no disk ever spins down.",
				"set a real option string, e.g. HD_IDLE_OPTS=\"-i 600 -a sda -i 300\"")
		} else {
			ok("hd-idle has a non-default configuration")
		}
	}

	// 6. A dead CMOS cell resets the clock on every full power loss, which makes
	//    journalctl -b indices and wtmp timestamps unusable afterwards -- and
	//    silently corrupts any post-mortem you try to write.
	if out, err := exec.Command("journalctl", "-b", "0", "--no-pager", "-q",
		"-u", "systemd-timesyncd", "-g", "jumped backwards").Output(); err == nil {
		if strings.TrimSpace(string(out)) != "" {
			warn("the system clock was unset at boot and had to be restored from a file",
				"This means the RTC lost backup power, which happens when the CMOS cell is dead. Timestamps before the first NTP sync are fabricated, so journalctl -b indices and outage post-mortems become unreliable.",
				"replace the CMOS/RTC backup cell; until then, trust monotonic time and NTP-synced entries only")
		} else {
			ok("system clock survived the last boot (RTC backup healthy)")
		}
	}

	fmt.Println()
	if problems == 0 {
		fmt.Println("No hazards found.")
		return 0
	}
	fmt.Printf("%d issue(s) found.\n", problems)
	return 1
}

func isActive(unit string) bool {
	out, _ := exec.Command("systemctl", "is-active", unit).Output()
	return strings.TrimSpace(string(out)) == "active"
}

// hibernationConfigured reports whether a resume device is actually set up.
func hibernationConfigured() bool {
	b, err := os.ReadFile("/sys/power/resume")
	if err != nil {
		return false
	}
	v := strings.TrimSpace(string(b))
	return v != "" && v != "0:0"
}
