# Silent-Error Audit (Phase 4 item 25)

A systemic sweep of three error-swallowing patterns across the codebase: Go's
`_ = someCmd.Run()`/discarded `exec.Command` results, and the shell idioms `|| true` and
`2>/dev/null` (both standalone and embedded inside Go-generated scripts). The question for
each instance: **if this specific operation fails, does the surrounding code still behave
correctly, or does it silently proceed as if it had succeeded?**

This is not, and cannot practically be, a full line-by-line rewrite of every instance — a
repo-wide grep found roughly **195** discarded `exec.Command`/`.Run()` results in Go source
alone, plus **52** more `|| true`/`2>/dev/null` occurrences embedded in Go-generated shell
scripts, plus dozens more in the standalone shell launchers (`autorun.sh`, `autorun.command`).
The large majority are legitimate best-effort cleanup (`umount -l` of a scratch mountpoint,
`rm` of a temp file, `sync`) where swallowing the error is the *correct* behavior, not a bug —
rewriting all of them to hard-fail would make routine, harmless cleanup failures abort real
provisioning runs for no safety benefit. This audit instead triages by actual risk and fixes
what it finds genuinely dangerous.

## What "hard-failing" correctly already looks like

`engine/bootloader.go`'s `assertDefaultCredentials` (audit 1A.3, fixed in an earlier session)
is this codebase's own reference example: it runs the account-creation script inside the
target chroot, then explicitly checks for both a non-error exit **and** a verification marker
(`ORCH_ACCOUNT_VERIFIED`) in the output before declaring success — it does not just check
`err == nil`, because a script that exits 0 without actually creating the account (an edge
case `set -e` alone doesn't fully protect against inside a sourced/chrooted context) would
otherwise be silently accepted. Every fix in this audit follows the same shape: check the
*specific* operation's result, not just "did something downstream eventually complain."

## Fixed in this pass

### `engine/backup.go` — `RestoreHostFromBackup` (disaster recovery)

**The real bug, found by actually reading the function, not just pattern-matching the
discard syntax:** every `parted`/`mkfs.vfat` call that sets up the disaster-recovery target
disk discarded its error —

```go
_ = exec.Command("parted", "-s", targetDisk, "mklabel", "gpt").Run()
_ = exec.Command("parted", "-s", targetDisk, "mkpart", "ESP", "fat32", "1MiB", "1025MiB").Run()
_ = exec.Command("parted", "-s", targetDisk, "set", "1", "esp", "on").Run()
_ = exec.Command("parted", "-s", targetDisk, "mkpart", "primary", "ext4", "1025MiB", "100%").Run()
...
_ = exec.Command("mkfs.vfat", "-F32", "-n", "BOOT", espPart).Run()
```

If `mklabel gpt` silently failed, every call after it would operate against a disk that was
never actually relabeled. A **total** failure (no partition created at all) does eventually
surface — a few lines later, `mkfs.ext4` on the root partition already correctly checks its
error, and would fail on a nonexistent device. But a **partial** failure (the ESP-specific
`mkpart`/`mkfs.vfat` failing while the root partition succeeds) would not: the ESP mount
further down (`_ = exec.Command("mount", espPart, efiDir).Run()`, also discarded) would
silently create an empty directory instead of a real mount, extraction would proceed into it
anyway, and the function would return success having produced an **unbootable UEFI system
with no real ESP content** — the exact "looks fine until you actually try to boot it" failure
mode this whole audit exists to catch.

**Fix:** every call that changes the disk's actual structure (`mklabel`, `mkpart`, `set ...
on`, `mkfs.vfat`, and the ESP `mount`) now checks its result and returns a specific,
immediate error naming which step failed. `partprobe` (a kernel re-read nudge, not a state
change) is left as best-effort, consistent with how every other partitioning function in this
codebase already treats it.

**Test (`engine/backup_test.go`'s `TestRestoreHostFromBackup_AbortsOnPartitionTableFailure`):**
points `targetDisk` at a nonexistent device path, which makes parted's very first call fail
immediately and harmlessly (no loop device or root needed — parted errors out before touching
anything real). Verified failing before the fix with a revealing symptom: the error that
actually surfaced was `mkfs.ext4 failed: ... does not exist and no size was specified` —
i.e., exactly the predicted bug, where the REAL failure (the discarded `mklabel`) was masked
and a much later, confusing error was reported instead. Passes with a specific "creating GPT
partition table" error after the fix.

## Surveyed and deliberately left as-is

- **`engine/usb_manager.go`'s `PrepareOrchestratorUSB`/`FactoryResetUSB`**: initially flagged
  by the same grep pattern, but on inspection their structurally-critical calls (`mklabel`,
  `mkpart`, `mkfs.*`) already check their errors correctly — only the ESP/boot **flag-setting**
  (`parted ... set 2 esp on`) and `partprobe`/`udevadm settle` (kernel re-read nudges) are
  discarded. Left as-is: many UEFI firmware implementations can still boot a FAT32 ESP even
  without the `esp` flag explicitly set (it's advisory on several implementations, required
  on others), so hard-failing here would be stricter than the actual risk warrants — and the
  partition itself, which is what actually matters, is already verified.
- **`engine/orchestrator.go`'s USB-storage mount fallbacks** (`mount storageDev usbStorageDir`
  / `mount -L ORCH_STORAGE usbStorageDir`, both discarded): this is bug G's own fix (see
  `PROGRESS.md`) — multiple fallback mount *attempts* are tried in sequence, each discarded
  individually, but the actual outcome is explicitly re-verified afterward via
  `ensureUSBStorageMounted` before anything proceeds. Discarding each individual attempt while
  checking the final state is the correct pattern here, not a bug.
- **`engine/autonomous_installer_script.go`'s generated shell (26 `|| true` occurrences)**:
  surveyed line by line. Most are genuine best-effort cleanup (unmounts, `rm` of the
  password-hash file, calamares removal) where failure is inherently fine. One borderline
  case — `parted ... mkpart primary ext4 50%% 100%% || true` when creating the target
  partition — discards its own result, but the very next command (`mkfs.ext4 ... "$TARGET_PART"`,
  which has no `|| true`) will fail under the script's own `set -euo pipefail` if the
  partition was never created, so a real failure here still aborts the script rather than
  succeeding falsely — just with a less precise error location than ideal. Lower severity
  than the backup.go case (that one could report **success**; this one still fails, just
  less legibly) — not fixed in this pass, flagged here rather than silently ignored.
- **Chroot bind-mounts** (`mount --bind /dev|/proc|/sys ...`, discarded in several files
  including `engine/orchestrator.go`, `engine/rootfs.go`, `engine/revert_linux.go`): a
  widespread, consistent pattern across this whole codebase, not unique to one file. A failed
  bind-mount can make some chrooted operations (anything needing `/dev/null`, `/proc/self`,
  etc.) behave oddly, but most of what actually runs inside these chroots (account creation,
  `update-grub`, package installs) tolerates a missing bind-mount degraded rather than
  catastrophically. Changing this pattern everywhere it appears is a much larger, cross-file
  change with real risk of introducing new failures in currently-working paths; out of scope
  for this pass.
- **macOS/Windows-specific files** (`engine/revert_darwin.go`, `engine/engine_windows.go`,
  `engine/revert_windows.go`, `engine/bcd_qemu.go`): surveyed for the same pattern but not
  deeply triaged or fixed here — this session runs on Linux and cannot exercise or verify a
  fix on these platforms the way `TestRestoreHostFromBackup_AbortsOnPartitionTableFailure`
  was verified for real against the Linux path above. Flagged as a known gap rather than
  guessed at blind.

## Not re-litigated here

Several earlier items in this same remediation effort already fixed instances of this exact
pattern in their own specific contexts: bug G (`ensureUSBStorageMounted`), bug F
(`findActiveESPMount`/`efibootmgr` injectability, F14), and item 21
(`RollbackImageDeploy`/the "no rollback handler" case) are all, in effect, earlier rounds of
this same silent-error audit applied to one function at a time. This document covers what
was found in a broader, dedicated sweep *after* those were already fixed.
