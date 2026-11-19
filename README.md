# Boot Orchestrator Wizard

A CLI/TUI tool for installing, dual-booting, backing up, and reverting operating systems on
bare-metal machines or VMs — Linux, Windows, and (recovery-image-only) macOS.

**Status:** under active security/correctness remediation following a full code audit
(`AUDIT_REPORT.md`). Most Critical/High findings are fixed with real reproducing tests —
see `PROGRESS.md` for the authoritative, item-by-item status (every entry there has either a
real commit hash + test, or an honest "not started"/"known limitation" note; nothing is
marked done without proof). Read `PROGRESS.md` before relying on any specific claim below.

**This tool performs genuinely destructive operations** — partitioning, UEFI NVRAM writes,
bootloader (GRUB/BCD/OpenCore) edits, filesystem formatting. Even with the hardening done so
far, only point it at a disk you are prepared to lose, and test against loopback devices
(`losetup`) or disposable VM disks first.

## How to run it

```
./autorun.sh          # Linux
autorun.bat           # Windows (double-click, or run from cmd/PowerShell)
./autorun.command     # macOS (double-click in Finder, or run from Terminal)
```

Each script checks for a Go toolchain matching `go.mod`'s `go` directive, runs
`go mod download`, builds `cmd/orchestrator` into `bin/`, and launches it. Partition/NVRAM/
BCD operations need root (Linux/macOS: `sudo ./autorun.sh`) or Administrator (Windows) —
`autorun.sh` prompts for confirmation before installing any missing host package, and
requests elevation only once, right before running the already-built binary.

Before the TUI (or headless mode) does anything, a pre-flight check runs automatically
(hypervisor/firmware detection, disk headroom, catalog sanity) and refuses to continue on a
genuinely blocking issue — pass `--skip-preflight` to continue anyway.

### Headless / scripted use

```
./bin/orchestrator --headless --yes \
  --os-id <catalog-id> \
  --target-disk /dev/sdX \
  --username <name> --password-file <path-to-a-file-containing-the-password>
```

`--iso-path <path>` provisions a local ISO/image instead of a catalog entry (mutually
exclusive with `--os-id`). `--allow-unverified` is required if the resolved image has no
checksum to verify against (every `--iso-path`, and some catalog entries — see
`docs/MANIFEST_AUDIT.md`). `--revert --yes` reverts a dual-boot install back to single-boot
(`--keep-slice` reformats the added partition instead of deleting it). Nothing here invents
a default account or password — `--username`/`--password-file` are required.

### Interactively picking a local ISO

`--iso <path>` skips catalog/role selection and goes straight into the deploy flow with that
file. Inside the TUI, the "Deploy from Local Payload" option under the Client role also lets
you browse to one.

## What's implemented

- **TUI** (`ui/`) — Bubble Tea/Lip Gloss shell: mode select → OS catalog (fuzzy-filterable,
  with an explicit confirmation gate for any entry with no checksum) → target disk/USB
  selection → credentials → confirm → live progress → done. Also has dedicated flows for
  LAN peer / SCP / USB-staged custom payloads, disaster backup & restore, and reverting a
  dual-boot install back to single-boot.
- **Provisioning engine** (`engine/`) — a single `safety.Journal`-backed transaction covering
  download (with stall-watchdog/retry/resume), checksum verification, OpenPGP signature
  verification (when a catalog entry provides one — none currently do, see
  `docs/MANIFEST_AUDIT.md`), partitioning, OS deployment (Linux rootfs extraction, Windows
  WIM/ESD apply, macOS recovery payload), and bootloader registration (GRUB/BCD/OpenCore),
  with rollback handlers for every step and an explicit, documented exception where true
  rollback isn't possible (image-deploy, after the target partition is already formatted).
- **Revert** — `engine.ExecuteDecommissionWithOptions` undoes a dual-boot install: removes
  the added partition, restores the host partition's size (and recreates an equivalent swap
  partition if one was converted at install time), cleans dangling `/etc/fstab` entries,
  purges the EFI boot entry and ESP payload, and regenerates GRUB.
- **Autonomous bare-metal USB staging** — for installing onto the disk the tool is actively
  running from, stages the payload + a generated installer script onto a USB drive, arms a
  one-shot UEFI boot entry, and reboots into it.
- **Safety gates** — refuses to touch a disk with LVM/LUKS/mdraid/BitLocker, refuses non-
  block-device targets, refuses the live root disk in single-boot mode, detects Secure
  Boot/TPM state, and the pre-flight check described above.
- **Verification tooling** (`cmd/catalog-verify`, `cmd/download-verify`, the `OS Status
  Matrix` GitHub Actions workflow on `test/os-matrix`) — see `OS_STATUS_MATRIX.md` for real
  results, including real findings (two dead manifest URLs, one catalog entry incompatible
  with the install pipeline as currently written).

## Catalog

`discovery/manifests/*.json` — 28 entries (23 Linux distros/flavors, 3 Windows editions, 2
macOS recovery images) as of this writing; the exact count drifts as entries are
added/removed, so treat this as a snapshot, not a promise (`go run ./cmd/catalog-verify`
reports the live count and each mirror's real reachability). **No entry currently has a
SHA256 checksum or OpenPGP signature** — see `docs/MANIFEST_AUDIT.md` for why, and the
confirmation gate both the TUI and headless mode enforce until that's backfilled.

## macOS caveat

Apple Silicon Macs cannot dual-boot arbitrary non-Apple OSes at the firmware level — this
tool's macOS entries are recovery/virtualization-image payloads, not a path to installing
macOS as a second bare-metal OS on Apple Silicon.

## Build

```bash
go mod download
go build ./...
go vet ./...
go test ./...                      # most tests are safe to run unprivileged and skip
                                    # root-gated/real-disk cases cleanly; see below
go run ./cmd/orchestrator          # launch the TUI
GOOS=windows GOARCH=amd64 go build ./cmd/orchestrator
GOOS=darwin  GOARCH=arm64 go build ./cmd/orchestrator
```

Some tests exercise real (but safely scoped) destructive operations against loop-device disk
images rather than mocks, and require the test process itself to run as root — they skip
cleanly otherwise. Run them explicitly when you need that coverage:

```bash
sudo -E "$(which go)" test ./engine/... -run TestExecuteDecommission -v
```

**Never** run the test suite as a way to "try out" this tool against a real disk — every
destructive test targets a disposable loop-mounted image file it creates and tears down
itself, never a real block device.

### Platform support

| Platform | Compiles | Build/test actually run on this platform's own runner |
|---|---|---|
| Linux (amd64) | ✅ | ✅ — this is the primary development platform |
| Linux (arm64) | ✅ | ❌ not yet (Phase 6, `PROGRESS.md`) |
| Windows (amd64) | ✅ | Partial — see `OS_STATUS_MATRIX.md`'s Tier 4; a `windows-latest` CI job builds and unit-tests the Windows-specific code, but nothing here has driven a real Windows install end-to-end on real/VM Windows hardware |
| macOS (Intel + Apple Silicon) | ✅ | Partial — same as Windows, via `macos-latest` CI; see the macOS caveat above |

## Contributing

See `CONTRIBUTING.md`. `PROGRESS.md` is the single source of truth for what's done, in
progress, or a known, documented limitation — read it before starting any change.
`docs/ROADMAP.md` has the shorter, forward-looking view of what's next.

## License

MIT — see `LICENSE`.
