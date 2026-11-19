# Manual Test Plan

What automated tests in this repo cannot cover — real destructive operations against real
or disposable-VM hardware. Everything in `engine/*_test.go` already covers the *logic*
(mocked commands, real loop-device images) for exactly this reason; this plan is for a human
to run the real binary against something safe to lose, before trusting any of it against a
machine that matters.

## Before you start

1. **Use a disposable target.** A VM with a snapshot you can revert, or a physical machine/
   disk you are prepared to fully reinstall. Never a machine with data you care about.
2. **Snapshot or back up before every section below**, not just once at the start — several
   sections are destructive to the same disk in sequence.
3. Build fresh: `go build ./...` (or the appropriate `GOOS=windows`/`GOOS=darwin`/
   `GOOS=linux GOARCH=arm64` cross-compile for the platform under test), and confirm
   `go vet ./... && go test ./...` are clean first.
4. Have a way to watch the target disk's real state independently of this tool (a second
   terminal with `lsblk`/`parted print`, or the hypervisor's own disk inspector) — the point
   of this plan is to catch the tool lying to itself, so don't only trust its own output.

## 1. Pre-flight gate (safe — read-only)

- `go run ./cmd/env-check` — confirm it reports real hypervisor/firmware/disk-space/catalog
  info for the machine it's run on, exits 0, and the live-scrape section is skipped by
  default. `go run ./cmd/env-check --live-scrape` — confirm it then makes the
  `archive.ubuntu.com` call and reports current suites (needs real network).
- `go run ./cmd/orchestrator --dry-run` — confirm it prints the `[DRY-RUN]` banner
  (`cmd/orchestrator/main.go`'s `dryRunBanner`) and then runs the real flow (TUI or
  headless) with every reachable exec.Command/filesystem-write call logged and
  intercepted instead of executed — see `docs/dryrun-inventory.md` for exactly what is and
  isn't covered (the payload download and `safety.Journal`'s own bookkeeping writes are
  deliberately NOT intercepted; everything that could touch the target disk/bootloader/
  NVRAM is).
- `go run ./cmd/orchestrator --dry-run --headless --yes --allow-unverified --os-id <id>
  --target-disk /dev/sdX --username testuser --password-file <path>` against a real (but
  disposable) disk — confirm every `[DRY-RUN]` log line names a real operation that WOULD
  have happened, and confirm independently (via a second terminal) that the disk's real
  partition table, bootloader, and NVRAM boot entries are completely unchanged afterward.
- `go run ./cmd/orchestrator --target-disk /dev/sdX` (an LVM/LUKS/mdraid/BitLocker disk, if
  you have one disposable) — confirm the pre-flight + `PreflightStorageSafetyCheck` refuse
  before anything is written. If you don't have one handy, the automated
  `engine/preflight_safety_test.go` cases cover this via mocks — this manual step is only
  for an extra real-world sanity check if convenient.

## 2. Catalog reachability (safe — no real downloads)

- `go run ./cmd/catalog-verify -out /tmp/catalog-report.json` — confirm it completes and
  the report's entries are plausible. Known-expected failures as of this writing (see
  `OS_STATUS_MATRIX.md`): `gentoo-stage3-amd64` (dead URLs) and both macOS entries
  (expired signed URLs) — if these are now fixed, the report should show it; if new
  failures appear elsewhere, that's a real regression worth investigating before
  continuing.

## 3. TUI walkthrough — unverified-download gate

- Launch the TUI (no flags), pick Standard mode, select any catalog entry.
- Since no entry currently has a checksum, confirm `ScreenUnverifiedDownloadConfirm`
  appears every time, and:
  - Pressing `n`/`esc` returns to the OS list with the selection cleared.
  - Pressing `y`/`enter` proceeds to disk/USB selection.
