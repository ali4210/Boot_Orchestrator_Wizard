# Boot Orchestrator Wizard — Engineering Audit Report

**Scope:** Full repository audit (Linux/macOS/Windows launchers, Go engine, discovery manifests, safety layer). Static analysis only; no destructive operations were performed on this machine. A sandboxed QEMU/OVMF test was performed against throwaway disk images only.

**Audit basis:** `git log` shows 5 commits (`f62f026` Initial commit → `845730e` updates → `341e848` chore: save current state prior to git history purge → `3fab458` test → `a7b840e` test); commit messages carry no signal. The working tree has a large uncommitted diff (~3,000 lines across `engine/`, `ui/`, `safety/`) plus several new untracked files (`engine/bcd_qemu.go`, `engine/payload_normalizer.go`, `ui/disk_linux.go`, `ui/disk_windows.go`, `discovery/manifests/autounattend.xml`) and a leftover `orchestrator_journal.json` from a real prior run. That journal file is the single most important artifact in this audit: it is direct forensic evidence of the exact failure the user experienced.

---

## 1. Executive Summary

**Verdict: partly broken, with a redesign needed in two specific subsystems (disk handling and dual-boot bootloader orchestration) and a hardening pass needed before any public/open-source release.**

The project is not vaporware. It compiles cleanly (`go build ./...`, `go vet ./...` both exit 0), the architecture (journal-based rollback, per-OS engine files, a discovery/manifest catalog, a bubbletea TUI) is a reasonable shape for this problem, and large parts of the pipeline (ISO handling, checksum verification, live-rootfs extraction, Windows WIM deployment, journal/rollback bookkeeping) are genuinely implemented, not stubbed.

However, two concrete, high-confidence bugs explain "I could not get it working in practice":

1. **The partition-shrink step reliably fails** on real Linux targets because `resize2fs` is called without ensuring `/etc/mtab` exists (`engine/partition.go:303`). This is not hypothetical — it is the exact error captured in the user's own `orchestrator_journal.json` from a real run against `/dev/sda` on 2026-09-15.
2. **Even when every other step succeeds, a failed Linux dual-boot bootloader configuration is reported to the user as success.** `engine/orchestrator.go`'s generic (non-macOS, non-Windows) branch calls `journal.Fail(bootStep, err)` on error but — unlike the macOS and Windows branches a few lines above it — never `return`s. Execution falls through to an unconditional `journal.Commit(bootStep)` and a `"dual-boot bootloader synchronized"` success message. A user running the common case (installing a Linux OS) can get a tool that reports total success while leaving the machine unable to boot the new OS.

On top of these, the tool currently has **no dry-run, simulation, or test-target mode of any kind** — not in the launchers, not in the compiled binary (`cmd/orchestrator --help` shows exactly one flag, `-iso`), and no way to redirect disk-write operations at a loop device or image file instead of a real `lsblk`-enumerated device. This is both a testability gap for the maintainer and the reason this audit could not dynamically exercise the real disk/bootloader code paths.

There is also a release-blocking security/product issue independent of the above: the tool hardcodes a user account `saleem` / password `saleem` and sets the root password to `saleem` on **every** Linux machine it provisions (`engine/orchestrator.go:644-654`, `1009-1020`). This must not ship to any other user as-is.

The tool is not "fundamentally sound and almost done," and it is not "needs a ground-up rewrite" either. It sits in between: the orchestration skeleton, rollback journal, and media-handling layers are solid engineering and worth keeping; the disk-partitioning and bootloader-configuration layers need real redesign (LVM/LUKS/RAID awareness, proper mount-table handling, correct error propagation); and the credential/identity layer needs to be ripped out and replaced with something per-user.

---

## 2. Root Causes

Each issue: **file:line**, snippet, impact, severity, fixability.

