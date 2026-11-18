#!/usr/bin/env python3
"""Prints the serial-console success regex testing/qemu/boot-and-verify.sh
should watch for, for a given catalog entry ID.

VERIFIED entries below were actually booted under QEMU/OVMF (TCG, no KVM)
in a real run and the exact pattern confirmed to appear on the serial
console — see OS_STATUS_MATRIX.md for which ones and when. Every other
entry falls back to a generic, best-effort pattern that only proves the
firmware handed off to *some* bootloader/kernel text on the serial
console — it does NOT confirm reaching a working login prompt, and may
need a kernel console=ttyS0 argument or a tighter pattern once that entry
is actually run for the first time. Treat an unverified entry's "booted"
result in a report with that in mind, not as equivalent to a VERIFIED one.
"""
import sys

VERIFIED = {
    # Confirmed in this session: boots to Alpine's own GRUB menu, then the
    # kernel, then a "localhost login:" prompt, all on the serial console
    # with no extra kernel arguments needed.
    "alpine-linux-amd64": "login:",
}

# Best-effort, UNVERIFIED default: matches common early-boot text any
# GRUB/syslinux/isolinux-based x86 installer or live image is likely to
# print to a BIOS/UEFI-redirected serial console before handing off to the
# kernel, even if the kernel's own console output then goes to VGA only.
GENERIC_FALLBACK = r"(GRUB|isolinux|SYSLINUX|Loading Linux|Booting|login:|BusyBox)"


def main():
    if len(sys.argv) != 2:
        print("usage: boot_success_pattern.py <entry-id>", file=sys.stderr)
        sys.exit(2)
    print(VERIFIED.get(sys.argv[1], GENERIC_FALLBACK))


if __name__ == "__main__":
    main()
