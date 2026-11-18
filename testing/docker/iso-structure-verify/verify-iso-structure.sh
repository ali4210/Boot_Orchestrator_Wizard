#!/usr/bin/env bash
# Phase 3 "Tier 2": checks a catalog ISO's on-disk layout against the
# exact same search logic engine/autonomous_installer_script.go's
# renderAutonomousInstallerScript embeds for the real autonomous dual-boot
# installer (the SQUASH_FILE candidates: casper/filesystem.squashfs,
# live/filesystem.squashfs, install/filesystem.squashfs, or any
# *.squashfs; falling back to a /live directory copy, or finally a
# generic rsync of the whole ISO). Never mounts the ISO (no loop device,
# no privileged container needed) — reads its directory tree directly via
# xorriso, which works against the read-only ISO9660 image file itself.
#
# Usage: verify-iso-structure.sh <path-to-iso>
# Writes a JSON report to stdout; exits 0 regardless of what it finds
# (this is a report generator, not a gate — see OS_STATUS_MATRIX.md for
# how the results are used).
set -euo pipefail

iso_path="${1:?usage: verify-iso-structure.sh <path-to-iso>}"

listing="$(xorriso -indev "$iso_path" -find / -type f 2>/dev/null | sed -n 's/^.*'"'"'\(.*\)'"'"'$/\1/p')"

squashfs_path=""
for candidate in /casper/filesystem.squashfs /live/filesystem.squashfs /install/filesystem.squashfs; do
    if printf '%s\n' "$listing" | grep -qiF "$candidate"; then
        squashfs_path="$candidate"
        break
    fi
done
if [ -z "$squashfs_path" ]; then
    squashfs_path="$(printf '%s\n' "$listing" | grep -i '\.squashfs$' | head -n1 || true)"
fi

live_dir_present="false"
if printf '%s\n' "$listing" | grep -qiE '^/live/'; then
    live_dir_present="true"
fi

efi_boot_present="false"
if printf '%s\n' "$listing" | grep -qiE '^/efi/boot/boot(x64|ia32|aa64)\.efi$'; then
    efi_boot_present="true"
fi

squashfs_found="false"
if [ -n "$squashfs_path" ]; then
    squashfs_found="true"
fi

compatible="false"
notes="no squashfs and no /live directory found — the autonomous installer script would fall through to its generic rsync-everything fallback, which is NOT confirmed to produce a bootable installed system for this layout"
if [ "$squashfs_found" = "true" ]; then
    compatible="true"
    notes="squashfs found at $squashfs_path — matches the autonomous installer script's primary unsquashfs path"
elif [ "$live_dir_present" = "true" ]; then
    compatible="true"
    notes="no squashfs, but a /live directory is present — matches the autonomous installer script's secondary cp -a fallback"
fi

cat <<EOF
{
  "iso_path": "$iso_path",
  "squashfs_found": $squashfs_found,
  "squashfs_path": "$squashfs_path",
  "live_dir_present": $live_dir_present,
  "efi_boot_present": $efi_boot_present,
  "autonomous_installer_compatible": $compatible,
  "notes": "$notes"
}
EOF
