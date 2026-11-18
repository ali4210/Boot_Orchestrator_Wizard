# Roadmap

This is a map of where the project stands and what's next — not the detailed record. For
that, see `PROGRESS.md` (every item's real commit hash, what was tested, what was
deliberately left alone and why) and `AUDIT_REPORT.md` (the original findings this whole
effort works through).

## Done

- **Phase 1 (hardening/testability)** and **Phase 2 (the 4 originally-reported functional
  bugs, E-H)** — complete.
- **Pre-Phase-3 follow-ups ("Slice 1", F13-F18)** — complete: unverified-download
  confirmation gating, revert swap-recreation + injectable ESP/NVRAM, USB mount retry
  hardening, the ISO-picker wiring fixes, journal test isolation + step-name completeness
  checks, and the download-verify.yml/bug-repro documentation.
- **Phase 3 (OS verification matrix)** — complete on branch `test/os-matrix`, **not yet
  merged into this branch**. All four tiers (catalog reachability, Docker ISO-structure
  check, QEMU/OVMF boot, Windows/macOS native build+test) have a real, working
  implementation, each proven against at least one real case — see `OS_STATUS_MATRIX.md`
  for the actual results and the real bugs it found (two dead manifest URLs, one catalog
  entry incompatible with the current install pipeline).
- **Phase 4 (remaining audit items 2.7/2.12-2.17 + open-source readiness, items 20-30)** —
  complete: OpenPGP signature verification (built and wired into the real download flow),
  the image-deploy rollback limitation documented, the unsupported Windows XP entry
  removed, `cmd/env-check`'s checks wired into the real pre-flight gate, Windows launcher
  dedup + maintainer-script cleanup, a silent-error audit (with a real disaster-recovery
  bug fixed), generated-script checks (which found and fixed a real account-creation bug
  bypassing the hardened credential path), the Panther plaintext-password cleanup, and
  README/CONTRIBUTING/CI/LICENSE.

## Next

- **Phase 5 — a real `--dry-run`.** `--dry-run` currently refuses to start rather than
  simulate (see `cmd/orchestrator/main.go`'s `dryRunRefusalMessage`): a 2026 audit found
  roughly 350 direct `exec.Command` call sites and 140 direct filesystem-write call sites
  across `engine/`/`ui/` that bypass the one interception point that exists so far
  (`engine.Runner`). A real dry-run needs those routed through `engine.Runner` (or an
  equivalent) first — simulating only some of several hundred write paths would be actively
  misleading, not safer. Planned shape: `docs/dryrun-inventory.md` (catalog every
  call site), a filesystem interface alongside the existing command `Runner`, migrated in
  batches (not all at once), a guard test asserting no new unintercepted call site is added,
  then the real `--dry-run` implementation.
- **Phase 6 — ARM64.** Builds today (`GOOS=linux GOARCH=arm64 go build ./...`); never run.
  Needs real hardware or QEMU-emulated ARM64 testing (the `OS_STATUS_MATRIX.md` QEMU
  pipeline is x86_64/OVMF-specific right now — extending it to `qemu-system-aarch64` +
  AAVMF would be the natural way to get real boot-level coverage without new hardware).
- **Merge `test/os-matrix`** into the main line once Phase 5/6 land, or independently if
  that's preferred — it's currently a parallel branch, not yet integrated.
- **Final deliverables** (not started): `MANUAL_TEST_PLAN.md`, an `AUDIT_REPORT.md` progress
  section cross-referencing `PROGRESS.md`'s item-by-item record, and `READINESS_GATE.md`
  (green only if there are no known open Critical/High issues and every claim is
  test-backed — otherwise it lists exactly what's open).

## Known structural gaps worth a deliberate decision (not silently sitting there)

- **`engine/rootfs.go`** (~780 lines: `PlanDeploy`/`ApplyDeploy`/`deploySquashfsFromISO`/
  `deployWindowsFromISO`/etc.) has zero callers anywhere in the repo — an entire alternate
  deploy pipeline, apparently superseded by `engine/orchestrator.go`'s
  `extractLiveRootFS`/`deployWindowsPayload`, never removed. Flagged in `PROGRESS.md` item
  26; not deleted yet.
- **No catalog entry has a SHA256 checksum or OpenPGP signature.** The verification
  machinery for both is real and tested (`discovery.Catalog.Audit()`,
  `engine.VerifyDetachedSignature`/`verifyEntrySignature`); the data itself needs a human
  to source each value from each distro's actual official, signed checksum file — see
  `docs/MANIFEST_AUDIT.md`.
- **Two dead manifest URLs** (`gentoo-stage3-amd64`'s mirrors, both 404; both macOS
  entries' Apple recovery-image URL, 403/expired) — see `OS_STATUS_MATRIX.md`. Data-curation
  fixes, not code bugs.
- **`alpine-linux-amd64` is incompatible with the current autonomous dual-boot install
  path** (no squashfs, no `/live` directory in its standard ISO layout — see
  `OS_STATUS_MATRIX.md`). Needs either a special case in
  `engine/autonomous_installer_script.go`'s install logic, or marking the entry
  live/TTY-only in the manifest, until one of those happens.
