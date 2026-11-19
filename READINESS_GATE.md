# Readiness Gate

## Verdict: 🟢 GREEN

The gate is green only if there are no known open Critical or High issues and every claim
made about this tool is backed by a real test or a real, reproducible run. As of this pass,
**both findings that previously kept this gate NOT GREEN (2.4 and 2.6) are closed**, with
real commits and real tests, and no other Critical/High finding from the original audit is
open. This verdict is re-checked against the actual current state each time something
material changes — not left stale — and the known, non-blocking gaps below are stated
plainly rather than omitted now that the headline verdict is green.

## What closed since the last NOT GREEN verdict

### Critical: 2.4 — No real dry-run/simulation mode — now Fixed

Phase 5 (`PROGRESS.md` items 31-34) built real, tested interception infrastructure
(`engine.Runner`, `engine.FS`, `engine.SetDryRun`) and proved it against one real batch,
but deliberately stopped there: `RunProvision`'s call sites interleaved covered and
uncovered operations inside the same functions, so flipping the switch on at that point
would have intercepted only part of what happens. Items 45-49 went back and finished it:
every remaining raw `exec.Command`/filesystem-write call site reachable from a real entry
point (provision, revert, backup, autonomous USB staging) across `engine/`, `ui/`, and
`cmd/orchestrator/` — ~294 `exec.Command` sites, ~160 filesystem-write sites, 20+ files —
is now routed through `engine.Runner`, `engine.FS`, or the new `execCommand` var, all
switched together by `engine.SetDryRun`. `--dry-run` now enables that switch and runs the
real flow, instead of refusing to start.

Two things are deliberately still NOT intercepted — stated here, not buried:
- **The payload download itself** (`engine/streamer.go`'s real `net/http` GET plus
  `os.Create`/`io.Copy`) still happens for real. It never touches the target disk/
  bootloader/NVRAM — the actual safety concern 2.4 was about — and skipping it would make
  the rest of a dry run (checksum verification, free-space headroom) unrealistic. A dry
  run still uses real bandwidth and real local scratch disk space for this one step.
- **`safety.Journal`'s own bookkeeping writes** are not intercepted. `safety` cannot
  import `engine` (the reverse import already exists, so this would be a cycle); covering
  it would need an independent dry-run switch kept in sync with `engine.SetDryRun`, which
  wasn't built. A journal file at `safety.DefaultJournalPath()` is operational metadata,
  not a disk/bootloader/NVRAM write — a materially lower-risk category than everything
  else this finding was about.

See `docs/dryrun-inventory.md`'s "Update (item 45)" section for the complete account,
including the real end-to-end proof
(`TestRestoreHostFromBackup_DryRunNeverTouchesRealDiskAndSucceeds`): the exact bogus-disk
fixture that makes real mode fail fast on its very first `parted` call instead returns nil
under dry-run, because every mutating call in that function's entire chain is genuinely
intercepted — not a claim resting on the switch alone.

### High: 2.6 — x86_64 hardcoded despite ARM64 support — now Fixed

The one confirmed-broken case from the earlier pass (`engine/usb_manager.go`'s autonomous
USB-staging bootloader generator, which would have produced a completely unbootable stick
on real arm64 host hardware) was fixed and tested first (`PROGRESS.md` items 35-37). Items
42-44 then ran a full-repo grep for `x86_64`/`amd64`/`BOOTX64` and found three more live
instances of the same bug class, all now fixed and tested:

- `engine/orchestrator.go`'s autonomous-USB NVRAM one-shot boot entry (hardcoded
  `BOOTX64.EFI` independently of `usb_manager.go`'s own fix).
- `engine/backup.go`'s disaster-recovery restore (`grub-install --target=x86_64-efi`).
- `engine/bootloader.go`'s no-kernel self-heal (`apt-get install linux-image-amd64`).

