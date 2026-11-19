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
- **Phase 3 (OS verification matrix)** — complete, and merged into this branch
  (`679dc6d`). All four tiers (catalog reachability, Docker ISO-structure check, QEMU/OVMF
  boot, Windows/macOS native build+test) have a real, working implementation, each proven
  against at least one real case — see `OS_STATUS_MATRIX.md` for the actual results and
  the real bugs it found (two dead manifest URLs, one catalog entry incompatible with the
  current install pipeline).
- **Phase 4 (remaining audit items 2.7/2.12-2.17 + open-source readiness, items 20-30)** —
  complete: OpenPGP signature verification (built and wired into the real download flow),
  the image-deploy rollback limitation documented, the unsupported Windows XP entry
  removed, `cmd/env-check`'s checks wired into the real pre-flight gate, Windows launcher
  dedup + maintainer-script cleanup, a silent-error audit (with a real disaster-recovery
  bug fixed), generated-script checks (which found and fixed a real account-creation bug
  bypassing the hardened credential path), the Panther plaintext-password cleanup, and
  README/CONTRIBUTING/CI/LICENSE.

- **Phase 5 — a real `--dry-run`** — complete: every `exec.Command`/filesystem-write call
  site reachable from a real entry point (provision, revert, backup, autonomous USB
  staging) across `engine/`, `ui/`, and `cmd/orchestrator/` is now routed through
  `engine.Runner`, `engine.FS`, or the `execCommand` var, all switched together by
  `engine.SetDryRun`. `--dry-run` runs the real flow with that switch on instead of
  refusing to start. See `docs/dryrun-inventory.md`'s "Update (item 45)" section for the
  full account, including what's deliberately still NOT intercepted (the payload download
  itself, and `safety.Journal`'s own bookkeeping writes — both explained there, not hidden).
- **Phase 6 — ARM64** — complete for the scope this catalog actually has: `usb_manager.go`'s
  autonomous-USB-staging bootloader generator (the one confirmed-broken case) is fixed and
  tested; two real arm64 catalog entries were added (`debian-netboot-arm64`,
  `ubuntu-server-arm64`); a real QEMU/AAVMF boot was proven reaching Debian's actual GRUB
  installer menu (`testing/qemu/boot-and-verify-aarch64.sh`,
  `testing/reports/tier3-debian-netboot-arm64-*`). Never run on real arm64 hardware — see
  `MANUAL_TEST_PLAN.md` section 8.
- **Final deliverables**: `MANUAL_TEST_PLAN.md`, `AUDIT_REPORT.md`'s "Remediation
  Progress" section, and `READINESS_GATE.md` all exist and are kept current — see
  `READINESS_GATE.md` for this project's own honest verdict on itself, re-checked each
  time the underlying state changes (most recently when 2.4 and 2.6 were both closed —
  see `PROGRESS.md` items 42-45).

## Known structural gaps worth a deliberate decision (not silently sitting there)

- **`engine/rootfs.go`** (~780 lines: `PlanDeploy`/`ApplyDeploy`/`deploySquashfsFromISO`/
  `deployWindowsFromISO`/etc.) has zero callers anywhere in the repo — an entire alternate
  deploy pipeline, apparently superseded by `engine/orchestrator.go`'s
  `extractLiveRootFS`/`deployWindowsPayload`, never removed. Flagged in `PROGRESS.md` item
  26; not deleted yet.
- **`hypervisor/compact.go`** (`ZeroFillGuestLinux`/`ZeroFillGuestWindows`/
  `PlanHostCompact`, ~150 lines) has zero callers anywhere in the repo either — found
  during item 45's full-repo sweep for exec.Command/filesystem-write call sites, not
  previously documented as dead. Same treatment as `rootfs.go`: flagged, not deleted, not
  migrated onto the dry-run interception (there is nothing to migrate it *for*, since
  nothing reaches it).
- **`safety.Journal`'s own bookkeeping writes are not covered by `--dry-run`** — `safety`
  cannot import `engine` (the reverse import already exists), so this would need its own,
  independent dry-run switch kept in sync with `engine.SetDryRun`, which item 45
  deliberately didn't build: a journal file at `safety.DefaultJournalPath()` is operational
  metadata, not a disk/bootloader/NVRAM write, and is a materially different, lower-risk
  category than everything else 2.4 was about.
- **The payload download itself is not covered by `--dry-run`** — deliberately: it never
  touches the target disk, and skipping it would make the rest of a dry run (checksum
  verification, free-space headroom) unrealistic. See `docs/dryrun-inventory.md`.
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
