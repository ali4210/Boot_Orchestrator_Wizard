# Progress — Boot Orchestrator Wizard Audit Remediation

Tracks the 41-item plan from the Phase 1–6 instructions. Branch: `fix/audit-phase-0-1`
(Phase 3 work will move to `test/os-matrix` as instructed). Read this file first in any
new session before resuming work — it reflects the true state of the tree, not just intent.

Status legend: Done (commit hash + proof) / In progress / Blocked (reason) / Not started.

## Phase 1: Hardening and testability

### A. Credentials
- [x] 1. `json:"-"` on Password/PasswordHash + serialization audit — Done (`f09a2a9`).
      Root cause: `Credentials` had no json tags, so `json.Marshal` used default field
      names and serialized both the plaintext password and the hash verbatim. Reproduced
      in `engine/serialization_test.go` (failed pre-fix, now passes). Fixed by adding
      `json:"-"` to `Password`/`PasswordHash`. Serialization audit (grep for
      `json.Marshal`/`MarshalIndent`/`NewEncoder`): `safety/journal.go` (Journal/Step —
      `SessionState` has no Credentials field, never at risk), `engine/uefi.go` (rollback
      blobs, no Credentials), `engine/orchestrator.go:922,1395` (backup/revert params, no
      Credentials), `engine/isolate.go` (rollback/config, no Credentials),
      `engine/rootfs.go:255` (rollback, no Credentials), `engine/peer_relay.go:79` (ping
      response, unrelated). `ProvisionRequest` itself (`engine/orchestrator.go:32`) is
      never serialized anywhere today — only passed in-process — but now carries the same
      protection if that ever changes. No logging call anywhere prints Password/PasswordHash
      (grepped); only `Username` appears in two `logFn` calls in `engine/bootloader.go`
      (non-secret).
- [x] 2. transient_resize.sh mode 0700, cleanup on all paths — Done (commit TBD).
      Root cause (engine/orchestrator.go, autonomous-installer block): the script was
      written with `os.OpenFile(..., 0755)` and then re-chmod'd to `0755` explicitly —
      world-readable — and it embeds the plaintext-derived password HASH directly in its
      own text via a heredoc. A failure inside the script before the chroot block (which
      is the only place that removed `/tmp/.orch_pwhash`, and only on the success path)
      left that hash file on the target disk. Refactored the script template out into
      `engine/autonomous_installer_script.go` (`renderAutonomousInstallerScript`,
      `writeScriptSecurely`) so both are unit-testable. Fixed: write mode is now
      `transientScriptMode = 0700`, with an explicit `os.Chmod` after write (open(2)'s
      mode argument does not retroactively narrow an already-existing file's
      permissions — proved by `TestWriteScriptSecurely_DoesNotNarrowModeOnExistingFile`).
      Added a `trap cleanup EXIT` near the top of the script (before any command that can
      fail under `set -e`) that removes both `/mnt/target/tmp/.orch_pwhash` and the
      script's own on-disk path on every exit path, not just success. Proved by
      `engine/autonomous_installer_script_test.go`.
- [x] 3. Detect admin group (sudo/wheel) in chroot, verify useradd — Done (commit TBD).
      Root cause (engine/bootloader.go `assertDefaultCredentials`): hardcoded `-G sudo` /
      `usermod -aG sudo` unconditionally. On Fedora/RHEL/Arch/openSUSE (no "sudo" group),
      `usermod -aG sudo` fails and was swallowed by `2>/dev/null || true`, silently
      producing an account with **no admin group at all** — combined with root locked by
      default, a full lockout on first boot. The function also always returned `nil` and
      logged "Verified: ... full sudo privileges" even when nothing was actually verified.
      Fixed: extracted `renderAccountProvisioningScript` (pure, testable) which runs
      `getent group sudo` then `getent group wheel` inside the chroot and uses whichever
      exists (warns if neither does); after useradd/usermod it re-checks `id -u <user>` and
      prints a sentinel (`ORCH_ACCOUNT_VERIFIED`/`ORCH_ACCOUNT_MISSING`, exit 7) rather than
      assuming success. `assertDefaultCredentials` now returns a real error if that sentinel
      is absent. Proved end-to-end by actually executing the generated script via `sh -c`
      against fake `getent`/`useradd`/`usermod`/`chpasswd`/`id` stubs on a temp `PATH`
      (`engine/account_provisioning_script_test.go` — sudo case, wheel case, no-group case,
      useradd-succeeds-but-no-account case, useradd-hard-failure case) plus a Go-level mock
      test (`TestAssertDefaultCredentials_FailsLoudlyWhenAccountNotVerified`).
      **Known gap, intentionally not touched here:** `assertDefaultCredentials`'s only
      caller (`AutoConfigureDualBoot`) still only logs this new error as a `[WARNING]` and
      continues the rest of provisioning. Making every critical step hard-fail the whole
      pipeline is the subject of Phase 4 item 25 (silent-error audit across the whole
      codebase), done deliberately as one pass rather than piecemeal here.
