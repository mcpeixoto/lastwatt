#!/usr/bin/env bash
# Workstream A — prerequisite power fixes that require root.
# Review before running. Every change is reversible and printed as it happens.
set -euo pipefail

say() { printf '\n=== %s\n' "$*"; }

say "A1: disarm the UPower critical-action hang"
# CriticalPowerAction=HybridSleep cannot succeed here: hibernation was never
# configured (no resume= on the kernel cmdline, /sys/power/resume reads 0:0).
# On 2026-08-31 this hung the machine half-suspended and wasted its last 28
# minutes of battery. PowerOff always works.
if grep -q '^CriticalPowerAction=HybridSleep' /etc/UPower/UPower.conf; then
    cp -a /etc/UPower/UPower.conf /etc/UPower/UPower.conf.bak-lastwatt
    sed -i 's/^CriticalPowerAction=HybridSleep/CriticalPowerAction=PowerOff/' /etc/UPower/UPower.conf
    systemctl restart upower
    echo "  set CriticalPowerAction=PowerOff (backup: UPower.conf.bak-lastwatt)"
else
    echo "  already not HybridSleep, nothing to do"
fi

say "A3: release the discrete GPU (~10 W idle)"
# nvidia-persistenced holds a handle on the GTX 1660 Ti, which defeats runtime
# D3cold. Nothing is using the GPU. prime-select is already 'on-demand', so it
# wakes again automatically if anything ever needs it.
if systemctl is-enabled nvidia-persistenced &>/dev/null || systemctl is-active nvidia-persistenced &>/dev/null; then
    systemctl disable --now nvidia-persistenced
    sleep 3
    echo "  runtime_status now: $(cat /sys/bus/pci/devices/0000:01:00.0/power/runtime_status 2>/dev/null || echo unknown)"
else
    echo "  already disabled"
fi

say "A5: fix hd-idle (currently HD_IDLE_OPTS=\"-h\", which prints usage and exits)"
if grep -q 'HD_IDLE_OPTS="-h"' /etc/default/hd-idle 2>/dev/null; then
    cp -a /etc/default/hd-idle /etc/default/hd-idle.bak-lastwatt
    sed -i 's|^HD_IDLE_OPTS="-h"|HD_IDLE_OPTS="-i 0 -a /dev/sda -i 600"|' /etc/default/hd-idle
    systemctl restart hd-idle || true
    echo "  hd-idle will now spin down /dev/sda after 10 minutes idle"
else
    echo "  already customised, leaving alone"
fi

say "A5: disable vestigial services (no ZFS pools exist on this machine)"
for u in zfs-zed.service zfs-import-cache.service zfs-mount.service zfs-share.service zfs-volume-wait.service; do
    if systemctl is-enabled "$u" &>/dev/null; then
        systemctl disable --now "$u" 2>/dev/null && echo "  disabled $u" || true
    fi
done

say "Install lastwatt"
make -C "$(dirname "$0")/.." install

say "Done. Next steps (as your user):"
cat <<'NEXT'
  lastwatt doctor        # should now report far fewer issues
  lastwatt simulate      # dry run, changes nothing
  sudo systemctl daemon-reload && sudo systemctl enable --now lastwatt
NEXT