### 2.1 Bootloader failure silently reported as success — **Critical**
**`engine/orchestrator.go:1246-1256`**
```go
} else {
    onUpdate(StepUpdate{StepName: "configuring autonomous dual-boot & updating host bootloader...", Done: false})
    if err := AutoConfigureDualBoot(req.TargetPartition, grubDefaultPath, func(msg string) {
        onUpdate(StepUpdate{StepName: msg, Done: false})
    }); err != nil {
        journal.Fail(bootStep, err)
    }
}
journal.Commit(bootStep)
onUpdate(StepUpdate{StepName: "dual-boot bootloader synchronized", Done: true})
```
The `isMacOS` and `isWindows` branches immediately above both `return fail("bootloader-configure", err)` on error. The generic Linux `else` branch (the path taken for the large majority of manifest entries) only marks the journal step failed, then falls through to `journal.Commit(bootStep)`, which overwrites that failed status, and then emits a "synchronized" success update. The function goes on to return `ProvisionResult{Success: true}`.
**Impact:** A failed GRUB/dual-boot configuration for a Linux target is reported to the user as a successful install. This is the single most damaging bug for user trust — it produces exactly the "I did everything right and it still didn't boot" experience.
**Fixability:** Fixable now — one-line fix: `return fail("bootloader-configure", err)` inside the `else` branch, matching the other two branches.

