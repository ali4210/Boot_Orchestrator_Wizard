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
- [x] F6. autorun.sh: block only on root-owned bin/; journal gets a warning, not exit 1;
      engine chowns the journal to SUDO_UID/SUDO_GID after creating it; test the real
      autorun.sh (sourced), not a pasted copy — Done (`ab9d4a2`). Restructured
      `autorun.sh` so it can be `source`d for testing: `SCRIPT_DIR` now uses
      `: "${SCRIPT_DIR:=...}"` (respects a pre-set value instead of always recomputing),
      the action sequence moved into a `main()` function, and a trailing `if [[
      "${BASH_SOURCE[0]}" == "${0}" ]]; then main "$@"; fi` guard runs it only when the
      script is executed directly — sourcing just defines the functions. Split
      `check_root_owned_files`: a root-owned file under `bin/` still hard-blocks (exit 1,
      prints the `chown` fix); a root-owned journal now only prints a `[WARNING]` and
      continues. `safety/journal.go`'s `NewJournal` now calls `chownToSudoUser` (new,
      alongside the pure, testable `resolveSudoOwner`) right after writing the journal for
      the first time, handing ownership back to `SUDO_UID`/`SUDO_GID` when running under
      sudo, best-effort. Tests:
      `autorun_test.go` (new root-level test package) sources the real `autorun.sh` via
      `source` with `SCRIPT_DIR` pointed at a scratch dir — no pasted copy of the function —
      covering no-offenders, root-owned-bin-blocks, and root-owned-journal-warns-only (the
      latter two skip gracefully if passwordless `sudo` isn't available, so this never hangs
      on a password prompt). `safety/journal_chown_test.go`: `resolveSudoOwner` parsing table
      test, plus an end-to-end `NewJournal` test that sets `SUDO_UID`/`SUDO_GID` to the test
      process's own uid/gid (a self-chown needs no elevated privilege) to exercise the real
      `os.Chown` call path, not just the parsing logic.
      **Superseded by F12 below:** the `SUDO_UID`/`SUDO_GID` chown this item added was
      removed in F12 (along with `safety/journal_chown_test.go`) — on reflection, handing
      the journal's ownership to the unprivileged invoking user was the wrong direction
      entirely, since the journal is state a later elevated run trusts for real disk
      operations. F12 fixes the actual root-owned-journal problem this item was patching
      around by moving the journal outside the repo into a directory the user can't write
      to, rather than chowning a sensitive file to them. The restructuring this item made to
      `autorun.sh` (sourceable, `main()`-guarded) and `check_root_owned_files`'s `bin/`
      hard-block remain and are unaffected.
- [x] F7. PreflightStorageSafetyCheck: skip ONLY blkid exit code 2; any other blkid/lsblk
      error aborts (fail closed); use `blkid -p`; tests for exit 4, missing binary, exit 2
      — Done (`567c706`). Root cause (`engine/preflight_safety.go`
      `detectDangerousStorage`): `if err != nil { continue }` treated ANY blkid error —
      wrong exit code, a permission error, an I/O error, or the binary being completely
      missing — as "couldn't classify, assume not dangerous, keep going": fail-open, the
      opposite of a safety gate. Rewrote so only a real blkid exit code 2 (util-linux's
      documented "no recognizable superblock/filesystem found," a normal case e.g. an
      unformatted partition) is treated as a non-finding; every other outcome —
      `exitCodeOf(err) == -1` for a missing binary (`exec.Error`/`exec.ErrNotFound`, not an
      `*exec.ExitError`), or any other nonzero exit — now returns an error that
      `PreflightStorageSafetyCheck` propagates as an abort, having written nothing. Also
      switched to `blkid -p` (forces a live low-level probe instead of possibly answering
      from blkid's on-disk cache, which could report a stale type for a disk reformatted or
      encrypted since the cache was last built). Tests
      (`engine/preflight_safety_test.go`): exit code 2 still passes (pre-existing test,
      re-verified against the new fail-closed logic), new exit code 4 aborts, new missing
      binary (`exec.ErrNotFound`) aborts — both asserting a non-nil error, the exit-4 case
      also asserting the error names the exit code.
- [x] F8. headless `isBlockDevice`: EvalSymlinks first, then os.Stat + require ModeDevice
      without ModeCharDevice; tighten `isLikelyLiveRootDisk` to exact device or
      device+partition-suffix only — Done (`67796c5`). `cmd/orchestrator/headless.go`:
      `isBlockDevice` no longer infers from a `/sys/class/block/<basename>` lookup (which
      never resolved symlinks like `/dev/disk/by-id/...`, and wasn't checking the target
      path's own type at all); now `filepath.EvalSymlinks` then `os.Stat`s the resolved
      path and requires `fi.Mode()&os.ModeDevice != 0 && fi.Mode()&os.ModeCharDevice == 0`
      — a genuine block device node, not a char device, not a regular file. Root cause for
      the `isLikelyLiveRootDisk` half: `rootSrc == diskDevice || strings.HasPrefix(rootSrc,
      diskDevice)` — the `HasPrefix` let `diskDevice="/dev/sda"` match a root mounted on
      the textually-similar but unrelated `/dev/sdaa`, and `diskDevice="/dev/sda1"` match
      the different partition `/dev/sda12`. Rewrote to match exactly, or via
      `engine.SplitDiskAndPartition(rootSrc)`'s structural parse of `rootSrc`'s real parent
      disk — `sda` now matches `sda1`/`nvme0n1p2`'s true parent disk, never `sdaa`. Tests
      (`cmd/orchestrator/headless_test.go`): a regular file literally named `sda` is
      rejected; a symlink resolving to the real block device `/dev/loop0` (this project's
      own prescribed safe-testing device, read-only — never opened/mounted/written) is
      accepted; `/dev/null` (a real char device) is rejected; `sda` vs `sdaa` no longer
      false-matches (with a sanity check that `sdaa` still matches its own partition).
- [x] F9. Verify DetectBackupTargets still excludes the disk the tool is running from,
      including when it is a live USB (root on overlay/airootfs); add a test — Done
      (`0795fb8`). Found a real gap, not just a verification: `engine/orchestrator.go`
      `DetectActiveRootDisk` resolved `rootDev` from `findmnt -o SOURCE /`, but a live-boot
      session (Debian/Kali/Ubuntu live-boot, Arch archiso) mounts `/` as an
      overlay/aufs/tmpfs combining a read-only squashfs with a writable layer — `findmnt`
      reports the source as `overlay` (or similar), never a real block device, even though
      the session is genuinely running from a USB disk. That non-`/dev/`-prefixed `rootDev`
      fell straight through to the `DetectAllInternalDisks()`/hardcoded-`/dev/sda` fallback,
      returning the **wrong** disk — so `DetectBackupTargets`'s self-exclusion check (which
      trusts `DetectActiveRootDisk` entirely) would never actually exclude the live USB the
      tool is running from, offering it as a valid target to repartition. Fixed by checking
      the well-known mountpoints each live-boot family exposes its real boot medium at
      (`/run/live/medium`, `/lib/live/mount/medium`, `/run/archiso/bootmnt`, `/isodevice`)
      when the direct `/` source isn't a block device, before falling back further. Test
      (`engine/backup_test.go`
      `TestDetectBackupTargets_ExcludesLiveUSBBootDiskEvenOnOverlayRoot`, via a new
      `withFakeFindmntAndLsblk` helper — fake `findmnt`/`lsblk` on `PATH`, never touches the
      real system): **confirmed failing before the fix** (verified by temporarily
      reverting `engine/orchestrator.go` via `git stash` and re-running — the live-boot USB
      disk was wrongly included) and passing after; also asserts an internal non-removable
      disk stays excluded and a second, genuinely pluggable USB disk stays included.
- [x] F10. autorun.sh must not honor an ambient SCRIPT_DIR; always compute it from
      BASH_SOURCE; override only via ORCH_TEST_SCRIPT_DIR + ORCH_AUTORUN_TESTING=1 — Done
      (`7b2a233`). Root cause: F6's `: "${SCRIPT_DIR:=...}"` pattern made the script trust
      ANY pre-existing `SCRIPT_DIR` in the environment — since `autorun.sh` later does
      `rm -rf "${SCRIPT_DIR:?}/bin"`, `git gc --aggressive`, and other destructive
      operations scoped to it (the `--hard` purge), an accidental environment collision (or
      a hostile one) could redirect all of that at an arbitrary directory. Fixed: `SCRIPT_DIR`
      is now always computed from `BASH_SOURCE`, except when both
      `ORCH_AUTORUN_TESTING=1` and a non-empty `ORCH_TEST_SCRIPT_DIR` are set — a
      narrowly-named, two-variable opt-in a test sets on purpose and nothing else is likely
      to set by accident. `autorun_test.go`'s `sourceAutorunAndRun` updated to use this pair
      instead of plain `SCRIPT_DIR`, and now also strips any ambient `SCRIPT_DIR` from the
      test subprocess's environment via a new `envWithoutScriptDir` helper. New test
      `TestAmbientSCRIPT_DIR_DoesNotRedirectScript`: sources the real script with a plain
      `SCRIPT_DIR=/tmp/orch-test-evil-script-dir...` set and NO opt-in, asserting the
      resulting `$SCRIPT_DIR` is the real repo root, not the planted value. **Confirmed
      failing before the fix** (verified by temporarily reverting `autorun.sh` via `git
      stash` and re-running — the ambient value leaked straight through) and passing after.
      Does not actually invoke `purge_cache`/`--hard` for real in the test (that would be
      destructive against wherever it resolved); proving `SCRIPT_DIR`'s own value is
      correctly computed is sufficient, since `--hard`'s purge has no separate directory
      resolution of its own.
