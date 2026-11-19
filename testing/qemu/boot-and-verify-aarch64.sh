#!/usr/bin/env bash
# Phase 6 item 37: the aarch64/AAVMF equivalent of test/os-matrix's
# testing/qemu/boot-and-verify.sh (x86_64/OVMF) — boots an arm64 ISO
# under QEMU with AAVMF UEFI firmware (the real firmware class actual
# arm64 hardware — Raspberry Pi, ARM servers, Asahi Linux hosts — uses)
# and watches its serial console for a caller-supplied "it actually
# booted" pattern, within a bounded timeout. Runs with -accel tcg
# (software emulation of the aarch64 instruction set) rather than
# assuming KVM: this project's own dev sandbox has no /dev/kvm even for
# its native amd64, and emulating a DIFFERENT architecture (not just
# running without hardware acceleration on the same one) can never use
# KVM regardless — a real arm64 host would use KVM for same-arch VMs, but
# an amd64 CI runner booting an arm64 guest, which is what this script is
# actually for, cannot.
#
# Confirmed empirically to be noticeably slower than the x86_64/OVMF case
# (full instruction-set translation, not just missing acceleration): the
# real proof run that validated this script took several minutes of
# QEMU CPU time to reach GRUB's menu on a ~143MB Debian netboot ISO,
# versus 48 seconds for Alpine under x86_64/OVMF+TCG. Budget timeouts
# accordingly — this script's own default below is deliberately generous.
#
# Usage: boot-and-verify-aarch64.sh <iso-path> <success-regex> <timeout-seconds>
# Writes a JSON report to stdout. Always exits 0 (never kills the CI run
# by itself) — the report's own "booted" field is the result to check.
set -uo pipefail

iso_path="${1:?usage: boot-and-verify-aarch64.sh <iso-path> <success-regex> <timeout-seconds>}"
success_regex="${2:?missing success-regex}"
timeout_seconds="${3:?missing timeout-seconds}"

aavmf_code="${AAVMF_CODE:-/usr/share/AAVMF/AAVMF_CODE.fd}"
aavmf_vars_src="${AAVMF_VARS:-/usr/share/AAVMF/AAVMF_VARS.fd}"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

aavmf_vars="$workdir/AAVMF_VARS.fd"
cp "$aavmf_vars_src" "$aavmf_vars"

serial_log="$workdir/serial.log"
: > "$serial_log"

start_ts=$(date +%s)

qemu-system-aarch64 \
    -machine virt -accel tcg -cpu max -m 2048 \
    -drive if=pflash,format=raw,readonly=on,file="$aavmf_code" \
    -drive if=pflash,format=raw,file="$aavmf_vars" \
    -drive file="$iso_path",media=cdrom,readonly=on,if=virtio \
    -boot order=d \
    -nographic -serial file:"$serial_log" \
    -display none -no-reboot -monitor none \
    >"$workdir/qemu.out" 2>&1 &
qemu_pid=$!

matched="false"
timed_out="false"
elapsed=0
while kill -0 "$qemu_pid" 2>/dev/null; do
    # Strip ANSI/VT escape sequences before matching — GRUB/the installer
    # draw their menu via cursor-positioning and color codes that can
    # otherwise split literal text across escape sequences, defeating a
    # plain substring/regex match against the raw serial stream.
    if sed -r 's/\x1b\[[0-9;]*[a-zA-Z]//g' "$serial_log" 2>/dev/null | grep -qE "$success_regex"; then
        matched="true"
        break
    fi
    elapsed=$(( $(date +%s) - start_ts ))
    if [ "$elapsed" -ge "$timeout_seconds" ]; then
        timed_out="true"
        break
    fi
    sleep 2
done

kill "$qemu_pid" 2>/dev/null || true
wait "$qemu_pid" 2>/dev/null || true

duration=$(( $(date +%s) - start_ts ))
log_tail="$(sed -r 's/\x1b\[[0-9;]*[a-zA-Z]//g' "$serial_log" 2>/dev/null | tr -d '\000' | strings | tail -c 2000 | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))' 2>/dev/null || echo '""')"

cat <<EOF
{
  "iso_path": "$iso_path",
  "success_regex": "$success_regex",
  "booted": $matched,
  "timed_out": $timed_out,
  "duration_seconds": $duration,
  "accel": "tcg",
  "arch": "aarch64",
  "serial_log_tail": $log_tail
}
EOF
