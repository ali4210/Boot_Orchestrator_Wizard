# Readiness Gate

## Verdict: 🔴 NOT GREEN

The gate is green only if there are no known open Critical or High issues and every claim
made about this tool is backed by a real test or a real, reproducible run. As of this pass,
**two findings from the original audit remain open at Critical/High severity**, and they are
listed below rather than papered over. This is not a failure of the remediation effort — it
is the effort correctly refusing to claim more than what was actually verified, the same
discipline every item in `PROGRESS.md` was held to individually.

## What's open (the reason this isn't green)

### Critical: 2.4 — No real dry-run/simulation mode

**Status: genuinely open, not fixed.** Phase 5 (`PROGRESS.md` items 31-34) built real,
tested interception infrastructure (`engine.Runner`, `engine.FS`, `engine.SetDryRun`) and
proved it against one real migration batch end-to-end. That is real progress — but
`RunProvision`'s actual call sites interleave covered and uncovered operations **inside the
same functions** (partition, mount, bootloader, USB, backup/revert, WIM/BCD). Flipping
`SetDryRun(true)` and running the real flow today would intercept only part of what each
function does and let the rest execute for real against the actual disk — which is worse
than refusing, because it would look like protection while still writing to real hardware
partway through. `--dry-run` correctly still refuses to start. Closing this requires
migrating enough of the remaining ~400 call sites (`docs/dryrun-inventory.md` has the real
count and the recommended order) that the real flow is accurate under simulation from start
to finish — not a quick fix.

### High: 2.6 — x86_64 hardcoded despite ARM64 support

**Status: partially fixed.** The one confirmed-broken case this pass found —
`engine/usb_manager.go`'s autonomous USB-staging bootloader generator, which would have
produced a completely unbootable stick on real arm64 host hardware — is fixed and tested
(`PROGRESS.md` items 35-37). `engine/bcd_qemu.go` (the Windows-BCD-via-QEMU helper, which
hardcodes `qemu-system-x86_64` and `bootx64.efi`) and the other x86_64/amd64 string literals
catalogued in `docs/dryrun-inventory.md`'s category breakdown were **explicitly not
audited** this pass (judged lower priority: Windows-on-ARM is a vanishingly rare target for
this tool, and item 36's scope was "Debian/Ubuntu family only"). A full close requires
auditing every remaining hardcoded-architecture site, not just the one found.

## Everything else: no other open Critical/High issues

Every other finding from the original audit (`AUDIT_REPORT.md` section 2, items 2.1-2.3,
2.5, 2.8-2.19) is fixed with a real commit and test — see `AUDIT_REPORT.md`'s own
"Remediation Progress" section (item 39) for the full table, and `PROGRESS.md` for each
one's actual evidence. Two findings have a real but incomplete fix, tracked here for
transparency rather than as blocking the gate (neither is Critical/High on its own):

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
- **`engine/rootfs.go`** (~780 lines) has zero callers anywhere in the repo — confirmed dead
  code, not deleted (a deliberate decision deferred to `docs/ROADMAP.md`, not an oversight).
- **Windows and macOS have never been driven end-to-end against real or VM hardware** from
  this project's own (Linux-only) development sandbox. Both cross-compile cleanly and have
  CI running `go build`/`go vet`/`go test` natively on `windows-latest`/`macos-latest`
  (item 29), but no actual install has been observed completing on either platform. ARM64
  similarly: cross-compiles clean, and (new this pass) a real QEMU/AAVMF boot was proven for
  one catalog entry — but never on real arm64 hardware, and the autonomous-USB-staging fix
  (item 36) has only a unit-level proof, not a real-hardware one (see `MANUAL_TEST_PLAN.md`
  section 8).
- **`test/os-matrix` (Phase 3) is not merged** into this branch — `OS_STATUS_MATRIX.md`,
  `cmd/catalog-verify`, and the Docker/QEMU-x86_64 tooling it describes live there, not here.

## What every closed item IS backed by

Every item marked "Done" in `PROGRESS.md` has, at minimum, a real commit hash and a test
that was verified failing before the fix and passing after (the project's own
reproduce-first discipline, followed without exception across every single item — checked
by re-reading `PROGRESS.md` while writing this gate, not assumed). Several items go further:
real loop-device disk-image tests (not mocked) for partition/filesystem operations, real
`httptest`-server tests for network code, real OpenPGP cryptographic operations (not
stubbed) for signature verification, a real cross-platform build matrix that found two
previously-undetected Windows/macOS compile breakages, and a real QEMU boot (both x86_64
and now aarch64) reaching an actual bootloader/installer screen, not a prediction of one.

## What would make this green

1. Close 2.4: migrate enough of the remaining exec.Command/filesystem-write call sites
   (`docs/dryrun-inventory.md`) that `RunProvision` is accurate under `SetDryRun(true)`
   from start to finish, then implement the real flag.
2. Close 2.6: audit and fix every remaining hardcoded-architecture site, not just
   `usb_manager.go`'s.
3. Re-run this gate's own checklist after both, and update this document's verdict — don't
   leave it stale once the underlying state changes.
