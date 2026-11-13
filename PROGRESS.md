# Progress — Boot Orchestrator Wizard Audit Remediation

Tracks the 41-item plan from the Phase 1–6 instructions. Branch: `fix/audit-phase-0-1`
(Phase 3 work will move to `test/os-matrix` as instructed). Read this file first in any
new session before resuming work — it reflects the true state of the tree, not just intent.

Status legend: Done (commit hash + proof) / In progress / Blocked (reason) / Not started.

## Pre-Phase-2 follow-ups (requested mid-Phase-2, before Phase 2 is marked done)
- [x] F1. Remove positional sdc/sdd/sde USB fallback in usb_manager.go and backup.go;
      classify removable only via lsblk TRAN=="usb" or RM==1, fail closed — Done
      (`6842c03`). `engine/usb_manager.go`'s `ProbeRemovableTargets` and `ProbeUSBPayloads` both
      had `|| strings.HasPrefix(dev.Name, "sdc") || ... "sdd" || ... "sde"` in their isUSB
      checks — removed both. `engine/backup.go`'s `DetectBackupTargets` was actually
      broader: `strings.HasPrefix(dev.Name, "sd") && dev.Name != pDiskBase` treated ANY
      non-root "sdX" disk as removable; added `TRAN` to its lsblk query and replaced the
      condition with `dev.RM || strings.ToLower(dev.Tran) == "usb"` (dropped `Hotplug` too,
      per the literal "TRAN or RM" instruction). Tests (`engine/usb_manager_test.go`,
      `engine/backup_test.go`): a fake `lsblk` is placed first on `PATH` (never touches the
      real system's disks) reporting an internal, non-removable disk coincidentally named
      `sdc`/`sdb` alongside a genuine USB disk; asserts the internal one is excluded and the
      genuine USB one is still included, for all three affected functions.
- [x] F2. Move PreflightStorageSafetyCheck before the image download; test blkid exit code 2
      (no filesystem) does not abort — Done (`ea8f443`). Moved the call in
      `engine/orchestrator.go` `RunProvision` from inside the `partition-shrink` step
      (after the download/checksum-verify block) to right after `req.TargetDiskPath` is
      resolved, before `journal.Begin("image-download", ...)` — so a disk this tool will
      refuse to touch is rejected before spending any time/bandwidth downloading a payload
      for it. Now runs unconditionally (even when resuming a paused session): cheap and
      read-only, worth re-checking. The Secure Boot/TPM advisory warnings were left where
      they were (not part of this request, and they don't gate anything). Test
      (`TestPreflightStorageSafetyCheck_BlkidExitCode2DoesNotAbort`,
      `engine/preflight_safety_test.go`): mocks blkid returning a genuine exit-code-2
      `*exec.ExitError` with empty stdout (real blkid's behavior on an unformatted
      partition — a normal, common case, not a sign of LVM/LUKS/mdraid/BitLocker) and
      confirms the check passes cleanly rather than aborting.
- [x] F3. Headless: refuse non-block-device targets and the live root disk in single-boot
      mode; resolve autounattend.xml via embed/executable location not cwd; fail loudly if a
      Windows target has no unattend — Done (`c67083e`). `cmd/orchestrator/headless.go`:
      added `isBlockDevice` (checks `/sys/class/block/<name>`, same source of truth as
      `engine/revert_linux.go`'s `partitionExists`; a swappable var for testing) — `
      --target-disk` is now rejected if it isn't a real block device. Added
      `isLikelyLiveRootDisk` (reads `/proc/mounts` directly, independent of
      `engine.DetectActiveRootDisk`'s own heuristics) as defense-in-depth: if mode resolves
      to `single-boot` but `/proc/mounts` shows the target disk actually holds the live `/`
      filesystem, refuse — this specifically protects against `DetectActiveRootDisk` silently
      failing to detect the live disk (returning `""`) while the real mount table still shows
      it. `autounattendPath` resolution switched from `filepath.Abs("discovery/manifests/...")`
      (cwd-relative) to `resolveAutounattendPath`/`findAutounattendNear`, which resolve
      relative to `os.Executable()`'s own location — checking both alongside the binary and
      its parent directory (matching this project's own `bin/` + `discovery/` layout from
      `autorun.sh`). A Windows target with no resolvable unattend file now returns a hard
      error instead of silently proceeding with `AutounattendPath: ""`. Tests
      (`cmd/orchestrator/headless_test.go`): non-block-device rejection, live-root-disk
      single-boot refusal (via a faked `/proc/mounts` with `engine.DetectActiveRootDisk`'s
      own detection simulated as having failed), `isLikelyLiveRootDisk` directly (positive/
      negative/empty), `findAutounattendNear` (alongside/parent/not-found), and the
      Windows-no-unattend failure. Existing `buildHeadlessRequest` tests updated to stub
      `isBlockDevice` where needed so they keep exercising what they originally tested.
- [x] F4. Fill in real commit hashes in PROGRESS.md; verify every commit builds/tests green
      via a disposable rebase (not rewriting the real branch) — Done. All "(commit TBD)"
      placeholders below and above replaced with real hashes. Verification run as
      `git branch verify-rebase-tmp fix/audit-phase-0-1 && git checkout verify-rebase-tmp &&
      git rebase --exec "go build ./... && go test ./..." main`, then the temp branch
      deleted — **not** `git rebase` on `fix/audit-phase-0-1` itself. The literal command
      requested rewrites history (every commit on the branch gets a new hash when replayed
      onto main, even with identical content), which conflicts with this task's own explicit
      rule stated at the start of this work: "Never rewrite git history." The disposable-
      branch form proves the same thing — every commit, checked out in order, builds and
      tests green — without moving `fix/audit-phase-0-1`'s own ref or changing any of its
      commit hashes. **Result:** every commit builds and tests green except one, by
      design: `d756fec` ("test: add regression tests for audit 2.1 and 2.2 (2.1 currently
      failing)") fails `TestConfigureBootloaderStep_LinuxBranchFailurePropagates` — the
      deliberate reproduce-the-bug-with-a-failing-test commit from the original Step 2
      instructions, fixed by the very next commit (`f7ddc19`). No other commit, before or
      after it, failed. The rebase replayed onto unchanged `main` and produced
      byte-identical commit hashes (confirming the branch was already cleanly based on
      `main`); the temporary branch was deleted afterward.
- [x] F5. autorun.sh: detect root-owned files in bin/ or the journal and print the exact
      chown command instead of a raw permission error — Done (`3ce022d`). Added
      `check_root_owned_files` right before the build section: scans `bin/` with
      `find ... -user root` and checks the journal's owner via `stat -c '%U'`; if either is
      root-owned (e.g. left over from a run before item 1D.9's fix, or from someone
      directly invoking `sudo ./autorun.sh`), prints the exact `sudo chown -R
      "$(id -un):$(id -gn)" ...` command and exits, instead of failing opaquely mid-build
      with a bare permission-denied error. Verified both branches (no offenders / offenders
      present) against an isolated scratch directory — never the real repo — including
      actually `sudo chown`-ing a scratch file to root to exercise the positive case for
      real, then restoring ownership and removing the scratch fixture explicitly file-by-file
      (no `rm -rf` on anything but a `mktemp -d` path). Incidentally confirmed this is not a
      hypothetical: the real repo's own (gitignored, untouched, un-read per the
      never-touch-the-journal safety rule) `orchestrator_journal.json` is itself root-owned
      from an earlier, pre-session `sudo ./autorun.sh` run — exactly the case this check now
      catches with a clear message instead of a confusing build failure. `bash -n` and
      `shellcheck` both clean.

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
- [x] 2. transient_resize.sh mode 0700, cleanup on all paths — Done (`9039134`).
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
- [x] 3. Detect admin group (sudo/wheel) in chroot, verify useradd — Done (`2dc71cb`).
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
- [x] 4. Min password length 8, reserved username rejection — Done (`2748af2`).
      `engine/credentials.go`: `ValidUsername` now also rejects a `reservedUsernames` set
      (root, daemon, bin, sys, nobody, sshd, systemd-*, administrator, guest, etc., matched
      case-insensitively) even if the string otherwise matches the regex. Added
      `MinPasswordLength = 8` and `ValidPassword`. `ui/model.go`'s credentials screen now
      calls `engine.ValidPassword` (was `len(password) < 4`) and reports the reserved-name
      rejection in its username error text. Tests: `TestValidUsername` extended with the
      reserved-name cases, new `TestValidPassword` with boundary cases (7/8/9 chars).

### B. Safety gates
- [x] 5. LVM/LUKS/mdraid/BitLocker pre-flight abort; Secure Boot/TPM detection — Done
      (`3f21fec`). New `engine/preflight_safety.go`: `PreflightStorageSafetyCheck(disk)`
      lists the disk + all its partitions via `lsblk` (Runner-mediated) and probes each
      with `blkid -o value -s TYPE` (also Runner-mediated); util-linux's blkid reports
      `LVM2_member`/`crypto_LUKS`/`linux_raid_member`/`BitLocker` directly, so one probe per
      device covers all four without needing lvm2/cryptsetup/mdadm installed. Aborts
      (returns an error, writes nothing) on any match. `DetectSecureBootAndTPM()` checks
      `mokutil --sb-state` (Runner-mediated) and `/sys/class/tpm/tpm0` (read-only os.Stat)
      and returns advisory warnings (does not abort — this tool doesn't sign its own
      bootloader chain, so Secure Boot/TPM are informational). Wired into
      `engine/orchestrator.go` `RunProvision`, as the very first thing inside the
      `!journal.Session.IsPaused` block — before `BackupPartitionTable` and every partition
      write that follows. Tests (`engine/preflight_safety_test.go`, all via mockRunner):
      abort cases for LVM/LUKS/mdraid/BitLocker, pass-case for plain ext4/vfat, empty-disk
      refusal, Secure-Boot-enabled warning, Secure-Boot-disabled no-warning.
- [x] 6. Verify `parted rm 2` only removes confirmed swap slice — Done (`c519078`).
      Root cause (`engine/partition.go` `CarveHardwarePartitionAtBoundary`, the trailing-swap
      branch): `strings.Contains(strings.ToLower(lastPart.fs), "swap") || lastPart.num == 3`
      — the `|| lastPart.num == 3` meant ANY disk whose last partition happened to be
      numbered 3 was treated as swap and removed, regardless of actual filesystem type; a
      disk with a real third data partition (not swap) would have had it deleted.
      Reproduced in `TestCarveHardwarePartitionAtBoundary_DoesNotDeleteNonSwapPartitionThree`
      (fails pre-fix, confirmed). Fixed with `isConfirmedSwapPartition(devPath,
      partedFSName)`: requires BOTH parted's own fs-type column AND an independent `blkid
      -o value -s TYPE` probe to agree the partition is swap before any removal; the
      existing size-sufficiency check (swap slice large enough for the target) is
      unchanged and still runs first. Tests: non-swap-partition-3 no-delete,
      confirmed-swap-converts-correctly, parted/blkid-mismatch no-delete, focused
      `isConfirmedSwapPartition` table test (4 cases).

### C. Headless mode and loop targets
- [x] 7. --headless/--os-id/--iso-path/--target-disk/--username/--password-file/--yes — Done
      (`d05bbe7`). New `cmd/orchestrator/headless.go`: `headlessConfig.validate()` enforces
      all required flags and refuses without `--yes` last (so every other problem is
      reported too, not just the missing `--yes`). Password is **only** ever accepted via
      `--password-file` (read by `readPasswordFile`, one trailing `\n`/`\r\n` stripped,
      internal whitespace untouched) — never as a CLI argument, so it's never visible via
      `ps`/`/proc/<pid>/cmdline`. `buildHeadlessRequest` resolves `--os-id` against
      `discovery.LoadEmbedded()` (mirrors/SHA256/distro from the matched entry) or
      `--iso-path` (a local file, routed through the same local-path ingest
      `engine/streamer.go` already supports for absolute paths — no catalog entry, so no
      Windows auto-detection, same limitation as the TUI's unwired custom-ISO path), enforces
      `engine.ValidUsername`/`engine.ValidPassword`, hashes the password
      (`Credentials.HashPassword`), and resolves dual-boot-vs-single-boot plus host/target
      partition carryover via `resolveHeadlessTarget` exactly like the TUI's
      `startProvisioning` does with `engine.DetectActiveRootDisk`. `runHeadless` then calls
      the identical `engine.RunProvision(ctx, req, "/etc/default/grub", onUpdate)` the TUI
      uses (`ui/provision.go`'s `runProvisionWithCancel`) — same engine path, same safety
      gates (including the new B.5 preflight), just with stdout instead of Bubble Tea.
      Tests (`cmd/orchestrator/headless_test.go`): full `validate()` matrix (each missing
      flag, mutual exclusion, missing `--yes`), password-file trailing-newline stripping
      (LF/CRLF/none/internal-whitespace-preserved), missing-password-file, dual-boot vs
      single-boot target resolution, reserved-username rejection, short-password rejection,
      unknown `--os-id` rejection, successful catalog-entry resolution (asserting the hash is
      set and the plaintext password is never carried in the built request), local
      `--iso-path` resolution, missing-`--iso-path`-file rejection.
      **Known gap:** no end-to-end test actually invokes `engine.RunProvision` from headless
      mode (that would require mocking the full partition/download/bootloader pipeline);
      covered instead by the existing per-function engine tests plus this request-building
      layer. OS-family auto-detection for `--iso-path` (so Windows ISOs get an autounattend
      without needing `--os-id`) is Phase 2 item 19's scope, not this one.
- [x] 8. Loop device / image file support audit — Done (`ee73ec1`).
      Audit: found 6 functions independently hand-rolling "disk name + partition number"
      parsing, each special-casing only `nvme`/`mmcblk` by substring before falling back to
      a bare "trailing digits" parse — which mis-parses loop device partitions (e.g.
      `/dev/loop0p1` parsed as disk `/dev/loop0p`, part `1`, instead of disk `/dev/loop0`,
      part `1`). Loop devices are this project's own prescribed safe-testing mechanism, so
      this bug could have silently broken exactly the kind of test rigs the audit's safety
      rules call for. Fixed the safe ones (pure string parsing, no system calls, fully
      round-trip unit-testable):
      - `engine/partition.go` `SplitDiskAndPartition`: rewritten to detect the "pN" infix
        structurally (disk name ends in a digit) instead of a substring allowlist.
      - `engine/orchestrator.go` `DetectActiveRootDisk`: removed its own duplicate inline
        parser, now calls the shared `SplitDiskAndPartition`/`JoinDiskAndPartition`.
      - `engine/autodiscover.go` `DiscoverHostStorage`: same — removed the private
        duplicate `splitDiskAndPartition`/ad-hoc `findESPDevice` fallback, now uses the
        shared helpers.
      - `engine/revert_linux.go` `partitionExists`: now builds the `/sys/class/block` name
        via `JoinDiskAndPartition`.
      - `engine/backup.go` `resolveBackupMountNode`: now resolves partition 1 via
        `JoinDiskAndPartition`.
      Tests (`engine/disk_naming_test.go`): `SplitDiskAndPartition`/`JoinDiskAndPartition`
      exhaustive table tests across sdX/nvme/mmcblk/loop/vda naming, plus a round-trip
      (`Join(Split(x)) == x`) test across all of them.
      **Found but NOT changed (documented, not "safe"):** `engine/usb_manager.go` and
      `engine/backup.go`'s USB-identification heuristic (`strings.HasPrefix(dev.Name,
      "sdc"/"sdd"/"sde")` as a fallback alongside the authoritative `lsblk`
      `TRAN=="usb"`/`RM` checks) assumes USB drives land at a specific positional device
      letter — a real portability risk on multi-disk machines, but changing disk-role
      detection heuristics can't be verified without real multi-disk hardware, so left
      untouched and flagged here rather than guessed at blind.

### D. Launchers
- [x] 9. autorun.sh: confirm before root installs, safe rm -rf, no go get/mod tidy — Done
      (`bd5f6d7`). Rewrote `autorun.sh`'s control flow: it used to `exec sudo ...` as
      literally the first thing (before any dependency check or build), so `go get`/
      `go mod tidy`/`go build` and the module/build caches all ran AS ROOT — writing
      root-owned files into the invoking user's own `$GOPATH`/`$GOCACHE` and this repo's
      `bin/`. Now: dependency checks, confirmation, and the full build run as the invoking
      user; root is requested exactly once, right before `exec`-ing the already-built
      binary (which genuinely needs it for partition/bootloader operations). Package
      installs (`assert_host_dependencies`) now call `confirm_or_abort` first (skippable
      with `-y`/`--yes`; aborts loudly instead of hanging if stdin isn't a TTY and `-y`
      wasn't given) and `sudo` only that specific install command, not the whole script.
      `rm -rf "$SCRIPT_DIR/bin"` and every other destructive `rm -rf "$SCRIPT_DIR/..."` now
      use `"${SCRIPT_DIR:?}/..."`, verified to abort instead of resolving to `rm -rf /bin`
      if `SCRIPT_DIR` were ever empty. `go get ...`/`go mod tidy` replaced with
      `go mod download` (fetches what's already pinned in go.sum; never mutates
      go.mod/go.sum or hits the network for anything but already-resolved module
      versions). Verified: `bash -n` and `shellcheck` both clean; `./autorun.sh -h` runs
      end-to-end (safe, exits before touching anything); `confirm_or_abort`'s two branches
      and the `${SCRIPT_DIR:?}` guard verified in isolated, safe subshell tests (not
      against the real repo/system).
- [x] 10. autorun.ps1: Go bootstrap version fix / GOTOOLCHAIN — Done (`50113e5`).
      Root cause: hardcoded `$DownloadUrl = "https://go.dev/dl/go1.22.6.windows-amd64.zip"`
      — go.mod requires `go 1.26.0`; the bootstrapped toolchain was stale and could drift
      further any time go.mod's directive changes without anyone remembering to update this
      literal. Fixed to parse the required version directly from go.mod
      (`Select-String -Pattern '^go\s+(\d+\.\d+(?:\.\d+)?)'`, padding a two-part version
      like "1.26" to "1.26.0" for the archive-name convention) and build the download URL
      from that; also sets `$env:GOTOOLCHAIN = "auto"` unconditionally (both when Go is
      already on PATH and after bootstrapping) so Go's own 1.21+ toolchain manager
      transparently upgrades if the resolved version is ever still behind what go.mod
      needs. Verified: PowerShell's own parser (`[System.Management.Automation.Language.
      Parser]::ParseFile`) reports no syntax errors; `PSScriptAnalyzer` reports zero
      Error-severity findings (only pre-existing, unrelated style/Information-level
      findings across the whole file — trailing whitespace, `Write-Host` usage — not
      introduced by this change and not fixed here, out of scope for this item); the
      version-extraction regex verified against the real go.mod (resolves to `1.26.0`) and
      against a scratch two-part-version fixture (resolves to `1.24.0`), with no network
      call or real download performed.

**CHECKPOINT 1:** Reached. All 10 items done, build/vet/test green on every commit. See chat
summary delivered at this checkpoint for the full per-commit breakdown.

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