- [x] F11. Move `check_root_owned_files` before flag processing and `purge_cache`; extend
      it to cover `.runtime/` and other root-run-written directories; test by sourcing the
      real script — Done (`1f04cef`). Root cause: `check_root_owned_files` ran AFTER flag
      processing, so `--hard`'s `purge_cache "hard"` (which does `rm -rf
      "${SCRIPT_DIR:?}/bin"` and `rm -rf "${SCRIPT_DIR:?}/.runtime"` unconditionally)
      executed first — a root-owned `bin/` or `.runtime/` would make those `rm -rf` calls
      fail with a raw, confusing permission error before the clear check ever ran. Moved the
      call to right after the "running as root directly" notice, before
      `assert_host_dependencies` and all flag/purge handling. Extended coverage: `.runtime/`
      is populated by the root-run binary itself (not the unprivileged build step), so a
      root-owned entry there is actually a *common*, expected case, not a rare leftover —
      it's now hard-blocked the same way as `bin/` (both are directories: removing/writing
      inside a root-owned directory genuinely fails for a non-root user). Also found and
      added `orchestrator_installed_os.json` (a second plain state file the root-run engine
      writes next to the journal, previously uncovered and missing from `.gitignore` too —
      added there as well) — treated as warning-only like the journal, since unlinking a
      *file* only requires write permission on its parent directory, not ownership of the
      file itself, so `rm -f` on it always succeeds regardless of owner. Shellcheck caught a
      real bug while writing this (SC2145, mixing a string and `"${array[@]}"` in one
      argument silently drops all but the first element) — fixed with `printf` and explicit
      indices before committing. Tests (`autorun_test.go`, sourcing the real script, not a
      pasted copy): `.runtime/` root-owned blocks (**confirmed failing before the fix** via
      `git stash` on `autorun.sh` and re-running, then restored) and
      `orchestrator_installed_os.json` root-owned only warns.
      **Superseded by F12 below:** F12 removes the journal/installed-os-record warning
      logic this item added, because both files no longer live in the repo by default at
      all — see F12's entry for why, and for the `.runtime`/`bin` directory-blocking half of
      this item, which is unaffected and still stands.

- [x] F12. The journal must not be user-writable — Done (commit TBD).
      **What changed:** `safety.DefaultJournalPath()` (new) resolves the journal to a
      dedicated state directory outside the git repository — `/var/lib/boot-orchestrator`
      first, falling back to `/var/tmp/boot-orchestrator`, then
      `~/.local/state/boot-orchestrator`, each created mode `0700` — instead of the
      repo-relative `orchestrator_journal.json` every call site used to hardcode
      (`ui/model.go` x4, `cmd/orchestrator/headless.go`, `ui/view.go`'s display string).
      `engine/orchestrator.go`'s `installedRecordPath` (`orchestrator_installed_os.json`)
      is derived from `filepath.Dir(req.JournalPath)`, so it moves automatically with no
      separate change needed. `engine/revert_linux.go` and `engine/revert_windows.go`'s
      `DiscoverSecondaryOSes`/cleanup logic now check and purge **both** the new default
      location and the legacy repo-relative paths, so an install recorded by a version of
      this tool from before this change is still found and a clean revert still purges
      everything. Removed the `SUDO_UID`/`SUDO_GID` chown added in F6 entirely (along with
      its now-pointless tests, `safety/journal_chown_test.go`) — chowning the journal to an
      unprivileged user was the opposite of the fix; `DefaultJournalPath`'s `0700` directory
      is the actual mechanism now. `autorun.sh`'s `check_root_owned_files` no longer checks
      journal/installed-os-record ownership at all (per the explicit instruction) — both
      files are gone from the repo directory by default, so there is nothing there to check.
      Added defense-in-depth validation on every `LoadJournal`: `allowedStepNames` rejects
      any `Step.Name` this engine's own code doesn't actually create via `journal.Begin`
      (closing off a tampered journal making `RollbackAll` invoke an unexpected handler),
      and `looksLikeDevicePath` rejects any `TargetDisk`/`TargetPartition`/`ShrunkPartition`
      containing `..` or not shaped like `/dev/...` (no path traversal).

      **Security tradeoff — read before relying on this:**
      - *Windows has no real guarantee here.* Go's `os.MkdirAll` mode parameter does not
        translate into a meaningful Windows ACL (it only maps to the read-only attribute
        bit), so on Windows `DefaultJournalPath` picks a conventional location
        (`%ProgramData%\BootOrchestrator`) but does **not** actually prevent an unprivileged
        user from reading or writing it. The directory-permission defense in this change is
        real only on Linux/macOS, which is also where this tool's actual disk-provisioning
        logic lives; Windows support here is BCD/WIM-deployment-only.
      - *No device-existence or serial-number check.* The task that prompted this asked for
        "device exists, sizes and serial match" validation too. This was deliberately **not**
        implemented: `SessionState` has no serial-number field at all (adding one is a schema
        change, not done here), and a raw existence/path check would produce false failures
        on legitimate resumes — device paths (`/dev/sdX`) legitimately get renumbered across
        reboots, which is exactly why `SessionState` separately carries `TargetPartUUID`/
        `ShrunkPartUUID` fields for the revert/resume path to re-resolve against rather than
        trusting the raw path. Validation here is therefore structural (shape + allowlist),
        not semantic (existence + identity) — it stops injection/tampering attempts, not a
        sophisticated attacker who can place a well-formed-looking but false `/dev/sdX`
        value.
      - *The `/var/tmp` and `~/.local/state` fallbacks are weaker than `/var/lib`.* They're
        only reached when `/var/lib/boot-orchestrator` can't be created (e.g. this binary is
        run without root at all, such as for local testing). In that case the directory is
        owned by whatever user created it — there is no root/non-root privilege boundary to
        protect yet, since no elevated run has happened. The **first** real elevated run
        will successfully create `/var/lib/boot-orchestrator` and all later runs use that
        from then on; this is a one-time bootstrap gap, not a standing weakness once the
        tool has been run as root at least once.
      - *Field validation runs on every `LoadJournal` unconditionally,* including loads done
        by the TUI's own "resume?" prompt and `DiscoverSecondaryOSes`'s multi-path scan — a
        corrupted-but-not-malicious journal (e.g. from a crash mid-write, though the
        rename-based `flushLocked` is designed to prevent partial writes) would also now be
        rejected outright rather than partially trusted. This is intentional (fail closed)
        but means a real crash-corrupted journal is unrecoverable rather than best-effort
        salvageable.

      **Tests:** `safety/journal_security_test.go` — `DefaultJournalPath` resolves outside
      the current directory and to a `0700` directory (verified for real: running this test
      suite creates a real `/var/tmp/boot-orchestrator` directory on the host, mode `0700`,
      owned by the invoking user — confirmed by `stat`); `validateLoadedJournal` accepts a
      well-formed session, rejects `..` traversal, rejects a non-device-shaped path, rejects
      an unrecognized step name, allows empty fields; `LoadJournal` end-to-end rejects a
      hand-tampered on-disk journal with an unrecognized step name and with a path-traversal
      device path; a real `NewJournal` → `Begin` → `LoadJournal` round trip still loads
      cleanly (proving validation doesn't false-positive against the engine's own real
      output). `autorun_test.go`'s now-obsolete journal/installed-os-record warning tests
      were removed along with the code they tested.

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

### E. Downloads stall or end incomplete — Done (`817cb96`)
- [x] 11. Download stall/incomplete reproduction (httptest cases) — Done, see below.
- [x] 12. Fix downloader (timeouts, resume, retry, checksum, atomic) — Done, see below.
- [x] 13. Manifest checksum/URL audit — Done, see below.
- [x] 14. .github/workflows/download-verify.yml — Done, see below.

**Root causes found in `engine/streamer.go`'s `downloadFromSingleMirror`/`StreamDownloadWithFailover`:**
1. **No stall watchdog at all.** `client.Timeout` was `0` (disabled, needed to allow slow-but-progressing multi-GB transfers) and the transport's `ResponseHeaderTimeout`/`IdleConnTimeout` only cover getting headers / idle pooled connections — neither covers a connection that accepted the request, sent headers, and then went silent mid-body. `decoded.Reader.Read(buffer)` would block forever. This is almost certainly the literal "downloads stall" symptom. Reproduced in `TestStreamDownload_StallMidBody_EventuallyFailsFast` (bounded wait with a 5s test-level timeout so the *test itself* can't hang even if the fix regresses).
2. **The same blocking read existed inside `InspectAndDecodeStream`'s own initial 512-byte format-sniff** (`io/io.ReadFull`), called *before* any watchdog existed — a server that sent zero body bytes before stalling bypassed even a loop-scoped watchdog entirely. Reproduced separately in `TestStreamDownload_StallDuringInitialFormatSniff_StillCaught` (caught only after moving the watchdog to start before this call, not just around the main copy loop).
3. **No retry at all per mirror.** A single failed attempt moved straight to the next mirror (or straight to "all mirrors exhausted" when there was only one — the common case for a single `ImageURL`). A transient blip on a multi-gigabyte download killed the whole download with zero retries.
4. **No checksum on any manifest entry — see item 13.** The protocol-level case where a response has no `Content-Length` and isn't chunked (framed by "read until connection close") makes a truncated transfer *indistinguishable from a legitimate one* at the transport layer by design (confirmed in `TestStreamDownload_NoContentLengthEarlyClose_CaughtByChecksum` — the transport reports success; only an end-to-end checksum catches it). With every manifest entry missing a checksum (see item 13), this failure mode was completely unprotected for every preset OS.
5. **Already correct, not a bug (locked in with a regression test):** a declared-but-undelivered `Content-Length` (`TestStreamDownload_TruncatedWithDeclaredContentLength_Fails`) and a chunked transfer cut short both already surface as `io.ErrUnexpectedEOF` (not `io.EOF`), which the existing `errors.Is(readErr, io.EOF)` check already correctly treats as a real error, not success.
6. **Already correct, not a bug (locked in with a regression test):** redirect chains (`TestStreamDownload_RedirectChain_Succeeds`), a mirror returning an error status failing over to the next mirror (see below for a bug found IN this path), a server ignoring `Range` and the existing fallback cleanly restarting from scratch (`TestStreamDownload_ServerIgnoresRange_RestartsCleanly`), chunked transfer encoding (`TestStreamDownload_ChunkedTransferEncoding_Succeeds`), and non-blocking progress reporting (`TestStreamDownload_ProgressReportingIsNonBlocking` — the copy loop only touches atomic counters; a separate ticker goroutine calls `onProgress`, so a slow/blocking callback can't stall the transfer).

**Fixes:**
- Added a stall watchdog (`stallTimeout`, default 30s, a var for tests): a background goroutine cancels a per-attempt `context.Context` (derived from the caller's, since `net/http` exposes no per-read deadline on `resp.Body`) if no new bytes have been read for that long. Started *before* `InspectAndDecodeStream`, covering its initial blocking read too, not just the main copy loop.
- Added `downloadFromSingleMirrorWithRetry`: retries the *same* mirror up to `downloadMaxAttempts` (default 5, a var for tests) times with exponential backoff + jitter (`downloadRetryBaseDelay`, default 2s, a var for tests) on a stall or transient error, re-resuming from whatever bytes already landed via the existing Range/resume mechanism. Reports progress-channel `Status` text like `"stalled, retrying 2/5 in 4s..."`.
- Added `errPermanentStatus`: **a bug introduced by my own first draft of the retry wrapper and caught by my own test** (`TestStreamDownload_Mirror403FailsOverToWorkingMirror` initially took 34 seconds instead of milliseconds) — a deterministic HTTP error status (403, 404, ...) was being retried 5 times with full exponential backoff before failing over, burning ~30s against a mirror that could never succeed. Fixed by giving permanent-status failures a distinct error type that `downloadFromSingleMirrorWithRetry` fails fast on instead of retrying.
- Checksum mismatch now gets exactly one clean restart (full re-download from scratch, discarding the `.part` file) before that mirror is abandoned, in case the mismatch came from a stale/corrupt partial file rather than the mirror itself.
- Added a free-disk-space pre-check (`engine/diskspace_unix.go` via `golang.org/x/sys/unix.Statfs`, `engine/diskspace_windows.go` via `GetDiskFreeSpaceExW`): once a response's `Content-Length` is known, confirms the destination filesystem has room for the remaining bytes before starting the write loop. Best-effort — skipped silently if space can't be determined.
- **Item 13 — manifest audit:** `discovery/audit.go` (`AuditEntry`/`Catalog.Audit`) checks every entry for a download URL/mirror and a SHA256 checksum. **Finding: 29/29 catalog entries (100%) are missing a checksum.** All have at least one URL. Full list and rationale for why this wasn't fixed in-session (can't safely download 29 multi-GB ISOs to compute real checksums; this is a data-curation task, not a code bug) in `docs/MANIFEST_AUDIT.md`. **Not done:** UI-level visual flagging of broken entries in the TUI catalog browser — the detection function is ready to wire in, but with literally every entry currently flagged, hiding/blocking all of them would leave the tool with zero selectable options, worse than the status quo until checksums are backfilled.
- **Item 14:** `cmd/download-verify` + `.github/workflows/download-verify.yml` — downloads every catalog entry with this tool's own downloader (`engine.StreamDownloadWithFailover`), verifies against the manifest checksum when present, **computes and reports one when absent** (so the workflow's own output can be used to responsibly backfill item 13's gap after human review of provenance), skips entries larger than the runner's free space as `SKIPPED-SIZE`, deletes each download immediately after checking so disk usage never exceeds one entry at a time, and uploads a JSON report artifact. Triggers: `workflow_dispatch` and a monthly cron — **never on push/PR**.

**Tests:** `engine/streamer_test.go` (12 tests, all passing, httptest-based — stall mid-body, stall during format-sniff, stall-then-recovers-on-retry, truncated-with-declared-Content-Length, no-Content-Length-early-close, HTML-error-page-as-200, redirect chain, mirror 403 failover, server-ignores-Range clean restart, chunked encoding, free-disk-space sanity, non-blocking progress); `discovery/audit_test.go` (6 tests, synthetic + a real-catalog report test that logs but doesn't fail the suite over the data gap); `cmd/download-verify/main_test.go` (6 tests, all against `httptest` servers with tiny payloads, never real downloads).
### F. Revert (dual boot back to single boot) does not work — Done (`00615e9`)
- [x] 15. Revert reproduction (loop-image fixture) — Done, see below.
- [x] 16. Fix revert (partition/fstab/grub/ESP restore) — Done, see below.

**Investigation:** `engine/revert_linux.go`'s `ExecuteDecommissionWithOptions` (the function
behind the TUI's revert menu, via `ui/model.go`'s `triggerTargetedRevertCmd`) had **no
testability isolation at all** — every real-system side effect (`/etc/grub.d/40_custom`,
`/etc/default/grub.d/*.cfg`, `update-grub`, `findActiveESPMount`'s hardcoded `/boot/efi`
probe, `efibootmgr`) was a literal hardcoded path, making it unsafe to exercise end-to-end
in an automated test without first adding injection points for the ones that could be made
injectable. Two real, confirmed bugs found:
1. **No swap/fstab awareness at all** (confirmed by `grep swap\|fstab
   engine/revert_linux.go` — zero matches). If install-time partitioning converted a
   trailing swap slice into the new OS's partition, revert never recreates swap, and never
   fixes `/etc/fstab` if it referenced that slice — a dangling fstab entry after revert
   (delays boot while systemd waits on it, or a swap entry that fails silently).
2. **`TargetPartUUID`/`ShrunkPartUUID`/`ExpectedFSType`/`ExpectedSizeBytes` were completely
   unused on Linux** — despite being specifically documented ("used to re-resolve the live
   device at revert time instead of trusting DiskDevice/PartitionNum directly, disk
   letters/numbers can shift between creation and revert") and actually implemented on
   **macOS** (`revert_darwin.go`'s `resolveDeviceByPartitionUUID` +
   `verifyDarwinTargetBeforeDestruction`). The Linux path — the tool's primary platform —
   blindly trusted a possibly-stale `DiskDevice`/`PartitionNum`, with **no verification at
   all** before deleting a partition. If disk naming shifted between install and revert
   (different USB port, another drive attached, NVMe enumeration change), this could
   delete/resize the wrong partition on the wrong disk.

**Reproduction:** `engine/revert_linux_loopimage_test.go` builds a real GPT disk image
(ESP + a shrunk host ext4 root with test files + a second "new OS" ext4 partition, the
literal end state of a completed dual-boot install), loop-mounts it, and calls the real
`ExecuteDecommissionWithOptions` against it. Made safe via three injectable vars added to
`revert_linux.go` (`grubCustomPath`, `grubDropinPaths`, `updateGrubFunc`, `fstabPath` —
redirected to a scratch directory for the test) plus passing an empty
`EFIDirNameToPurge`/`EFIBootEntryNum` (the only way to neutralize `findActiveESPMount`/
`efibootmgr`, which remain hardcoded — a residual testability gap, noted below). Requires
the test *process itself* to run as root (`ExecuteDecommissionWithOptions` checks
`os.Geteuid() != 0`), so it skips cleanly under a normal `go test ./...` and was run for
real via `sudo -E go test ./engine/... -run TestExecuteDecommission` to prove both the
reproduction and the fix.

**Fixes:**
- `cleanDanglingFstabEntries`/`fstabDeviceExists`: after the partition delete/grow/resize,
  scans `/etc/fstab` and comments out any entry (`UUID=`, `PARTUUID=`, `LABEL=`, or a raw
  `/dev/...` path) that no longer resolves to a real device — catching both the deleted
  partition and any swap slice consumed at install time, without needing to have tracked
  which UUID belonged to which beforehand. **Does not recreate a lost swap partition** —
  this tool doesn't currently record the original slice's size/UUID anywhere, and
  synthesizing one from scratch at revert time was judged out of scope for this session;
  documented as a known limitation rather than silently left undone.
- `resolveDeviceByPartUUID` + `verifyLinuxTargetBeforeDestruction` (mirroring the existing
  macOS pattern): re-resolves the live device by `TargetPartUUID` before touching anything,
  and aborts with a clear "safety abort" error if the device's actual filesystem type or
  size doesn't match what was recorded at install time — **proven** by
  `TestExecuteDecommission_AbortsOnFilesystemMismatch`, confirmed failing before the fix
  (temporarily disabled the check, re-ran under root, the wrong-type partition got deleted
  anyway) and passing after.
- Moved `resolveBootEntryByEFIDir` from a private copy in `ui/model.go` to exported
  `engine.ResolveBootEntryByEFIDir` (a cross-platform file, not linux-only — `efibootmgr`
  simply fails to start on other OSes and the function already handles that gracefully) so
  both the TUI and the new headless `--revert` share one implementation.
- Fixed a stale reference found during this investigation: `ui/model.go`'s
  `triggerTargetedRevertCmd` only removed the legacy repo-relative
  `orchestrator_installed_os.json` after a revert — follow-up F12 moved that file's default
  location outside the repo, so this now also removes it from
  `filepath.Dir(safety.DefaultJournalPath())`.
- Added `--revert`/`--keep-slice` to headless mode (`cmd/orchestrator/revert.go`): discovers
  secondary OSes via the identical `engine.DiscoverSecondaryOSes()` the TUI uses, requires
  **exactly one** unambiguous candidate (refuses and lists all candidates rather than
  guessing which to revert — this is destructive), and calls the identical
  `engine.ExecuteDecommissionWithOptions`. Requires `--yes`.

**Tests:** `engine/revert_linux_loopimage_test.go` (2 tests, root-gated, run for real this
session): core restore (partition removed, host grown + resized, **file checksums
byte-for-byte identical before/after**, GRUB regen invoked against scratch paths, dangling
fstab entry cleaned up, live fstab entry preserved) and the filesystem-mismatch safety
abort. `cmd/orchestrator/revert_test.go` (4 tests): guest selection (none/one/multiple-
refuses) and the `--yes` gate.

**Not done (explicitly out of scope, documented here rather than silently skipped):**
- Swap partition recreation (see above — needs a schema change to record the original
  slice's size/UUID at install time, not done in this session).
- `findActiveESPMount`/`efibootmgr` remain hardcoded to real system paths — not made
  injectable, so the ESP-file-removal and NVRAM-boot-entry-removal code paths have **no**
  automated test coverage at all (only exercised by passing empty values that skip them
  entirely). This is a real, acknowledged gap, not a passed test.
### G. transient_resize.sh is not being created — Done (`a3ee951`)
- [x] 17. transient_resize.sh not created — reproduction + fix — Done, see below.

**Root cause (`engine/orchestrator.go`, the bare-metal USB-staging block, around where
`usbStorageDir := "/mnt/orch_usb_storage"` is set up): neither of the two fallback mount
attempts was ever checked for success.**
```go
if err := exec.Command("mountpoint", "-q", usbStorageDir).Run(); err != nil {
    _ = exec.Command("mount", storageDev, usbStorageDir).Run()
}
if err := exec.Command("mountpoint", "-q", usbStorageDir).Run(); err != nil {
    _ = exec.Command("mount", "-L", "ORCH_STORAGE", usbStorageDir).Run()
}
// execution just continues here regardless of whether either mount worked
```
If **both** mount attempts failed (USB not plugged in yet, its filesystem not yet probed by
the kernel, a permission issue, a race with udev, ...), `usbStorageDir` stayed exactly what
`os.MkdirAll` made it: an ordinary, empty **local** directory on the root filesystem.
Nothing downstream knew that. `writeScriptSecurely(filepath.Join(usbStorageDir,
"transient_resize.sh"), ...)` "succeeded" by writing the script into that local directory
instead of the actual USB stick — and the existing `os.Stat(scriptPath)` "strict hardware
physical verification" check right after it **also kept passing**, because a file really
was written, just to the wrong filesystem entirely. The same blind spot affected the OS
payload copy (`copyFileDirect` to `isoSrcPath`) just above it. On the next boot, when the
system actually boots from the USB stick and its own live environment freshly mounts the
real `ORCH_STORAGE` partition, it finds **no** `transient_resize.sh` there at all — this is
almost certainly the literal reported symptom, and the orchestrator itself never saw an
error, since every signal it checked (the file existing, its size being nonzero) was
satisfied by the misplaced local copy.

**Fix:** added `ensureUSBStorageMounted` (`engine/autonomous_installer_script.go`) — an
explicit `mountpoint -q` check immediately after both fallback mount attempts — and wired
it in right before the payload-staging and script-write steps: if `usbStorageDir` still
isn't a real mountpoint at that point, the step now fails loudly (`journal.Fail` +
`fail("partition-shrink", ...)`) with a message telling the user to reconnect the USB drive
and retry, instead of silently writing to the wrong filesystem and reporting success.

**Tests (`engine/usb_storage_mount_test.go`):** the negative case (an ordinary, unmounted
`t.TempDir()` — exactly what `usbStorageDir` becomes when both mount attempts fail) is
rejected; the positive case uses a **real** tmpfs mount (RAM-backed, never touches any real
disk, created/torn down via passwordless `sudo` and skipped cleanly if unavailable) to prove
this isn't just "always returns an error" — a genuinely mounted filesystem is accepted.
### H. "Deploy payload" ISO picker browses /root under sudo — Done (`c56b8f8`)
- [x] 18. Deploy payload ISO picker — reproduction — Done, see below.
- [x] 19. Fix ISO/image picker — Done, see below.

**Root cause (`engine/file_dialog.go` `GetRealUserHome`), confirmed by code reading (the
literal question asked — "does it browse /root instead of the invoking user's home" — was
already answered "no, not /root specifically" since the function explicitly excludes
`/root`; the REAL bug is one step more subtle):** the function **guessed** the invoking
user's home as `filepath.Join("/home", sudoUser)` instead of looking it up authoritatively.
That guess is right for the common default layout, but wrong for any account whose home
isn't literally at `/home/<username>` (a custom `useradd -d`, an NSS/LDAP-provided home
elsewhere). When the guess missed, the function fell through to `os.UserHomeDir()` — which,
running under `sudo`, resolves to **root's own home** (correctly excluded) — and then to a
hardcoded `"/home/kali"` fallback that was clearly a leftover from this project's own
development/testing environment, not something meaningful for any other user, and finally
to `"/"` (the filesystem root) if even that didn't exist either: a surprising, unhelpful
place for an ISO/image picker to land for any invoking user whose setup doesn't happen to
match the exact default.

**Fix:** resolves via `os/user.Lookup` (through an injectable `userLookupFunc` var, so a
test can simulate a custom-home account without creating/deleting a real system user) —
the real passwd/NSS database entry for `SUDO_USER`, authoritative rather than a guessed
path pattern. Removed the hardcoded `"/home/kali"` fallback entirely.

**Both selection modes, per the task's explicit request:**
- **Interactive directory browsing** (`ListDirectoryContents` with an empty path — what the
  TUI's picker screen starts at): now resolves through the fixed `GetRealUserHome`.
  `TestListDirectoryContents_UsesRealUserHomeNotRoot` proves no listed entry is rooted
  under `/root` when `SUDO_USER` is set.
- **Direct ISO/image path selection** (the `--iso`/`--iso-path` CLI flow, or a manually
  entered path): confirmed by code inspection and
  `TestListDirectoryContents_DirectISOSelectionBypassesHomeResolution` to never consult
  `GetRealUserHome`/`SUDO_USER` at all — `ListDirectoryContents` only substitutes the real
  home for an empty or literal `"/root"` path, so an explicit path is always used exactly
  as given, independent of this bug.

**Tests (`engine/file_dialog_test.go`):** `GetRealUserHome` resolves to the real current
user's actual passwd-database home (not a guess) when used as `SUDO_USER`; never returns
`/root` when `SUDO_USER` is set; **the precise regression** —
`TestGetRealUserHome_UsesLookupHomeNotGuessedPath` — simulates (via the injectable lookup,
no real `useradd`) an account whose real home is deliberately *not* under `/home/<name>`
and proves the resolved path is the looked-up one, not the guessed one; falls back safely
(no fabricated path) for a `SUDO_USER` naming an account that doesn't exist at all; plus
the two `ListDirectoryContents` tests above and an `IsSupportedPayload` extension-matching
table test.

**CHECKPOINT 2:** Reached. All 4 bugs (E/F/G/H) fixed with reproducing tests. See chat
summary delivered at this checkpoint for the full per-bug breakdown. 41 commits total on
`fix/audit-phase-0-1` as of this checkpoint; nothing pushed.

## Pre-Phase-3 follow-ups ("Slice 1", requested before starting Phase 3)

- [x] F13. No catalog entry may be silently provisioned unverified; show an "UNVERIFIED
      download" warning and require confirmation in both the TUI and headless mode. Do not
      invent checksums — Done (`a7de3b9`). **Checksums not backfilled**: per the explicit
      instruction, no SHA256 was added for any entry — none of the 29 current entries could
      be sourced from an official *signed* checksum file within this session (see
      `docs/MANIFEST_AUDIT.md`, written in the prior session; still accurate). What *was*
      built is the confirmation gate itself:
      - **TUI** (`ui/model.go`): new `ScreenUnverifiedDownloadConfirm`. Selecting a catalog
        entry with an empty `SHA256` in `ScreenOSSelect` now routes through this screen
        instead of proceeding straight to disk/USB selection; declining clears the
        selection and returns to the OS list, confirming proceeds via a new
        `proceedAfterOSSelected()` helper (extracted from the inline logic both the direct
        and confirmed paths now share). Rendered in `ui/view.go`
        (`viewUnverifiedDownloadConfirm`).
      - **Headless** (`cmd/orchestrator/headless.go`/`main.go`): new `--allow-unverified`
        flag, deliberately separate from `--yes` (`--yes` confirms the target disk/OS/
        account; this specifically confirms accepting an unverifiable download — conflating
        them would make it too easy to pass once and forget what was actually being
        accepted). `buildHeadlessRequest` refuses any resolved image with an empty checksum
        — covering both `--os-id` catalog entries and `--iso-path` (which is *always*
        unverified, since there's no catalog checksum for an arbitrary local file) — unless
        the flag is set.
      - **Scope note:** the TUI gate covers catalog-entry selection (`ScreenOSSelect`) —
        literally "no catalog entry", per the task wording. The separate "Deploy payload"
        custom-ISO/LAN-peer/staged-payload flows (`ScreenClientPanel`, `ScreenLANPeerConnect`,
        `ScreenFilePicker`) are a different code path and were not gated here; see F16 below
        for that investigation.
      - Tests: `cmd/orchestrator/headless_test.go` (refuses without the flag for both
        `--os-id` and `--iso-path`, allows with it); `ui/os_select_test.go` (Bubble Tea
        model tests driving real key messages: unverified entry routes to the confirm
        screen, verified entry skips it, decline returns to the OS list and clears the
        selection, accept proceeds in both advanced and non-advanced mode, plus a render
        smoke test).

- [x] F14. Revert: make `findActiveESPMount`/`efibootmgr` injectable, test ESP file removal
      and boot-entry removal on the loop image; record the original swap slice (size, UUID)
      at install time and recreate it on revert (`2db7f6a`)
      - `findActiveESPMount` and the `efibootmgr -b <num> -B` NVRAM-removal call are now
        injectable package vars (`findActiveESPMountFunc`, `efibootmgrRemoveFunc`), alongside
        a new `isUEFIModeFunc` (the UEFI-mode gate itself needed to be injectable too, so a
        test can force it on without real EFI firmware under the test process).
      - Install time: `CarveHardwarePartitionAtBoundary`'s trailing-swap-conversion branch now
        captures the swap slice's size and PARTUUID (via `blkid`) before deleting it, returned
        on `PartitionResult` and persisted into `safety.SessionState.OriginalSwapSizeBytes`/
        `OriginalSwapUUID` by `RunProvision`.
      - Revert time: `DetectedGuestOS` carries those two fields from the journal (only the
        journal-sourced guest entry can know them — os-prober/lsblk discovery has no way to
        know what a partition used to be before install), threaded into `RealRevertParams` at
        both real call sites (`cmd/orchestrator/revert.go`'s `runHeadlessRevert`, `ui/model.go`'s
        `triggerTargetedRevertCmd`). When `OriginalSwapSizeBytes > 0`, `ExecuteDecommissionWithOptions`
        skips `growpart` (which has no "stop short" mode) and instead resizes the host partition
        to leave exactly that much trailing free space, then `recreateSwapPartition` carves a new
        swap partition there, restores its original GPT PARTUUID via `sgdisk -u`, formats it with
        `mkswap`, and `ensureSwapFstabEntry` adds a `PARTUUID=... none swap sw 0 0` fstab line if
        one isn't already present.
      - Real bug hit and fixed during this work, not just a design choice: both the host-resize
        boundary and the swap `mkpart` call were first computed by hand from `blockdev --getss`/
        `--getsz` (raw sector count). That failed for real on the loop-image test — GPT reserves
        a trailing region (backup header + partition array, ~33-34 sectors) that raw sector count
        includes but parted's script mode will never let a partition use; pinning both a
        hand-computed start AND end made parted refuse rather than auto-round. Fixed by reading
        the disk's own free-space region via `parted -m unit s print free` (new
        `trailingFreeSectorRange` helper) and handing those exact boundaries back to `mkpart`/
        `resizepart`, instead of computing them independently.
      - Tests (`engine/revert_linux_loopimage_test.go`, real loop-device, root-gated):
        `withScratchRevertPaths` now also redirects `findActiveESPMountFunc`/
        `efibootmgrRemoveFunc`/`isUEFIModeFunc`, so
        `TestExecuteDecommission_RestoresHostAndRemovesNewOSPartition` was extended to pass real
        (non-empty) `EFIDirNameToPurge`/`EFIBootEntryNum` and assert the scratch ESP payload
        directory is actually removed and the spy records the boot-entry number — closing the
        gap this file's own comment used to document ("does NOT redirect findActiveESPMount or
        efibootmgr ... tests must pass an empty EFIDirNameToPurge/EFIBootEntryNum"). New
        `TestExecuteDecommission_RecreatesOriginalSwapPartition` reproduces the swap-loss bug
        (fixture's "new OS" partition stands in for a swap-converted partition) and asserts the
        host partition stops short of 100%, a swap partition is recreated at the same number,
        same size (±16MiB), restored PARTUUID, and a working fstab entry — verified failing
        before the `trailingFreeSectorRange` fix (real `blkid`/`parted` error from the
        hand-computed-sector-math bug above) and passing after. All three
        `TestExecuteDecommission_*` tests plus the full `go test ./...` and `go vet ./...` run
        clean; the root-gated tests were run for real via
        `sudo -E $(which go) test ./engine/... -run TestExecuteDecommission -v`.
      - Not covered by this item (pre-existing gaps, unchanged): `ui/provision.go`'s
        `runRealRevert` (dead code — no caller — still takes individual disk/partNum/hostPart/
        efiDir/bootEntry args rather than a `DetectedGuestOS`, so it was not threaded with swap
        fields); the autonomous-installer USB-staging path's own partition-carving logic doesn't
        do swap conversion, so it needs no equivalent handling (not re-verified in this session,
        carried over from the prior session's note).
- [x] F15. transient_resize.sh: partprobe + udevadm settle + bounded retry (10x1s) before
      declaring the USB mount failed; try UUID as well as label (`8257aa6`)
      - Before: transient_resize.sh's ORCH_STORAGE vault mount was exactly one
        `mount -L ORCH_STORAGE /mnt/vault 2>/dev/null || true` attempt — no re-probe of the
        kernel's partition table, no retry, no UUID fallback, and no check of whether it had
        actually succeeded (the `|| true` swallowed the failure and let the script barrel
        ahead with a non-existent payload path).
      - Extracted the mount logic into its own Go-level function,
        `usbStorageMountRetryBlock(vaultDir, storageUUIDVar)` in `engine/autonomous_installer_script.go`,
        so it's independently testable rather than only checkable by reading generated text. It
        now runs `partprobe`/`udevadm settle --timeout=5` before each of up to 10 attempts (1s
        apart), tries `mount -L ORCH_STORAGE` then falls back to `mount -U "$STORAGE_UUID"`, and
        explicitly fails loudly (`exit 1` with a clear message) if none of the 10 attempts
        succeed, instead of silently continuing.
      - `STORAGE_UUID` is a new field on `autonomousInstallerScriptParams`/the rendered script,
        resolved host-side in `RunProvision` via `blkid -s UUID -o value "$storageDev"` (the
        storage partition is already located by that point to stage the payload) and passed
        through at render time.
      - Tests (`engine/autonomous_installer_script_test.go`): per the task's "test with a fake
        mount that succeeds on the Nth try," `runUSBStorageMountRetryBlock` actually executes
        the generated bash fragment under real `bash` with stub `mount`/`partprobe`/`udevadm`/
        `sleep` binaries on `$PATH` (`sleep` stubbed to a no-op so the test suite stays fast).
        `TestUSBStorageMountRetryBlock_SucceedsOnThirdAttempt` proves a mount stub that fails
        twice and succeeds on the 3rd call is tolerated (and that it stops retrying immediately
        on success — exactly 3 calls, not 10). `TestUSBStorageMountRetryBlock_FailsLoudlyAfterTenAttempts`
        proves it gives up after exactly 10 attempts with a clear error.
        `TestUSBStorageMountRetryBlock_FallsBackToUUID` proves the LABEL-fails/UUID-succeeds
        path. `TestRenderAutonomousInstallerScript_EmbedsUSBStorageMountRetryBlock` proves the
        full rendered script actually wires the block in (declares `STORAGE_UUID`, embeds the
        10-attempt loop, the partprobe/settle calls, and the UUID fallback) rather than the
        fragment and the rendered script silently drifting apart.
      - Reproduce-first verified empirically: with `engine/autonomous_installer_script.go` and
        `engine/orchestrator.go`'s changes stashed (`git stash push`), `go vet ./engine/...`
        fails outright — `undefined: usbStorageMountRetryBlock` — since the pre-fix code had no
        such function at all for the new tests to exercise; restoring the fix (`git stash pop`)
        made `go build`/`go vet`/`go test ./...` pass clean again.
      - Not covered by this item (a different artifact, not transient_resize.sh, so out of
        scope per the task's literal wording): `engine/usb_manager.go`'s GRUB menu entry kernel
        cmdline (`search --no-floppy --set=storage_part --label ORCH_STORAGE` /
        `systemd.run="... for i in $(seq 1 30); do mount -L ORCH_STORAGE ..."`) — this is the
        mechanism that locates and launches transient_resize.sh in the first place, on first
        boot into the live USB image; it already has its own 30x1s retry but no partprobe/
        udevadm settle/UUID fallback. Left as-is.
- [x] F16. ISO picker: Bubble Tea model test driving Deploy payload -> picker -> .iso
      selection, and direct .iso path entry; wire the custom-ISO path if unwired (`88a885c`)
      - Two genuinely separate gaps, both fixed:
        1. **The interactive picker was unwired for the client-deploy flow.** `ScreenClientPanel`'s
           "Deploy from Local Payload" option (no staged payload auto-detected) routed to
           `ScreenFilePicker`, but that screen's selection handler (`esc`/`enter`/`s`) only ever
           recognized two destinations — SCP push and the seeder dashboard, both server-role. A
           file picked from the client-deploy launch site silently fell through to the seeder
           branch and landed on `ScreenSeederDashboard` — the wrong screen, server role — so the
           picked .iso never reached a `ProvisionRequest` at all.
        2. **The `--iso` CLI flag was dead.** `cmd/orchestrator/main.go`'s `CustomISOPath`
           package variable was set from `--iso` but `ui.NewModel()` took zero arguments, so
           nothing downstream ever read it — confirmed by headless.go's own comment pointing at
           this as a known-unwired gap.
      - Fix: added a `pickerForClientDeploy` bool to `ui.Model` (alongside the existing
        `pickerForSCP`), set at the client-deploy picker launch site and explicitly cleared at
        the other two (seeder/SCP) launch sites; `ScreenFilePicker`'s `esc`/`enter` handlers now
        check it first. Extracted the staged-payload "build a synthetic discovery.Entry and jump
        to ScreenCustomBootModeSelect" logic (previously duplicated twice) into one shared
        `(m *Model) selectLocalPayload(path, sizeBytes, versionLabel)` helper, used by: the
        staged-payload shortcut (unchanged behavior), the picker's file-selection branch (the
        fix), and the new `--iso` wiring. `ui.NewModel(customISOPath string)` now takes the path;
        a valid file wires straight into `selectLocalPayload` (skipping catalog/role selection
        entirely, mirroring how `--iso-path` skips catalog selection in headless mode); a missing
        path or a directory sets `m.fatalErr` (rendered on every screen) instead of being
        silently ignored. `cmd/orchestrator/main.go` now passes `CustomISOPath` through.
      - Tests (`ui/iso_picker_test.go`, pure in-process Bubble Tea `Update()` driving, no real
        disk/journal I/O — `startProvisioning` itself is deliberately never called from a test:
        it also writes to the REAL, non-injectable `safety.DefaultJournalPath()`, which this
        whole project is never allowed to touch from a test):
        `TestClientDeploy_PickerSelectsISO_CarriesPathIntoProvisionRequest` drives real
        `tea.KeyMsg`s through `ScreenModeSelect` -> `ScreenAdvancedRoleSelect` ->
        `ScreenClientPanel` -> `ScreenFilePicker`, feeds a synthetic `dirContentsMsg` (no real
        filesystem read) containing a staged temp `.iso`, selects it, and asserts
        `m.overridePayloadURL`/`m.selectedOS.DownloadURL` — the exact field pair
        `startProvisioning` (ui/model.go) uses verbatim to build `ProvisionRequest.ImageURL` —
        equal the picked path, plus a real `os.Stat`-derived size.
        `TestClientDeploy_PickerEscape_ReturnsToClientPanel` covers the `esc` half of the same
        bug. `TestNewModel_DirectISOPath_CarriesPathIntoProvisionRequest`,
        `_MissingFileIsAFatalErrorNotSilentlyIgnored`, and `_DirectoryIsAFatalError` cover the
        `--iso` wiring (success, missing file, directory); `TestNewModel_NoISOPath_...` is the
        no-op sanity check for every other caller of `NewModel`.
      - Reproduce-first verified empirically twice: (1) stashing `ui/model.go`+
        `cmd/orchestrator/main.go`+`ui/os_select_test.go` and running `go vet ./ui/...` fails
        outright (`too many arguments in call to NewModel`) since the old signature took no
        path argument at all; (2) with the fix restored but the picker's
        `case m.pickerForClientDeploy:` enter-branch manually removed again,
        `TestClientDeploy_PickerSelectsISO_CarriesPathIntoProvisionRequest` fails for real
        (`expected ScreenCustomBootModeSelect ..., got 8` — screen 8 is
        `ScreenSeederDashboard`), confirming the test actually catches the bug rather than just
        matching the fix's own shape. One test environment wrinkle discovered and worked around,
        not a bug: this dev sandbox's own `/tmp` has stray `.bin` files left over from unrelated
        prior work that `engine.InspectStagedPayload()`'s broad extension match
        (`IsSupportedPayload`) treats as a "staged payload," which short-circuits the picker
        test path entirely — the test detects this (`m.hasStagedPayload`) and skips cleanly with
        a clear message rather than flaking; verified passing for real with those files
        temporarily moved aside.
      - Not covered (same scope note as F13): catalog-entry selection via `ScreenOSSelect` was
        already covered by F13's tests; `ScreenLANPeerConnect`/USB-detected payloads
        (`ScreenUSBSelect`) already had working `overridePayloadURL` wiring before this item and
        were not touched.
- [x] F17. Journal: tests must not touch real /var/lib or /var/tmp; test that every
      `journal.Begin` name is in `allowedStepNames`; document where the journal lives after
      reboot into the USB environment (`7310c43`)
      - **Real bug found and fixed, wider than just this package's own tests:**
        `ui.NewModel()` unconditionally calls `safety.LoadJournal(safety.DefaultJournalPath())`
        on every construction (to pick up any unfinished session), and `DefaultJournalPath()`
        really runs `os.MkdirAll` against `/var/lib/boot-orchestrator` then
        `/var/tmp/boot-orchestrator` (mode 0700) until one succeeds. Confirmed on this dev
        machine: both directories existed for real (one root-owned, created by an earlier
        `sudo -E go test` run in this engagement; one kali-owned, from a plain `go test` run) —
        meaning every `ui` package test that ever constructed a `Model`, not just this
        package's own journal tests, had been touching real `/var/lib`/`/var/tmp` the whole
        time. (Left those two pre-existing leftover directories alone rather than deleting
        them — cleaning up state under `/var/lib`/`/var/tmp` outside a test's own `t.TempDir()`
        is exactly the kind of action this project's own rules say to leave for the user to
        decide on, not something to do unprompted.)
      - Fix: `safety/journal.go`'s two hardcoded Unix candidate directories are now a var,
        `journalUnixCandidateDirs`, with an exported `SetJournalCandidateDirsForTesting(dirs)
        (restore func())` so any package's test can redirect it to a scratch dir. Added
        `ui/main_test.go`'s `TestMain`, which redirects it to a temp dir for the whole `ui`
        test binary run — covering every current and future `ui` test that constructs a
        `Model`, not just the ones added in this item. `safety/journal_security_test.go`'s two
        pre-existing tests that call `DefaultJournalPath()` directly
        (`TestDefaultJournalPath_ResolvesOutsideTheCurrentDirectory`,
        `_DirectoryIsNotWorldOrGroupWritable`) now redirect via the same helper first.
      - New completeness tests (`safety/journal_stepnames_test.go`), per the task's literal
        ask: `TestAllJournalBeginCallSites_AreInAllowedStepNames` statically scans every `.go`
        file in the repo (via `go/parser`/`go/ast`, not an import of `engine` — avoids any
        import-cycle concern) for a literal `X.Begin("name", ...)` call and fails listing any
        step name used that isn't in `allowedStepNames` — the real-world failure mode being
        that such a step would commit fine but then make `LoadJournal` permanently refuse to
        reload that exact journal (it looks "unrecognized" to `validateLoadedJournal`), even on
        the very next run of the same binary that just wrote it.
        `TestAllowedStepNames_EveryEntryHasAUseOrIsDocumentedAsReserved` is the reverse
        direction, documenting a secondary finding this scan surfaced: `allowedStepNames`
        currently has 6 entries ("docker-isolation", "uefi-set-boot-next",
        "uefi-set-boot-order", "bcd-mutation", "bcd-add-entry", "grub-set-default") with zero
        `journal.Begin` call sites anywhere in the repo — they exist only as
        `engine/orchestrator.go`'s `RollbackAll` handler-map keys. Not a security issue (an
        allowlist being too permissive can't let a tampered journal invoke anything that isn't
        already gated by a step actually being present), but dead allowlist surface; this test
        pins the exact current set so it fails loudly if it silently grows or shrinks instead.
      - Reproduce-first verified empirically for both new tests: temporarily added
        `journal.Begin("totally-unlisted-step-xyz", nil)` to `engine/orchestrator.go` —
        `TestAllJournalBeginCallSites_AreInAllowedStepNames` failed for real, then passed again
        after reverting (`git checkout -- engine/orchestrator.go`); temporarily added an unused
        entry to `allowedStepNames` — `TestAllowedStepNames_...` failed for real, then passed
        again after removing it. Also verified the `ui/main_test.go` fix empirically: recorded
        the two pre-existing real directories' mtimes, ran `go test ./ui/...` and
        `go test ./safety/...` with `-count=1` (bypassing cache), confirmed the mtimes were
        unchanged afterward — proving the fix actually stops new real-filesystem touches,
        not just that it compiles.
      - **Where the journal lives after reboot into the USB environment** (documented, not
        just verified by inspection — confirmed by grepping `engine/autonomous_installer_script.go`
        and `engine/usb_manager.go` for any reference to "journal": there is none): the journal
        is purely host-side bookkeeping. `safety.DefaultJournalPath()` resolves to
        `/var/lib/boot-orchestrator/journal.json` (or the `/var/tmp` fallback) on the HOST's own
        persistent root filesystem, written by the orchestrator binary while it runs under the
        original host OS, BEFORE the autonomous-reboot handoff into the live USB staging
        environment. `transient_resize.sh` (generated by `renderAutonomousInstallerScript`) and
        the GRUB menu entry that launches it (`engine/usb_manager.go`) never read, write, or
        otherwise reference the journal at all — not even when they mount the host's own
        partition at `/mnt/host` and chroot into it for `update-initramfs`/`update-grub` (that
        chroot only ever touches `/boot/efi`, `/dev`, `/proc`, `/sys` bind-mounts, never
        `/var/lib`). So after the install completes and the machine reboots again, the SAME
        journal file is sitting at the SAME path on the host's own disk, exactly as the
        original orchestrator run left it — nothing in the USB-staging detour can see or
        disturb it. The only thing that ever removes it post-install is
        `ExecuteDecommissionWithOptions` (the revert path), which purges it deliberately as its
        own final step — an ordinary revert operation, unrelated to the reboot-into-USB cycle.
- [x] F18. Document download-verify.yml's exact workflow_dispatch steps; record per-bug
      (E-H) whether reproduced or only inferred (docs-only, no code change)

      **Running `.github/workflows/download-verify.yml` via `workflow_dispatch` — exact steps.**
      This workflow downloads every catalog entry FULLY (real, multi-gigabyte files) using
      this project's own downloader, so it is never run on push/PR — only on demand or the
      monthly cron. Not run as part of this session's own work (it would consume real CI
      minutes/bandwidth for dozens of multi-GB downloads with no code under review to justify
      it); documenting the steps, not executing them.

      *Via the GitHub web UI:*
      1. Push this branch (or merge it) so the workflow file exists on a branch GitHub can
         run it from — `workflow_dispatch` only works against a ref where the workflow
         definition is already present.
      2. Open the repo on GitHub -> **Actions** tab -> select **"Download Verify"** in the
         left-hand workflow list.
      3. Click **"Run workflow"** (top right of the runs list) -> pick the branch/ref to run
         against -> **"Run workflow"** (green button). No inputs are defined for this
         workflow, so there is nothing else to fill in.
      4. Watch the run under the "Download Verify" workflow's run list (up to a 360-minute
         timeout — downloading ~29 catalog entries for real is slow). The single job's
         `continue-on-error: true` on the `go run ./cmd/download-verify` step means the run
         shows green even if individual entries report `MISMATCH`/`FAILED`/`SKIPPED-SIZE` —
         the pass/fail signal to actually look at is the artifact's contents, not the run's
         overall status badge.
      5. Once finished, open the run -> **Artifacts** section at the bottom -> download
         `download-verify-report` (a `download-verify-report.json`, retained 90 days) and
         inspect each entry's `status`/`expected_sha256`/`computed_sha256`/`detail` fields
         (see `cmd/download-verify/main.go`'s `EntryReport` struct for the exact schema).

      *Via the `gh` CLI, equivalently:*
      ```bash
      gh workflow run "Download Verify" --ref <branch-name>
      # find the run just queued (there is no input to tag it with, so grab the most recent):
      gh run list --workflow="Download Verify" --limit 1
      gh run watch <run-id>              # follow it live, or:
      gh run view <run-id>                # check status once it's done
      gh run download <run-id> --name download-verify-report --dir ./download-verify-report
      cat ./download-verify-report/download-verify-report.json | jq '.[] | select(.status != "OK")'
      ```

      **Per-bug (E-H) reproduced vs. inferred — recorded honestly, not just "all tested":**

      | Bug | Status | Basis |
      |---|---|---|
      | E (download stall/incomplete) | **Reproduced** | `engine/streamer_test.go`'s `httptest`-backed tests make a real server actually stall mid-body / during the initial format-sniff / send a declared-but-undelivered `Content-Length` / etc., and drive the real `StreamDownloadWithFailover` code path against it — not simulated inputs to a mocked function. Verified failing before the stall-watchdog fix existed and passing after. |
      | F (revert does not work) | **Reproduced** | `engine/revert_linux_loopimage_test.go` builds a real GPT disk image on a real loop device and runs the real, unmodified `ExecuteDecommissionWithOptions` against it as root (`sudo -E go test ... -run TestExecuteDecommission`). Both original findings (missing swap/fstab awareness, unused `TargetPartUUID` verification) were confirmed broken this way, and the filesystem-mismatch safety-abort fix was specifically verified failing before the fix (temporarily disabled the check, re-ran under root, confirmed the wrong-type partition really got deleted) and passing after. Residual gap at the time (closed later by F14, this same slice): `findActiveESPMount`/`efibootmgr` were still hardcoded, so ESP-removal/NVRAM-removal had zero coverage — tests had to pass empty values to skip those paths entirely. |
      | G (transient_resize.sh not created) | **Mostly inferred, fix partially reproduced.** The root cause itself (neither USB mount fallback attempt was ever checked for success) was identified by **code reading**, not by actually unplugging a USB drive on real bare-metal hardware and observing the orchestrator silently write to a local directory instead — that end-to-end symptom was never reproduced live in this session. What **is** reproduced: `engine/usb_storage_mount_test.go`'s negative case (an ordinary unmounted `t.TempDir()` — structurally identical to what `usbStorageDir` becomes when both real mount attempts fail) is rejected by the new `ensureUSBStorageMounted` check, and the positive case uses a **real** tmpfs mount (not a mock) to prove a genuinely mounted filesystem is still accepted. So: the fix's logic is reproduced against a real mount/non-mount filesystem state; the original bug's full real-world trigger (an actual failed USB mount on actual hardware) is inferred from the code, not reproduced. |
      | H (ISO picker browses the wrong home) | **Reproduced, via simulation rather than a real account.** `engine/file_dialog_test.go`'s `TestGetRealUserHome_UsesLookupHomeNotGuessedPath` uses an **injectable** `userLookupFunc` to simulate a `SUDO_USER` whose real home is deliberately *not* at `/home/<name>` (the exact condition that makes the old guessed-path logic diverge from the correct looked-up path) and confirms the fixed code resolves to the looked-up path, not the guess. This exercises the real divergence mechanism end-to-end through the actual `GetRealUserHome` function, but via a simulated passwd-lookup result rather than an actually-created system user with a non-default home directory — a lighter-weight but still faithful reproduction, not pure code-reading inference. |

      Summary: **E and F are reproduced on real infrastructure** (real HTTP stalls, a real
      loop-mounted disk image). **G's root cause is inferred from code reading**, with only
      the fix's own logic reproduced against real (if synthetic) mount state. **H sits in
      between** — a real mechanism reproduced via a controlled simulation, not a fully live
      end-to-end repro.

## Phase 3: OS verification matrix (branch test/os-matrix)
- [ ] Tier 1 catalog validation — Not started
- [ ] Tier 2 Docker container pipeline — Not started
- [ ] Tier 3 QEMU boot matrix — Not started
- [ ] Tier 4 Windows/macOS runners — Not started
- [ ] OS_STATUS_MATRIX.md — Not started

**CHECKPOINT 3:** not reached

## Phase 4: Remaining audit items + open-source readiness
- [x] 20. GPG/signature verification — Done (`c35b452`). Audit 2.7: a SHA256 checksum
      alone doesn't protect against a compromised mirror serving a tampered file alongside a
      tampered checksum of its own. New `engine/signature.go`'s `VerifyDetachedSignature`
      verifies an OpenPGP detached signature (armored `.asc` or raw binary `.sig`/`.gpg` —
      format auto-detected, since distros aren't consistent about which they publish)
      against a provided ASCII-armored keyring. Uses `github.com/ProtonMail/go-crypto/openpgp`
      — deliberately NOT the standard `golang.org/x/crypto/openpgp` already in this module's
      dependency tree, since upstream has marked that package deprecated and "unsafe by
      design" and names this fork as the maintained replacement; using the deprecated one
      for a new security control would be the wrong call. This item is the verification
      *primitive* only — wiring it into the real download flow is item 28 below (split
      because no catalog entry currently has a signature URL/key to verify against; see
      that item for why, mirroring item 13's "cannot invent checksums" precedent for keys).
      Tests (`engine/signature_test.go`, real OpenPGP operations — a real keypair is
      generated per test, not mocked): valid armored signature verifies and reports the
      signer's identity; valid binary signature verifies; **the actual security property**
      — a signature valid for the original content fails against tampered content; a
      signature from a key not in the keyring (the "attacker signs their own tampered
      payload" scenario) is rejected; corrupt signature bytes and a corrupt keyring both
      fail cleanly rather than panicking or silently passing.
- [x] 21. Image-deploy rollback handler or documented limitation — Done (`5cdce6e`).
      Audit 2.12: `rollbackHandlers` had no entry at all for `"image-deploy"`, so a failed
      deploy made `journal.RollbackAll` report a generic `no rollback handler registered for
      "image-deploy"` — true, but indistinguishable from an oversight rather than the
      documented, inherent limitation it actually is (by the time image-deploy begins, the
      target partition has already been `mkfs`'d — there is no prior filesystem state left
      to restore to, full stop; the audit's own suggested fix was "document this explicitly,"
      not attempt a snapshot/restore that nothing in this tool currently supports). New
      `RollbackImageDeploy` (always returns nil — there's genuinely nothing to undo) is now
      registered for this step name, and `fail()` in `RunProvision` emits a specific
      `[NOTE]` message explaining the limitation (and that a shrunk host partition's own
      rollback is unaffected) at the moment image-deploy fails, rather than leaving a bare
      "rolled back successfully" with no context. Tests (`engine/orchestrator_test.go`):
      `RollbackImageDeploy` always succeeds regardless of input;
      `rollbackHandlers` registers a handler for `"image-deploy"`;
      **the real regression** — `journal.RollbackAll` against a journal whose only step is a
      failed `"image-deploy"` now completes with zero errors (verified failing with the exact
      pre-fix message by temporarily removing the registration and re-running, then restored).
- [x] 22. Windows XP entry removed/flagged — Done (`7d2a107`). Audit 2.13: Windows XP
      predates WIM-based installation, but its manifest entry (`winxp-pro-i386`) routed
      through the exact same `deployWindowsPayload` path as Windows 10/11, which
      unconditionally searches for `sources/install.wim`/`install.esd` — neither exists on
      XP media (pre-WIM, `i386`/`txtsetup.sif` layout). The existing `eol: true` flag was
      purely decorative: confirmed by grep that `Catalog.Filter`'s EOL-exclusion logic is
      never called by the TUI (`ui/model.go` iterates `ListCategories`/`GetEntriesByCategory`
      directly) or by headless mode — only the already-disconnected `cmd/env-check` (itself
      audit items 2.14/2.15, see item 23) calls `Filter` at all. So the entry was still fully
      selectable and would always fail late, after a ~600MB download, with a confusing
      "cannot locate install.wim or install.esd" error. Per the audit's own fixability
      note ("not fixable without a wholly different pre-WIM installation path — low value to
      build"), removed the entry from `discovery/manifests/windows.json` entirely rather than
      leaving it to fail at runtime. Updated `docs/MANIFEST_AUDIT.md` (28/28 now, was 29/29;
      re-ran `TestRealEmbeddedCatalog_AuditReport` for real to confirm the new count rather
      than hand-computing it) and `README.md`'s catalog description. Test
      (`discovery/audit_test.go`'s `TestEmbeddedCatalog_DoesNotContainWindowsXP`): asserts no
      catalog entry has this ID or distro name; verified failing by temporarily re-adding a
      synthetic XP entry and re-running, then restored.
- [x] 23. env-check opt-in + pre-flight gate — Done (`PENDING_HASH`). Audit 2.14: `cmd/env-check`
      made an unconditional live network call to `archive.ubuntu.com` as part of what's
      otherwise a local, offline-safe diagnostic — broke airgapped use, surprising for a
      "diagnostic" tool. Audit 2.15: the checks (hypervisor/firmware/disk-headroom/catalog)
      were real and working but nothing ever invoked `cmd/env-check` — a genuinely useful
      pre-flight check protecting zero actual users.
      - Extracted the composable checks into a new `preflight` package (`preflight.Run`):
        hypervisor detection, firmware mode, disk/power headroom (against a flat 25GB
        baseline margin, deliberately not tied to any specific OS size since none is chosen
        yet at this point — the real, size-specific check still runs later via
        `engine.PreflightStorageSafetyCheck` and the download-time free-space check), and
        catalog load sanity. Separate `preflight.RunLiveSuiteCheck()` wraps the live
        Ubuntu-suite scrape — never called by `Run` itself, only by `cmd/env-check`'s new
        `--live-scrape` flag (default off, satisfying 2.14).
      - `cmd/env-check` rewritten to call `preflight.Run` + the opt-in live check, same
        human-readable output as before, now also prints Warnings/Blocking sections and
        exits nonzero on a blocking issue. Also fixed a pre-existing bug found while
        touching this file: it hardcoded `safety.CheckGuards("/home/claude", ...)` — a
        leftover developer-machine path that made the disk-headroom check meaningless on
        any other machine; now takes `--target-path` (default `.`).
      - `cmd/orchestrator/main.go` now calls `preflight.Run` + a new `runPreflightGate`
        helper before either real entry point that provisions something (headless or the
        TUI) — not before `--revert`/`--dry-run`, which don't need it. A blocking issue
        refuses to start by default; new `--skip-preflight` overrides (still prints what it
        overrode, not silently). This is the actual 2.15 fix: the exact same checks that
        used to sit inert in `cmd/env-check` now gate the real entry points a user actually
        runs.
      - Tests: `preflight/preflight_test.go`'s `TestRun_SucceedsAgainstARealTempDir` runs
        the real function (real hypervisor/firmware/disk detection, no mocking) against a
        safe `t.TempDir()`. `cmd/orchestrator/preflight_test.go`: `runPreflightGate`'s three
        real decision paths — no blocking issue (no-op), a blocking issue refuses by
        default, `--skip-preflight` overrides the refusal but still surfaces the reason.
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