- Repeat via `--iso <path-to-any-real-file>` — confirm it skips catalog selection entirely
  and lands on `ScreenCustomBootModeSelect` directly (no confirmation screen for this path
  — see `PROGRESS.md` item 20/F13's documented scope note on why).

## 4. Headless mode

Against a disposable target disk:

```bash
./bin/orchestrator --headless --yes --allow-unverified \
  --os-id <a-small-catalog-entry-id> \
  --target-disk /dev/sdX \
  --username testuser --password-file /path/to/a/password/file
```

- Confirm it refuses without `--yes`.
- Confirm it refuses without `--allow-unverified` (no entry has a checksum yet) and that
  the error names the entry.
- Confirm it refuses `--target-disk` pointed at a regular file instead of a real block
  device (`isBlockDevice`).
- Confirm it refuses `--target-disk` pointed at the live root disk when not in dual-boot
  mode (`isLikelyLiveRootDisk`).
- With everything correct, confirm the journal (`safety.DefaultJournalPath()`) gets created,
  progresses through steps, and is cleaned up/finalized on success.

## 5. A real dual-boot install, start to finish

Pick ONE small, known-working Linux entry (the smallest reasonable one; avoid anything
flagged incompatible in `OS_STATUS_MATRIX.md` — e.g. **not** `alpine-linux-amd64`, which is
documented there as incompatible with the current install pipeline). Run it through the
real TUI flow on a disposable VM with an existing host OS already installed (genuine
dual-boot, not a blank disk):

1. Confirm the host partition shrinks to the requested allocation and the new partition is
   created with the expected filesystem.
2. Confirm the new OS's rootfs is actually extracted (watch for the `[ROOTFS DEPLOY]`
   step), the account is created with the username/password you entered, and `sudo`/`wheel`
   group membership is correct (log in as that user after boot and run `sudo -l`).
3. Confirm GRUB now offers both the host OS and the new OS as boot entries, and that both
   actually boot.
4. Confirm `/etc/fstab` on the host was NOT corrupted by the operation.

## 6. Revert

Immediately after section 5, on the same VM/snapshot:

```bash
./bin/orchestrator --revert --yes
```

- Confirm it finds exactly the one guest OS installed in section 5 (not zero, not
  ambiguous-multiple).
- Confirm the added partition is gone, the host partition is grown back to its original
  size, the host's own files are intact, and the host boots normally afterward with no
  leftover GRUB entry for the removed OS.
- Repeat with `--keep-slice` on a fresh copy of the same snapshot — confirm the partition is
  reformatted as an empty staging slice instead of removed, and the host partition is NOT
  grown.

## 7. Disaster backup & restore

On a disposable VM with something installed:

1. Trigger a backup via the TUI's backup flow (or whatever entry point currently exposes
   `engine/backup.go`'s snapshot path). Confirm a real archive + checksum manifest is
   produced.
2. Wipe the disk (or use a fresh disposable disk) and restore from that archive via
   `RestoreHostFromBackup`. Confirm: the restored system boots, UEFI vs. BIOS partitioning
   was chosen correctly for the target, and — the specific regression item 25 fixed — that
   a deliberately-broken target (e.g. an already-mounted/busy disk) makes the restore abort
   loudly with a clear partition-table error rather than silently producing a broken ESP.

## 8. Autonomous bare-metal USB staging

Only attempt this on real bare-metal disposable hardware, or a VM configured to boot from
a virtual USB stick — this is the path that stages a payload + generated installer script
onto a USB drive, arms a one-shot UEFI boot entry, and reboots.

1. Confirm the USB drive ends up with both `ORCH_STORAGE` and `ORCH_BOOT` partitions,
   correctly formatted, with the payload and `transient_resize.sh` actually present on
   `ORCH_STORAGE` (not silently written to local disk — the specific regression bug G
   fixed).
2. Confirm the machine actually reboots into the USB environment and
   `transient_resize.sh` runs to completion, producing a working dual-boot (or single-boot)
   result on next boot.
3. On **arm64 hardware specifically** (new as of Phase 6): confirm the staged
   `EFI/BOOT/BOOTAA64.EFI` (not `BOOTX64.EFI`) is what actually gets written and that the
   arm64 UEFI firmware can boot it — `engine/usb_manager_test.go`'s
   `TestEfiBootNamesForArch...` only proves the Go-level name selection; this step is the
   real hardware proof that's still missing (see `docs/ARCH_SUPPORT.md`).

## 9. Windows and macOS targets

Neither has been driven end-to-end against real or VM hardware from this project's own
development sandbox (Linux-only) — see `docs/ARCH_SUPPORT.md`/`OS_STATUS_MATRIX.md`'s Tier
4 notes. If you have access to real Windows/macOS hardware or VMs:

- **Windows**: run a dual-boot install against a Windows catalog entry. Confirm WIM apply
  succeeds, the autounattend-injected account is created, and — item 27's fix — that
  `C:\Windows\Panther\unattend.xml` (and the other two locations) are actually gone after
  first boot, not just scheduled to be removed.
- **macOS**: confirm the recovery payload deploys and the resulting partition is usable as
  documented (recovery/virtualization image, not a full bare-metal macOS install — see
  README.md's macOS caveat).

## Recording results

After running any section, note pass/fail and any deviation from the expected result
somewhere durable (an issue, a comment in `PROGRESS.md`, or your own notes) — this plan is
meant to be re-run periodically, not just once, and silent regressions are exactly what it
exists to catch.
