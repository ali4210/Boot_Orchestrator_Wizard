# Architecture Support (Phase 6, item 35)

What's real and tested, per architecture, as of this pass — not a target or a plan.

| Architecture | This binary builds | This binary's own code runs | Catalog entries | Real boot proof |
|---|---|---|---|---|
| **amd64** | ✅ | ✅ — this is the primary development/CI platform | 26 of 27 Linux/Windows/macOS entries | ✅ — Alpine booted under QEMU/OVMF (`OS_STATUS_MATRIX.md`, Phase 3) |
| **arm64** | ✅ (`GOOS=linux GOARCH=arm64 go build ./...`, confirmed clean) | Not run on real arm64 hardware this pass; the arch-specific code path (below) is unit-tested, not hardware-verified | 2 (`debian-netboot-arm64`, `ubuntu-server-arm64` — see item 36) | ✅ — real Debian netboot ISO booted under `qemu-system-aarch64` + AAVMF, reached the actual GRUB installer menu (see item 37) |
| **i386** | Builds (no code actively excludes it) | Not exercised at all | 0 (the one historical i386 entry, Windows XP, was removed — item 22) | Not attempted |

## What "arm64 support" means here, precisely

This tool runs *on* a machine to provision OSes onto disks attached to that same machine.
"arm64 support" is about the **host** this binary itself runs on (a Raspberry Pi, an ARM
server, an Asahi Linux machine, ...) being able to use it correctly — not about installing
an arm64 guest OS from an amd64 host, which isn't a real bare-metal scenario (you cannot
dual-boot an arm64 OS on amd64 firmware or vice versa; the CPU architectures are physically
incompatible at the firmware/bootloader level).

**Found and fixed this pass**: `engine/usb_manager.go`'s `ArmAutonomousUSBBootloader` (which
stages this tool's own autonomous-install USB stick) hardcoded x86_64 EFI binary names
(`BOOTX64.EFI`, `grubx64.efi`, GRUB's `x86_64-efi` target format) unconditionally — on a real
arm64 host, this would have silently produced a USB stick carrying an x86_64 EFI binary that
arm64 UEFI firmware cannot execute at all, i.e. autonomous bare-metal staging was completely
broken on arm64 even though the rest of the binary builds and the catalog now has real arm64
entries. Fixed via `efiBootNamesForArch(runtime.GOARCH)` (`engine/usb_manager_test.go` has
the regression test) — selects `BOOTAA64.EFI`/`grubaa64.efi`/`arm64-efi` on arm64 hosts.

**Not audited for arch-correctness this pass** (flagged, not silently assumed fine):
`engine/bcd_qemu.go` (Windows-BCD-via-QEMU helper, hardcodes `qemu-system-x86_64` and
`bootx64.efi`) and the handful of other `x86_64`/`amd64` string literals noted in
`docs/dryrun-inventory.md`'s category breakdown. These are lower priority: `bcd_qemu.go` is
Windows-specific tooling (Windows-on-ARM is a vanishingly rare target for this tool, and was
explicitly out of scope — "Debian/Ubuntu family only," item 36), and the others are
read-only/query paths or Linux-distribution-detection logic unaffected by host CPU
architecture.

## Real boot proof (item 37)

`testing/qemu/boot-and-verify-aarch64.sh` — the aarch64/AAVMF equivalent of Phase 3's
x86_64/OVMF `testing/qemu/boot-and-verify.sh` (that script lives on branch `test/os-matrix`,
not yet merged — see `docs/ROADMAP.md`). Run for real against the new
`debian-netboot-arm64` catalog entry's actual ISO: AAVMF UEFI firmware initialized, GRUB
loaded ("Welcome to GRUB! GNU GRUB version 2.06-13+deb12u2"), and the real Debian Installer
boot menu rendered ("Install", "Graphical install", "Advanced options...", "Install with
speech synthesis") — see `testing/reports/tier3-debian-netboot-arm64-*.txt` for the
decoded evidence.

**Confirmed empirically, not assumed**: aarch64 TCG emulation (no `/dev/kvm` in this sandbox,
and emulating a *different* instruction set than the host's own amd64 can never use KVM
regardless of what's available) is markedly slower than the x86_64/OVMF+TCG case from Phase
3 — reaching GRUB's menu on a ~143MB ISO took several minutes of real QEMU CPU time here,
versus 48 seconds for Alpine's x86_64 boot. Budget timeouts accordingly if running this
again; the script's design doc comment carries this same note.

**Not attempted**: actually selecting "Install" and running the Debian installer to
completion (needs real network access from inside the emulated guest for package
fetching — out of scope for a boot-reachability proof) and real arm64 *hardware* (this was
entirely QEMU/TCG emulation on amd64 host hardware, not a Raspberry Pi or other real arm64
machine).
