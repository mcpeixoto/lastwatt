# lastwatt

**Keep a laptop-as-server alive through a mains outage by shedding load instead of shutting down.**

A laptop running as a home server already has a UPS bolted to it: its battery. But nothing on a stock Linux system *uses* it that way. When the power goes out, the machine keeps the discrete GPU awake, the panel lit and every container running, then dies at full tilt an hour later.

`lastwatt` turns that battery into real ride-through time. It detects mains loss in milliseconds, progressively sheds load in reversible stages, and restores the **exact** prior state when power returns — with no human involvement.

On the machine it was written for, that took runtime from **1 h 52 m to a projected 4–5 h** on the same worn-out battery.

---

## The problem, concretely

This project started with a forensic post-mortem of a real outage:

```
12:03:56  mains lost      (TLP switches to battery; the router dies in the same second)
13:28:06  battery at 2%
13:28:26  UPower fires CriticalPowerAction=HybridSleep
13:28:27  PM: hibernation entry ... and then nothing
          hibernation was never configured, so it hung half-suspended:
          network asleep, CPU awake, burning the last 28 minutes achieving nothing
13:56:32  battery dead, hard power-off
```

Two failures. Nothing shed load — the machine averaged **28 W** against a **42 Wh** battery. And the one emergency action that did fire *could not succeed*, because it had never been configured.

Both are things `lastwatt` exists to prevent. `lastwatt doctor` finds the second class of problem on any machine.

---

## What it does

Load is shed in tiers. Each tier has its own trigger and its own reversal:

| Tier | Trigger | What it sheds |
|------|---------|---------------|
| 1 — instant | the moment mains drops | panel backlight, discrete GPU (→ D3cold), wifi/bluetooth radios, VNC, printing, mDNS, maintenance timers, idle disks |
| 2 — edge | 90 s sustained outage | public-facing containers, tunnels, reverse proxy |
| 3 — stateful | < 50% or < 45 min left | mail queue, databases — each with a generous grace period — then the container runtime |
| 4 — desktop | < 30% | the display manager and the whole graphical session |
| 5 — floor | < 5% or < 8 min left | flush notifications, write the report, `sync`, clean power-off |

**Tier 1 fires instantly** because every action in it is cheap and invisible to running services. **Everything below tier 1 is debounced**, so a thirty-second flicker never tears down your mail server.

That debounce is not hypothetical. Nine outages hit the reference machine in a single week:

```
41 m 14 s   3 s   18 s   6 m 03 s   6 s   2 s   4 m 52 s   1 m 19 s   1 h 52 m (fatal)
```

**Six of the nine were under 90 seconds.** A daemon without hysteresis would have stopped and restarted the mail server six times for outages the machine never noticed. With the tier-2 debounce, none of them get past tier 1 — where the only cost is a blanked screen that comes straight back.

Tiers are a *staircase*: engaging tier 3 implies tiers 1 and 2 are engaged too. An outage that starts with an already-low battery lands directly in the right posture instead of walking down one stage at a time while it drains.

---

## The three properties that make it safe

**Exact-state restore, never a blanket restart.** Every action records the state it displaced. Restore replays only that. A naive `docker start $(docker ps -aq)` would resurrect containers you deliberately stopped months ago; `lastwatt` brings back precisely what was running, in dependency order.

**Crash safety.** The undo journal is written to disk *before* each action, atomically. If the daemon is killed mid-outage and mains returns while it is dead, the next start notices the machine is shed while on mains and repairs it.

**It does not fight your existing power stack.** TLP re-asserts EPP, disk APM, wifi power-save and runtime-PM on every `power_supply` uevent — including battery capacity ticks, so it re-runs constantly. Anything `lastwatt` wrote to those knobs would be silently reverted minutes later. So `lastwatt` sheds **workloads**, and leaves TLP's tunables to TLP.

---

## Detection

`lastwatt` listens directly on the kernel uevent socket (`NETLINK_KOBJECT_UEVENT`) rather than installing a udev rule. That avoids udev's `RUN+=` sandbox and its 180-second event timeout, and keeps working even if `systemd-udevd` is wedged — which is exactly the situation a load-shedding daemon needs to survive. Detection latency is ~10–50 ms.

A 1-second poll of `/sys/class/power_supply/AC0/online` runs alongside it as a backstop. That is not redundancy theatre: it is what saves you under the heavy load that motivates shedding in the first place. Both paths act on edges only and converge idempotently.

