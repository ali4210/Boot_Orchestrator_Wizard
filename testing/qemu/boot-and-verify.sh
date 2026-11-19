#!/usr/bin/env bash
# Phase 3 "Tier 3": boots an ISO under QEMU with OVMF UEFI firmware (the
# same firmware class this tool's own real installs rely on for UEFI
# boot) and watches its serial console for a caller-supplied "it actually
# booted" pattern, within a bounded timeout. Runs with -accel tcg
# (software emulation) rather than assuming KVM is available — neither
# this project's own dev sandbox nor a default GitHub-hosted Actions
# runner has /dev/kvm, so a pipeline that silently assumed kvm would
# simply fail to start anywhere this is actually meant to run.
#
# Usage: boot-and-verify.sh <iso-path> <success-regex> <timeout-seconds>
# Writes a JSON report to stdout. Always exits 0 (never kills the CI run
# by itself) — the report's own "booted" field is the result to check.
set -uo pipefail

iso_path="${1:?usage: boot-and-verify.sh <iso-path> <success-regex> <timeout-seconds>}"
success_regex="${2:?missing success-regex}"
timeout_seconds="${3:?missing timeout-seconds}"

ovmf_code="${OVMF_CODE:-/usr/share/OVMF/OVMF_CODE_4M.fd}"
ovmf_vars_src="${OVMF_VARS:-/usr/share/OVMF/OVMF_VARS_4M.fd}"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

# OVMF_VARS is the UEFI NVRAM store; QEMU writes to it, so each run needs
# its own writable copy rather than sharing (or corrupting) the system one.
ovmf_vars="$workdir/OVMF_VARS.fd"
cp "$ovmf_vars_src" "$ovmf_vars"

serial_log="$workdir/serial.log"
: > "$serial_log"

start_ts=$(date +%s)

qemu-system-x86_64 \
    -machine q35 -accel tcg -cpu max -m 2048 \
    -drive if=pflash,format=raw,readonly=on,file="$ovmf_code" \
    -drive if=pflash,format=raw,file="$ovmf_vars" \
    -drive file="$iso_path",media=cdrom,readonly=on \
    -boot order=d \
    -nographic -serial file:"$serial_log" \
    -display none -no-reboot -monitor none \
    >"$workdir/qemu.out" 2>&1 &
qemu_pid=$!

matched="false"
timed_out="false"
elapsed=0
while kill -0 "$qemu_pid" 2>/dev/null; do
    if grep -qE "$success_regex" "$serial_log" 2>/dev/null; then
        matched="true"
        break
    fi
    elapsed=$(( $(date +%s) - start_ts ))
    if [ "$elapsed" -ge "$timeout_seconds" ]; then
        timed_out="true"
        break
    fi
    sleep 1
done

kill "$qemu_pid" 2>/dev/null || true
wait "$qemu_pid" 2>/dev/null || true

duration=$(( $(date +%s) - start_ts ))
log_tail="$(tail -c 2000 "$serial_log" 2>/dev/null | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))' 2>/dev/null || echo '""')"

cat <<EOF
{
  "iso_path": "$iso_path",
  "success_regex": "$success_regex",
  "booted": $matched,
  "timed_out": $timed_out,
  "duration_seconds": $duration,
  "accel": "tcg",
  "serial_log_tail": $log_tail
}
EOF