### 2.2 `resize2fs` fails without `/etc/mtab` — **Critical, confirmed by real-world evidence**
**`engine/partition.go:303-304`**
```go
if outResize, errResize := exec.Command("resize2fs", part1Dev, fmt.Sprintf("%dG", newPart1GiB)).CombinedOutput(); errResize != nil {
    return nil, fmt.Errorf("resize2fs failed on %s: %w (%s)", part1Dev, errResize, string(outResize))
}
```
**Evidence — `orchestrator_journal.json`, step `partition-shrink-003`, real run on 2026-09-15 against `/dev/sda`:**
```
"error": "resize2fs failed on /dev/sda1: exit status 1 (resize2fs 1.47.4 (6-Mar-2025)
ext2fs_check_mount_point: Can't check if filesystem is mounted due to missing mtab file
while determining whether /dev/sda1 is mounted.\n)"
```
`resize2fs` needs `/etc/mtab` to determine whether a target is mounted; on systems where `/etc/mtab` is not a symlink to `/proc/self/mounts` (common on minimal/live environments, and apparently on the user's own test environment), this check throws rather than falling back, and the partition-shrink step — the very first disk-modifying step in the whole pipeline — fails every time.
**Impact:** Blocks the entire provisioning flow at the first real disk operation. This is very likely the literal, direct cause of "I could not get it working in practice."
**Fixability:** Fixable now — ensure `/etc/mtab` exists/symlinks to `/proc/self/mounts` before calling `resize2fs` (or pass `-f` to force past the check), and treat this as a pre-flight environment check rather than a late failure.

### 2.3 Hardcoded identical username/password on every provisioned machine — **Critical (security/product)**
**`engine/orchestrator.go:644, 651-654, 1009-1010, 1017-1020`**
```go
_ = os.WriteFile(filepath.Join(sudoersDir, "saleem"), []byte("saleem ALL=(ALL:ALL) ALL\n"), 0440)
...
echo 'root:saleem' | chpasswd
id -u saleem >/dev/null 2>&1 || useradd -m -s /bin/bash -G sudo saleem
echo 'saleem:saleem' | chpasswd
```
Repeated nearly verbatim at line ~1009-1020 for the bare-metal live-root path.
**Impact:** Every machine this tool provisions gets the same username and the same trivial password for both that user and root, regardless of who is running the tool. This is a weak-credential vulnerability by itself, and it also hardcodes the original developer's own username into a tool meant to be used by anyone — a correctness bug for "any machine," not just a security one.
**Fixability:** Fixable now — prompt for a username/password in the TUI (or generate a random password and display it once), remove the hardcoded string entirely before any public use.

### 2.4 No dry-run / simulation / test-target mode exists anywhere — **Critical for safety & testability**
Confirmed by exhaustive grep across `.go`/`.sh`/`.ps1`/`.bat`/`.cmd`/`.command` for `dry.?run`/`simulate`/`DRY_RUN`: zero hits. `cmd/orchestrator/main.go` parses exactly one flag, `-iso` (source media path only — there is no equivalent flag for the destination). `engine/usb_manager.go:111` and `engine/partition.go:624` call `exec.Command("lsblk", ...)` unconditionally against the live system with no injection point for a loop device or image file.
**Impact:** Nobody — not the maintainer, not a contributor, not this audit — can exercise the real partition/bootloader/deploy code paths without pointing the tool at a real disk. This is almost certainly also why the user's prior run above was against `/dev/sda` on a real machine rather than a test VM.
**Fixability:** Fixable now, moderate effort — thread a `--target-image <file>` / `--dry-run` flag through `engine/partition.go`, `engine/usb_manager.go`, and the bootloader-install call sites so `lsblk`/`parted`/`resize2fs`/`grub-install` can be redirected at a loop-mounted image file. This is the highest-leverage single change for both safety and development velocity.

### 2.5 No LVM, LUKS, RAID, or BitLocker detection or handling — **High, fixable with redesign**
Grep across `engine/` and `safety/` for `lvm|luks|cryptsetup|mdadm|bitlocker` returns nothing. `engine/partition.go`'s shrink/resize logic assumes a plain ext4 (or NTFS, for Windows) partition directly on the disk.
**Impact:** Any Fedora/openSUSE/RHEL-family default install (LVM-on-LUKS is the installer default on several of these), or any encrypted Windows (BitLocker, on by default on most OEM Windows 11 machines) or Linux (LUKS) target will either fail opaquely or — worse — have its partition table resized/modified by `partition.go` without understanding the real on-disk structure, risking data loss. This is the scenario the safety rules in this audit were written to guard against, and the tool has no internal guard for it either.
**Impact detail:** `safety/disk_guard*.go` checks free space and power state, not volume-manager/encryption state.
**Fixability:** Fixable with redesign — needs real LVM physical-volume/volume-group introspection (`lvs`/`pvs`) and a LUKS/BitLocker detection+abort path before any write. Not a one-line fix; this is structural.

### 2.6 x86_64 hardcoded throughout the bootloader/media layer despite ARM64 catalog entries — **High**
**Evidence:** `discovery/catalog.go:49-50` defines `ArchAMD64` and `ArchARM64` constants (so the catalog/manifest layer believes it supports both), but:
- `engine/usb_manager.go:282,287,313` — `/usr/lib/grub/x86_64-efi` hardcoded
- `engine/backup.go:516` — `grub-install --target=x86_64-efi` hardcoded
- `engine/bootloader.go:405` — `apt-get install linux-image-amd64` hardcoded
- `engine/bcd_qemu.go` — shells out to `qemu-system-x86_64` specifically
- `autorun.ps1` always sets `GOARCH=amd64` with no ARM64 Windows branch (unlike `autorun.command`, which does branch on Apple Silicon vs Intel)
**Impact:** Selecting any ARM64 manifest entry produces either an outright failure or, worse, an attempt to install amd64 GRUB/kernel modules on an ARM64 target. "Any machine" is not accurate for non-x86_64 hardware today.
**Fixability:** Fixable with redesign — needs per-arch branching threaded through every hardcoded `x86_64-efi`/`amd64` string. Not inherently impossible (GRUB and Debian/Ubuntu packages do support arm64), just not implemented.

### 2.7 No GPG/signature verification of downloaded ISOs — **High**
SHA-256 checksum verification exists in the ISO download path, but there is no GPG/signature check against the distro's published signing key.
**Impact:** A checksum alone does not protect against a compromised mirror serving a tampered ISO with a matching tampered checksum file from the same mirror. This matters more for a tool explicitly designed to auto-download and install OS media unattended.
**Fixability:** Fixable now — most major distros publish detached GPG signatures (`SHA256SUMS.gpg`) alongside checksums; verify against a bundled or fetched keyring before proceeding.

### 2.8 Broken Go-version bootstrap on Windows — **High**
**`autorun.ps1:126`**
```powershell
# hardcoded: https://go.dev/dl/go1.22.6.windows-amd64.zip
```
but `go.mod` requires `go 1.26.0`.
**Impact:** On a clean Windows machine with no Go preinstalled, the launcher's own fallback downloads a Go toolchain too old to build the module (Go refuses to build when the module's `go` directive exceeds the installed toolchain version). The auto-bootstrap path is self-defeating.
**Fixability:** Fixable now — bump the pinned URL/version to match (or exceed) `go.mod`'s directive, or rely on Go's own toolchain auto-download mechanism instead of hand-rolling it.