- [ ] 4. Min password length 8, reserved username rejection — Not started

### B. Safety gates
- [ ] 5. LVM/LUKS/mdraid/BitLocker pre-flight abort; Secure Boot/TPM detection — Not started
- [ ] 6. Verify `parted rm 2` only removes confirmed swap slice — Not started

### C. Headless mode and loop targets
- [ ] 7. --headless/--os-id/--iso-path/--target-disk/--username/--password-file/--yes — Not started
- [ ] 8. Loop device / image file support audit — Not started

### D. Launchers
- [ ] 9. autorun.sh: confirm before root installs, safe rm -rf, no go get/mod tidy — Not started
- [ ] 10. autorun.ps1: Go bootstrap version fix / GOTOOLCHAIN — Not started

**CHECKPOINT 1:** not reached

## Phase 2: Reported functional bugs
- [ ] 11. Download stall/incomplete reproduction (httptest cases) — Not started
- [ ] 12. Fix downloader (timeouts, resume, retry, checksum, atomic) — Not started
- [ ] 13. Manifest checksum/URL audit — Not started
- [ ] 14. .github/workflows/download-verify.yml — Not started
- [ ] 15. Revert reproduction (loop-image fixture) — Not started
- [ ] 16. Fix revert (partition/fstab/grub/ESP restore) — Not started
- [ ] 17. transient_resize.sh not created — reproduction + fix — Not started
- [ ] 18. Deploy payload ISO picker — reproduction — Not started
- [ ] 19. Fix ISO/image picker — Not started

**CHECKPOINT 2:** not reached

## Phase 3: OS verification matrix (branch test/os-matrix)
- [ ] Tier 1 catalog validation — Not started
- [ ] Tier 2 Docker container pipeline — Not started
- [ ] Tier 3 QEMU boot matrix — Not started
- [ ] Tier 4 Windows/macOS runners — Not started
- [ ] OS_STATUS_MATRIX.md — Not started

**CHECKPOINT 3:** not reached

## Phase 4: Remaining audit items + open-source readiness
- [ ] 20. GPG/signature verification — Not started
- [ ] 21. Image-deploy rollback handler or documented limitation — Not started
- [ ] 22. Windows XP entry removed/flagged — Not started
- [ ] 23. env-check opt-in + pre-flight gate — Not started
- [ ] 24. Windows launcher dedup + orphaned scripts moved — Not started
- [ ] 25. Silent-error audit (`|| true`, `2>/dev/null`, `_ = exec.Command`) — Not started
- [ ] 26. Generated-script check (bash -n, shellcheck, no fmt verbs/saleem) — Not started
- [ ] 27. Windows Panther plaintext password verification — Not started
- [ ] 28. Wire signature check into downloader — Not started
- [ ] 29. README/CONTRIBUTING/CI; LICENSE — ask user first — Not started
- [ ] 30. docs/ROADMAP.md — Not started

**CHECKPOINT 4:** not reached

## Phase 5: Real dry-run (audit 2.4, full)
- [ ] 31. docs/dryrun-inventory.md — Not started
- [ ] 32. FS interface + migration batches — Not started
- [ ] 33. Guard test + allowlist — Not started
- [ ] 34. Real --dry-run implementation — Not started

**CHECKPOINT 5:** not reached

## Phase 6: Architecture support (audit 2.6)
- [ ] 35. Arch table (amd64/arm64) — Not started
- [ ] 36. arm64 support for Debian/Ubuntu family only — Not started
- [ ] 37. qemu-system-aarch64 boot tests — Not started

**CHECKPOINT 6:** not reached

## Final deliverables
- [ ] 38. MANUAL_TEST_PLAN.md — Not started
- [ ] 39. AUDIT_REPORT.md progress section update — Not started
- [ ] 40. READINESS_GATE.md — Not started
- [ ] 41. Final summary — Not started

## Prior session (commits already on fix/audit-phase-0-1 before this plan started)
- `f7ddc19` fix: audit 2.1 — Linux bootloader failure no longer reported as success
- `82b00ed` fix: audit 2.2 — resize2fs preflight checks (mtab, mount, e2fsck, min size)
- `422c33a` fix: corrections to audit 2.1/2.2 work — exit codes, ordering, stdout safety
- `9c70753` fix: audit 2.3 — remove every hardcoded "saleem" account/password
- `63fdf46` feat: --dry-run fails closed instead of simulating partially (audit 2.4)

These satisfy some groundwork for Phase 1 item A and Phase 5, but the new plan's items are
broader (JSON serialization, script permissions/cleanup, admin-group detection, min password
length, reserved usernames) and are NOT yet done — tracked fresh above.