UPower is deliberately *not* the trigger. It is strictly downstream of the same uevent, adds two failure domains, and is the component whose broken critical action caused the original hang.

Runtime remaining is computed as `energy_now / EWMA(power_now)`. Many EC firmwares report `power_now` as `0` on mains and only produce a usable figure a few seconds into a discharge, so zero samples are discarded and tiers that trigger on time-remaining stay dormant until the estimate is trustworthy.

---

## Install

Requires Go 1.24+ to build. The result is a single static binary with no runtime dependencies.

```sh
git clone https://github.com/mcpeixoto/lastwatt
cd lastwatt
make build
sudo make install
```

Then **before enabling anything**, see what it would do:

```sh
lastwatt simulate     # dry run: changes nothing
```

`simulate` resolves every action against the live machine and reports exactly which units, containers and files it would touch. Review it, edit `/etc/lastwatt/lastwatt.toml`, and only then:

```sh
sudo systemctl enable --now lastwatt
```

---

## Commands

```
lastwatt status            AC state, battery, health, draw, estimated runtime, current tier
lastwatt simulate          dry-run every tier and print exactly what would happen
lastwatt shed <n|all>      manually engage tiers, for testing
lastwatt restore           restore everything shed
lastwatt report            print the most recent outage post-mortem
lastwatt doctor            audit this machine for power-management hazards
lastwatt run               the daemon (what the systemd unit calls)
```

### `lastwatt doctor`

Audits for the misconfigurations that silently defeat load shedding. Every check came from a real problem found on the first host it ran on:

- a `CriticalPowerAction` that **cannot succeed** because hibernation was never configured — the exact failure that hung the machine above
- GNOME's `lid-close-*-action` overriding logind's `HandleLidSwitch=ignore`, turning a closed lid into an unintended power-off path on an always-on server
- `nvidia-persistenced` pinning a discrete GPU awake, defeating runtime D3cold — commonly ~10 W at idle, the single largest item on a laptop server
- crashlooping containers, which keep the CPU out of deep C-states around the clock
- `hd-idle` shipping its Debian default `HD_IDLE_OPTS="-h"`, which prints usage and exits, so no disk ever spins down

---

## Configuration

TOML, at `/etc/lastwatt/lastwatt.toml`. Tiers are config, not code — actions implement a small `Apply`/`Revert` interface, so the whole policy is declarative.

```toml
[[tiers]]
name  = "edge"
after = "90s"                 # debounce: ignore brief flickers

  [[tiers.actions]]
  type         = "docker"
  stop_timeout = 90           # mail queues need more than the default 10s
  containers   = ["mailserver"]

  [[tiers.actions]]
  type  = "systemd"
  units = ["cloudflared.service"]
```

Triggers: `immediate`, `after`, `below_percent`, `below_remaining`. A tier fires when **any** of them is met.

Action types: `systemd` (stop, optionally runtime-mask), `docker` (with per-container grace), `sysfs` (records and restores the previous value), `rfkill`, `mount`, and `exec` with a paired restore command.

A worked example for a real host is in [`configs/examples/`](configs/examples/).

---

## What this is not

**It is not a UPS replacement.** It cannot help a desktop with no battery, it cannot ride out a multi-day outage, and it will not protect you from a surge. What it does is make the battery you already own last several times longer, and guarantee the machine ends up shut down cleanly rather than dying dirty with a mail queue open.

It also does not address the other half of the problem: many laptops need a physical button press to come back after a power-off. Consumer laptop firmware generally has no "restore on AC power loss" setting — that is a desktop and server-board feature — so assume a shutdown means a physical trip unless you have tested otherwise. Set your floor tier accordingly: the lower it is, the more chance mains returns before you lose the machine.

**Testing it takes five minutes and no battery drain:** power the machine off, unplug the AC brick at the wall, wait 30 seconds, plug it back in, and watch for 90 seconds. If it stays dark, there is no auto-power-on.

One warning from building this: if your CMOS/RTC backup cell is dead, a full battery drain resets the clock to the BIOS default, and `systemd-timesyncd` then restores a *fabricated* time from a file mtime. Every timestamp before the first NTP sync is fiction, `journalctl -b -N` indices become meaningless, and post-mortems will lie to you convincingly. `lastwatt doctor` checks for this.

---

## License

MIT — see [LICENSE](LICENSE).