### 2.9 Unconfirmed root-level package installation before the user sees anything — **High**
**`autorun.sh:63-73`**
Silently runs `apt-get install`/`pacman -Sy`/`dnf install`/`zypper install` (distro-detected) as root with no confirmation prompt, before the TUI has even launched.
**Impact:** Mutates the host's installed packages without the user's explicit, in-the-moment consent — surprising behavior for a tool a new open-source user is trying out for the first time.
**Fixability:** Fixable now — gate behind a y/N prompt, or require an explicit `--yes`/`-f` flag (the `-f` flag already exists for "force clean rebuild," so this could be folded into existing flag semantics).

### 2.10 Risky unguarded `rm -rf` pattern on launcher-derived path — **Medium**
**`autorun.sh:108`**, mirrored in **`autorun.command:73`**
```bash
rm -rf "$SCRIPT_DIR/bin"
```
flagged by shellcheck as the SC2115 pattern. If `SCRIPT_DIR` (or `DIR` in `autorun.command`) were ever empty — e.g. an unusual invocation via a broken symlink or edge-case `BASH_SOURCE` resolution — this becomes `rm -rf /bin`. The preceding `cd`/`pwd` guard makes this unlikely in normal use, but it's exactly the fragile pattern shellcheck exists to catch, and the blast radius if it ever fires is severe.
**Impact:** Low likelihood, catastrophic-if-triggered.
**Fixability:** Fixable now — `rm -rf "${SCRIPT_DIR:?}/bin"` (fails loudly instead of expanding to `/bin` if the variable is ever unset/empty).

### 2.11 `autorun.sh` mutates `go.mod`/`go.sum` at every launch — **Medium**
**`autorun.sh:150`** runs `go get golang.org/x/crypto/ssh` (plus `go mod tidy`) on every launch where a binary is missing.
**Impact:** Can silently rewrite dependency lockfiles on a pinned/offline/CI machine, and makes builds non-reproducible across machines/time if the dependency has moved.
**Fixability:** Fixable now — vendor dependencies or rely on the already-committed `go.sum`; only run `go mod download`, never `go get`/`go mod tidy`, from the launcher.

### 2.12 `image-deploy` journal step has no registered rollback handler — **Medium**
If image deployment fails after a successful partition-shrink, rollback only undoes the shrink, not the `mkfs` that already wiped the new partition's prior contents.
**Impact:** Partial, undocumented rollback coverage; a failure mid-deploy can leave a partition freshly formatted and empty rather than restored, with no warning to the user that this is what "rollback" actually means in that case.
**Fixability:** Fixable now — document this explicitly in the rollback step's user-facing message at minimum; ideally snapshot/backup the partition's prior superblock state before `mkfs` if full rollback is wanted.

### 2.13 Windows XP manifest entry has no viable install path — **Medium**
Windows XP predates WIM-based installation; the manifest entry routes through the same `deployWindowsPayload`/WIM path as Windows 10/11, which cannot work for XP media.
**Impact:** Selecting this entry will fail, with no earlier warning that it's unsupported.
**Fixability:** Not fixable without a wholly different (pre-WIM, `txtsetup.sif`-era) installation path — low value to build; recommend removing the manifest entry instead and documenting XP as explicitly unsupported.

### 2.14 `cmd/env-check` makes an uncontrolled live network call — **Low**
The standalone diagnostic binary calls out to `archive.ubuntu.com` to scrape live suite data as part of what's otherwise a local hardware/firmware check.
**Impact:** Surprising side effect for a "diagnostic" tool; breaks offline/airgapped use of what should be a pure read-only check.
**Fixability:** Fixable now — make the live scrape opt-in or cache/bundle a static suite list as the default.

### 2.15 `cmd/env-check` is disconnected from the actual provisioning flow — **Low**
It's a real, working, read-only diagnostic (hypervisor/firmware/disk-space/catalog checks) but no launcher or the main orchestrator ever invokes it.
**Impact:** A genuinely useful pre-flight check exists in the repo but provides zero protection to an actual user, because nothing wires it into the real flow.
**Fixability:** Fixable now — call it automatically before the TUI launches, and gate continuation on its results.

### 2.16 Windows launcher duplication / dead check — **Low**
`autorun.bat` and `autorun.cmd` are ~95% identical, both delegating to `autorun.ps1`; `.bat`'s `net session` admin pre-check result is logged but never acted on (real elevation happens unconditionally inside `autorun.ps1` anyway).
**Impact:** Maintenance burden, confusing for contributors (which one is "the" Windows entry point?), no functional harm.
**Fixability:** Fixable now — delete one of `.bat`/`.cmd`, keep a single Windows entry point, remove the dead check.