`engine/bcd_qemu.go` (the Windows-BCD-via-QEMU helper, hardcoding `qemu-system-x86_64` and
`bootx64.efi`) is the one case left deliberately unfixed rather than unaudited: no arm64
Windows catalog entry exists or can be tested against (Windows-on-ARM was never in
scope — item 36's arm64 support is "Debian/Ubuntu family only"), so implementing untestable
arm64 BCD logic would be guesswork. Instead, `writeWindowsBCD` now calls
`validateBCDHostArch(runtime.GOARCH)` and refuses loudly with a clear explanation on any
non-amd64 host, rather than silently attempting x86_64-only logic that would fail
confusingly deep inside a QEMU invocation. This removes the "silently" from the original
finding without pretending to support a target that doesn't exist. See
`docs/ARCH_SUPPORT.md`.

Also audited and **intentionally** left alone, documented rather than silently skipped:
`orchestrator.go`'s/`rootfs.go`'s `"arch", "x86_64"` ISO path components (Arch Linux's own
on-disk directory layout, not host-arch logic — Arch Linux ARM isn't in the catalog), and
the `User-Agent` header strings in `sentinel.go`/`streamer.go` (cosmetic HTTP header text,
no functional effect).

## Everything else: no other open Critical/High issues

Every other finding from the original audit (`AUDIT_REPORT.md` section 2, items 2.1-2.3,
2.5, 2.8-2.19) is fixed with a real commit and test — see `AUDIT_REPORT.md`'s own
"Remediation Progress" section for the full table, and `PROGRESS.md` for each one's actual
evidence. Two findings have a real but incomplete fix, tracked here for transparency
rather than as blocking the gate (neither is Critical/High on its own):

- **2.7 (High in the original audit, now downgraded in practice)**: OpenPGP signature
  verification is real, tested, and wired into the download flow — but **no catalog entry
  has a `SignatureURL`/`SigningKeyURL` yet** (same situation as SHA256 checksums — see
  `docs/MANIFEST_AUDIT.md`), so the mechanism currently protects nothing in practice.
  Backfilling real values is a data-curation task, not a code fix, and inventing them would
  be worse than leaving the gap honest.
- **2.3-adjacent (item 26's finding)**: the hardcoded-account-creation bug class was fixed
  in a second code path this pass found — but the discovery itself ("grep for the shape of
  a bug, not just the one call site a report named") is a reason to distrust a simple
  "2.3: Fixed" checkbox slightly more than usual. No other instance is currently known, but
  none was exhaustively searched for either.

## Known data/coverage gaps (non-blocking, but real)

- **No catalog entry has a SHA256 checksum or OpenPGP signature.** The verification
  machinery for both is real and tested; the data needs independent human sourcing per
  entry.
- **Two dead manifest URLs** (`gentoo-stage3-amd64`, both macOS recovery entries) found by
  `OS_STATUS_MATRIX.md`'s real Tier 1 run — a data-curation fix, not attempted this pass.
- **`alpine-linux-amd64` is incompatible with the current autonomous install pipeline**
  (no squashfs, no `/live` directory) — found by `OS_STATUS_MATRIX.md`'s real Tier 2 run,
  not fixed this pass.
- **`engine/rootfs.go`** (~780 lines) and **`hypervisor/compact.go`** (~150 lines) have
  zero callers anywhere in the repo — both confirmed dead code, neither deleted (a
  deliberate decision deferred to `docs/ROADMAP.md`, not an oversight; `compact.go` was
  found newly dead during item 45's full-repo sweep, not previously documented).
- **The dry-run scope boundaries noted above** (payload download, `safety.Journal`
  bookkeeping) — deliberate, documented, not silently gapped.
- **Windows and macOS have never been driven end-to-end against real or VM hardware** from
  this project's own (Linux-only) development sandbox. Both cross-compile cleanly and have
  CI running `go build`/`go vet`/`go test` natively on `windows-latest`/`macos-latest`
  (item 29), but no actual install has been observed completing on either platform. ARM64
  similarly: cross-compiles clean, and a real QEMU/AAVMF boot was proven for one catalog
  entry — but never on real arm64 hardware, and the autonomous-USB-staging fix (item 36)
  has only a unit-level proof, not a real-hardware one (see `MANUAL_TEST_PLAN.md` section
  8).
- ~~`test/os-matrix` (Phase 3) is not merged into this branch~~ — merged (`679dc6d`).
  `OS_STATUS_MATRIX.md`, `cmd/catalog-verify`, and the Docker/QEMU-x86_64 tooling it
  describes now live on this branch too; `go build`/`vet`/`test` confirmed green
  afterward, not assumed.

## What every closed item IS backed by

Every item marked "Done" in `PROGRESS.md` has, at minimum, a real commit hash and a test
that was verified failing before the fix and passing after (the project's own
reproduce-first discipline, followed without exception across every single item — checked
by re-reading `PROGRESS.md` while writing this gate, not assumed). Several items go
further: real loop-device disk-image tests (not mocked) for partition/filesystem
operations, real `httptest`-server tests for network code, real OpenPGP cryptographic
operations (not stubbed) for signature verification, a real cross-platform build matrix
that found two previously-undetected Windows/macOS compile breakages, a real QEMU boot
(both x86_64 and aarch64) reaching an actual bootloader/installer screen, and — for 2.4's
closure specifically — a real end-to-end proof that a representative function's entire
call chain is genuinely intercepted under simulation, not just that the switch itself
flips.

## What would make this NOT green again

This verdict is accurate as of the commits in `PROGRESS.md` through item 49. It should be
re-checked (not assumed to still hold) if any of the following happen: a new Critical/High
finding is reported or discovered; a call site is added to a file this gate's closures
depend on without also being routed through `Runner`/`FS`/`execCommand` (`call_site_
ratchet_test.go`'s guard mechanically catches this for `engine`/`ui`/`cmd/orchestrator` —
extended to the latter in item 49 specifically because the revert.go gap it found would not
have been caught otherwise — but any future package is not automatically covered); or
`engine/bcd_qemu.go` gains a real arm64 Windows use case without `validateBCDHostArch`
being revisited.