### 2.17 Orphaned maintainer scripts in repo root — **Low**
`audit_and_fix_all.py`, `audit_non_linux.py`, `fix_linux_ids.py`, `generate_perfect_linux.py`, `test_manifest_mirrors.py`, `test_win_mac_mirrors.py`, `update_macos_manifest.py`, `update_manifest.py`, `update_windows_manifest.py` — confirmed via grep: zero references from any launcher or Go source. One-off scripts used to regenerate `discovery/manifests/*.json` by hand at some point, left in the repo root.
**Impact:** Clutters the root directory and confuses a new contributor about what's actually part of the shipped tool. The names themselves (`audit_and_fix_all.py`, `fix_linux_ids.py`) suggest prior ad-hoc patching sessions rather than a maintained tool.
**Fixability:** Fixable now — move to `tools/maintenance/` or delete if no longer needed, document in a CONTRIBUTING guide if kept.

### 2.18 No TPM or Linux-side Secure Boot state detection — **Medium**
Firmware detection covers UEFI-vs-BIOS and Windows-side Secure Boot (`Confirm-SecureBootUEFI`), but nothing checks TPM presence/state or Secure Boot state from the Linux side (`mokutil`/`sbctl`-equivalent).
**Impact:** Dual-boot installs onto Secure-Boot-enforced machines, or OSes/kernels requiring TPM (Windows 11's own install requirement, notably), aren't pre-flighted — failures surface late and opaquely instead of as an early, clear warning.
**Fixability:** Fixable now — both checks are a handful of shell-outs (`mokutil --sb-state`, reading `/sys/class/tpm/tpm0`).

### 2.19 Zero automated test coverage on the actual logic — **Medium**
`go test ./...`: only `boot-orchestrator/ui` has any tests. `engine`, `safety`, `discovery`, `hypervisor`, `cmd/env-check`, `cmd/orchestrator` all report `[no test files]`. The partition/bootloader/journal logic that this report flags as buggy has zero unit-test coverage.
**Impact:** Both bugs in 2.1 and 2.2 above are the kind that a basic unit test (mock `exec.Command`, assert the Linux branch returns on error; assert `resize2fs` failure propagates) would have caught before release.
**Fixability:** Fixable now — not a redesign, just missing engineering discipline.

---

## 3. Fixability Summary Table

| # | Issue | Severity | Fixability |
|---|---|---|---|
| 2.1 | Bootloader failure reported as success | Critical | Fixable now |
| 2.2 | `resize2fs`/mtab crash blocks all partitioning | Critical | Fixable now |
| 2.3 | Hardcoded `saleem`/`saleem` credentials | Critical | Fixable now |
| 2.4 | No dry-run/test-target mode | Critical | Fixable now (moderate effort) |
| 2.5 | No LVM/LUKS/RAID/BitLocker handling | High | Fixable with redesign |
| 2.6 | x86_64 hardcoded despite ARM64 catalog entries | High | Fixable with redesign |
| 2.7 | No GPG/signature verification of ISOs | High | Fixable now |
| 2.8 | Windows Go-bootstrap version mismatch | High | Fixable now |
| 2.9 | Unconfirmed root package installs | High | Fixable now |
| 2.10 | Unguarded `rm -rf "$SCRIPT_DIR/bin"` | Medium | Fixable now |
| 2.11 | Launcher mutates go.mod/go.sum at runtime | Medium | Fixable now |
| 2.12 | No rollback handler for image-deploy | Medium | Fixable now |
| 2.13 | Windows XP manifest entry non-viable | Medium | Not fixable / remove entry |
| 2.14 | env-check makes live network call | Low | Fixable now |
| 2.15 | env-check disconnected from real flow | Low | Fixable now |
| 2.16 | Windows launcher duplication | Low | Fixable now |
| 2.17 | Orphaned maintainer scripts in repo root | Low | Fixable now |
| 2.18 | No TPM/Linux Secure Boot detection | Medium | Fixable now |
| 2.19 | Zero test coverage on engine logic | Medium | Fixable now |

**Reality check on "any OS, any machine":** No tool can honestly guarantee 100% reliability across every OS, firmware, and architecture combination — vendor-specific quirks (OEM RAID/Optane configs, exotic Secure Boot implementations, ARM SBC boot flows, Apple Silicon's non-standard boot chain) will always have long tails of edge cases. A credible scope for this project is **x86_64 UEFI machines, GPT, plain (non-LVM, non-encrypted) partitions, for a curated list of mainstream Linux distros and Windows 10/11** as the "fully supported" tier, with everything else (BIOS/legacy, LVM/LUKS/BitLocker, ARM64, macOS beyond recovery-boot) explicitly labeled experimental/unsupported until built out. That's achievable; "any machine" as literal marketing copy is not.

---

## 4. Supported-OS / Firmware / Architecture Matrix

| Target | Manifest entries | Real code path | Status |
|---|---|---|---|
| Debian/Ubuntu/Parrot/Kali (live/casper) | ~20 | `extractLiveRootFS` + `AutoConfigureDualBoot` | **Partial** — real implementation, undermined by bugs 2.1 & 2.2 |
| Arch Linux (airootfs.sfs) | few | implemented | **Untested** — real code, never exercised end-to-end here |
| Windows 10/11/Server | 3 | WIM deploy + BCD-via-QEMU | **Partial** — real but fragile (depends on qemu/OVMF at runtime, 2.3 credential issue applies if dual-boot path touches the Linux side) |
| Windows XP | 1 | same WIM path (wrong era) | **Not viable** — placeholder/broken, recommend removal |
| macOS Sonoma/Sequoia | 2 | 7z BaseSystem unpack + OpenCore | **Partial** — recovery-boot only, not a full OS install |
| LVM-on-Linux distros (Fedora/openSUSE defaults) | n/a | none | **Unsupported** — no detection, risk of data loss |
| LUKS/BitLocker-encrypted targets | n/a | none | **Unsupported** — no detection, risk of data loss |
| ARM64 (any OS) | some catalog entries | none (x86_64 hardcoded throughout) | **Placeholder only** |
| UEFI, GPT, x86_64, unencrypted | — | — | **Best-supported tier**, still blocked today by 2.1/2.2 |
| Legacy BIOS/MBR | — | detected (`cmd/env-check` correctly identified this machine as Legacy BIOS/MBR) | **Partial** — detected, not clear bootloader install logic fully branches for it everywhere |
| Secure Boot / TPM-enforced machines | — | Windows-side SB check only | **Untested/partial** |

---

## 5. Prioritized Fix Roadmap

**Phase 0 — Unblock testing (do this first, enables everything else to be verified safely):**
1. Add a `--target-image <file>` / `--dry-run` flag threaded through `partition.go`, `usb_manager.go`, and bootloader call sites (2.4). *Effort: 1-2 days.*
2. Add basic unit tests around the journal/rollback and bootloader-branch logic using mocked `exec.Command` (2.19), specifically a regression test for 2.1. *Effort: 1 day.*

**Phase 1 — Fix the two confirmed blocking bugs:**
3. Fix the Linux bootloader branch to `return` on error like the other two branches (2.1). *Effort: trivial, < 1 hour, but write the regression test from step 2 first.*
4. Fix `resize2fs` mtab handling (2.2). *Effort: trivial, < 1 hour.*
5. Remove hardcoded `saleem` credentials; add a credential prompt or generated-password flow (2.3). *Effort: half a day.*

**Phase 2 — Harden the common path:**
6. Add TPM/Secure Boot detection pre-flight checks with clear user-facing warnings (2.18). *Effort: 1 day.*
7. Add GPG signature verification for ISO downloads (2.7). *Effort: 1-2 days (keyring management).*
8. Fix Windows Go-bootstrap version (2.8), confirmation prompt before package installs (2.9), guard the `rm -rf` (2.10), stop runtime go.mod mutation (2.11). *Effort: combined, 1 day.*

**Phase 3 — Redesign for broader hardware coverage (bigger effort, do after Phase 0-2 ships something stable):**
9. LVM/LUKS/BitLocker detection with a hard abort (not a silent corrupt) when detected and unhandled (2.5). *Effort: 1-2 weeks for detection + safe-abort; full support for resizing LVM volumes is a larger project.*
10. Real per-architecture branching (arm64 GRUB targets, kernel packages, BCD path) instead of hardcoded x86_64 strings (2.6). *Effort: 1-2 weeks.*

**Phase 4 — Cleanup for release:**
11. Wire `cmd/env-check` into the real flow as a mandatory pre-flight gate (2.15), make its network call opt-in (2.14).
12. Remove/relocate orphaned root Python scripts (2.17), de-duplicate Windows launchers (2.16), remove the non-viable Windows XP manifest entry (2.13), document partial rollback semantics for image-deploy (2.12).

---

## 6. Missing Pieces for Open-Source Readiness

- **No LICENSE file** at all — the repo cannot legally be treated as open source by anyone until one is added. This is the single highest-priority non-code item.
- **No CONTRIBUTING guide** — no documented build/test process, coding conventions, or PR expectations.
- **No CI** — no `.github/workflows` or equivalent found; `go build`/`go vet`/`go test` aren't even run automatically today, so regressions like 2.1 can land silently.
- **Near-zero test coverage** — see 2.19; needed both for CI to be meaningful and for safe refactoring of the disk/bootloader logic in Phase 3 above.
- **No dry-run/safety-first default** — see 2.4. For a tool whose job is to write to disks and bootloaders, this is also an open-source trust issue: a new contributor or user has no safe way to try it before trusting it with a real machine.
- **No explicit safety warnings in the README** — a tool that repartitions disks and reconfigures bootloaders should lead its README with a clear, prominent warning about data-loss risk, backup recommendations, and the current lack of LVM/encryption support, not bury it.
- **Repo-root clutter** — orphaned maintainer scripts (2.17) and a stray `orchestrator_journal.json` from a real run (containing a real local device path and timestamps) are currently tracked/untracked in the working tree; the journal file in particular should never be committed (it's debug/user-state output, and this specific one is forensic evidence of a real run against real hardware).
- **go.mod module path / versioning** — worth a sanity check before a public release that the module path makes sense for an external `go get`, independent of this audit's scope.

---

## 7. Remediation Progress (added after the fact — item 39 of the follow-up plan)

This section was added during a later remediation pass on branch `fix/audit-phase-0-1`
(and, for Phase 3, `test/os-matrix`). **It is a cross-reference, not a replacement**:
`PROGRESS.md` is the authoritative, item-by-item record (every "Done" below has a real
commit hash and test there — follow the link, don't take this table's word alone).

| Finding | Severity | Status | Where |
|---|---|---|---|
| 2.1 Bootloader failure silently reported as success | Critical | **Fixed** | Prior session, `f7ddc19` |
| 2.2 `resize2fs` fails without `/etc/mtab` | Critical | **Fixed** | Prior session, `82b00ed` |
| 2.3 Hardcoded identical username/password | Critical (security/product) | **Fixed** — but see note | `PROGRESS.md` Phase 1 + item 26 |
| 2.4 No dry-run/simulation mode | Critical for safety & testability | **Open — honestly, not fixed.** Real interception infrastructure built and proven against one batch, but running the real flow under it would still be misleading (see below) | `PROGRESS.md` items 31-34 |
| 2.5 No LVM/LUKS/RAID/BitLocker detection | High | **Fixed** | `PROGRESS.md` Phase 1 item 5, `3f21fec` |
| 2.6 x86_64 hardcoded despite ARM64 catalog entries | High | **Partially fixed** — see note | `PROGRESS.md` items 35-37, `a7e8b64` |
| 2.7 No GPG/signature verification | High | **Mechanism fixed; data gap remains** — see note | `PROGRESS.md` items 20/28 |
| 2.8 Broken Go-version bootstrap on Windows | High | **Fixed** | Prior session, `50113e5` |
| 2.9 Unconfirmed root-level package installation | High | **Fixed** | Prior session, `bd5f6d7` |
| 2.10 Risky unguarded `rm -rf` | Medium | **Fixed** | Prior session, `bd5f6d7` |
| 2.11 `autorun.sh` mutates `go.mod`/`go.sum` | Medium | **Fixed** | Prior session, `bd5f6d7` |
| 2.12 `image-deploy` has no rollback handler | Medium | **Fixed** (documented limitation, not a silent gap) | `PROGRESS.md` item 21, `5cdce6e` |
| 2.13 Windows XP has no viable install path | Medium | **Fixed** (entry removed) | `PROGRESS.md` item 22, `7d2a107` |
| 2.14 `cmd/env-check`'s uncontrolled network call | Low | **Fixed** (opt-in) | `PROGRESS.md` item 23, `8b629ba` |
| 2.15 `cmd/env-check` disconnected from the real flow | Low | **Fixed** (wired into a real pre-flight gate) | `PROGRESS.md` item 23, `8b629ba` |
| 2.16 Windows launcher duplication | Low | **Fixed** | `PROGRESS.md` item 24, `5b9d316`/`e4debf4` |
| 2.17 Orphaned maintainer scripts | Low | **Fixed** (moved to `tools/maintenance/`) | `PROGRESS.md` item 24, `5b9d316` |
| 2.18 No TPM/Secure Boot detection | Medium | **Fixed** | `PROGRESS.md` Phase 1 item 5, `3f21fec` |
| 2.19 Zero automated test coverage | Medium | **Fixed in spirit, not literally exhaustive** — see note | Every item in `PROGRESS.md` |

**Notes on the ones that aren't simply "Fixed":**

- **2.3** was fixed on its original code path, but item 26 (this later pass) found the
  *exact same bug class* — hardcoded `useradd -G sudo` with no `wheel`-group detection or
  verification — in a second, parallel account-creation code path
  (`extractLiveRootFS`/`engine/orchestrator.go`) that the original 2.3 fix never touched.
  Worth remembering as a pattern: fixing one instance of a bug class doesn't mean every
  instance is fixed; grep for the shape of the bug, not just the one call site a report
  named.
- **2.4** remains genuinely open. Phase 5 built real, tested interception infrastructure
  (`engine.Runner`/`engine.FS`/`SetDryRun`) and proved it against one real batch — but
  `RunProvision`'s actual call sites interleave covered and uncovered operations inside the
  same functions, so running the real flow under simulation today would intercept only part
  of what happens and let the rest execute for real. That's worse than refusing, so
  `--dry-run` still refuses to start. See `docs/dryrun-inventory.md`/`docs/ROADMAP.md` for
  what's left before this can honestly close.
- **2.6** is partially fixed: the one confirmed-broken case found this pass
  (`engine/usb_manager.go`'s autonomous USB-staging bootloader, which would have produced a
  completely unbootable stick on real arm64 host hardware) is fixed and tested. Other
  x86_64-hardcoded sites (`engine/bcd_qemu.go`'s Windows-BCD-via-QEMU helper, in particular)
  were explicitly not audited this pass — see `docs/ARCH_SUPPORT.md`.
- **2.7**'s verification *mechanism* (OpenPGP signature checking, wired into the real
  download flow) is real, tested, and will work the moment a catalog entry has real
  `SignatureURL`/`SigningKeyURL` values — but no entry has them yet, for the same reason no
  entry has a SHA256 checksum yet (see `docs/MANIFEST_AUDIT.md`): inventing either would
  mean fabricating a trust relationship nobody actually verified.
- **2.19** went from "zero" to hundreds of tests across `engine`/`ui`/`safety`/`discovery`/
  `preflight`, including real loop-device disk-image tests, real httptest-server tests, and
  real cryptographic (OpenPGP) tests — a different situation in kind, not just degree. It is
  not literally exhaustive (`docs/dryrun-inventory.md`'s ~500-call-site inventory is itself
  proof some call sites still have no dedicated test), so "fixed in spirit" rather than
  "every line is covered."

Section 6's "Missing Pieces for Open-Source Readiness" list: LICENSE (`PROGRESS.md` item
29, MIT, user asked first), CONTRIBUTING.md (item 29), CI (item 29 — `go build`/`vet`/`test`
on every push/PR; `download-verify.yml`/`os-matrix.yml` deliberately stay
`workflow_dispatch`-only), repo-root clutter (item 24), and README safety warnings (item 29)
are all addressed. The stray `orchestrator_journal.json` this report found was never
actually committed — it's listed in `.gitignore` (confirmed via `git log --all -- 
orchestrator_journal.json` returning nothing) — so that specific concern turned out to
already be handled, just not obviously so from the audit's own vantage point at the time.
Near-zero test coverage: see 2.19 above. No dry-run: see 2.4 above.

---

*End of report. No files other than this report were modified during this audit. No destructive disk, partition, bootloader, or firmware operations were performed on this machine.*
