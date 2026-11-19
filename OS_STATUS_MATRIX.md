# OS Status Matrix

Phase 3 deliverable. Tracks, per catalog entry, four independent checks ("tiers"), each
progressively more expensive:

- **Tier 1 — catalog reachability** (`cmd/catalog-verify`): an HTTP HEAD (or 1-byte ranged
  GET fallback) against every mirror. Cheap, safe, no real download. **Run against the full
  catalog for real as part of producing this document** — see results below.
- **Tier 2 — Docker ISO-structure check** (`testing/docker/iso-structure-verify`): downloads
  an entry's ISO, and inside a hermetic Debian-based Docker container (no loop-mount, no
  privileged container needed — reads the ISO9660 tree directly via `xorriso`), checks
  whether its layout matches what `engine/autonomous_installer_script.go`'s
  `renderAutonomousInstallerScript` actually searches for at install time: a
  `casper|live|install/filesystem.squashfs`, any top-level `*.squashfs`, or (as a secondary
  fallback) a `/live` directory. **Proven against one real entry this session** (`alpine-linux-amd64`)
  — see below for how to run it against more.
- **Tier 3 — QEMU/OVMF boot matrix** (`testing/qemu/boot-and-verify.sh`): boots the real ISO
  under QEMU with OVMF UEFI firmware and watches its serial console for a success pattern,
  within a bounded timeout. Runs with `-accel tcg` (software emulation) deliberately — neither
  this project's dev sandbox nor a default GitHub-hosted Actions runner has `/dev/kvm`, so a
  pipeline that assumed KVM would simply fail to start anywhere it's actually meant to run.
  **Proven against one real entry this session** (`alpine-linux-amd64`, booted in 48 seconds).
- **Tier 4 — Windows/macOS native build+test**: a GitHub Actions matrix job on
  `windows-latest`/`macos-latest` running `go build ./... && go vet ./... && go test ./...`
  natively on each OS — the only way this project's Windows-specific (BCD/WIM) and
  Darwin-specific (`revert_darwin.go`) code paths actually get compiled and unit-tested on
  their real target OS. **This never boots a macOS (or Windows) guest image** — it is native
  CI on GitHub's own runner hardware building this repo's Go code, not a Hackintosh VM. Not
  run from this session (no access to GitHub-hosted Windows/macOS runners from here); defined
  in `.github/workflows/os-matrix.yml` and runs whenever that workflow is dispatched.

**Why only one entry for Tiers 2/3, when the catalog has 29:** QEMU under software emulation
(no KVM here or on a default Actions runner) and full ISO downloads are both slow and
bandwidth-heavy. `alpine-linux-amd64` (210MB) was chosen specifically to prove the pipeline
mechanism end-to-end for real — not mocked, not simulated — within this session's actual time
and bandwidth budget. Running it against the rest of the catalog is exactly what
`.github/workflows/os-matrix.yml`'s `workflow_dispatch` input (`entry_ids`) is for; see
"Running the full matrix" below.

## Results

| Entry | Family | Tier 1: reachable | Tier 2: installer-compatible | Tier 3: boots under QEMU/OVMF | Notes |
|---|---|---|---|---|---|
| win11-workstation-amd64 | windows | Reachable | Not run | Not run | |
| win10-workstation-amd64 | windows | Reachable | Not run | Not run | approx_size_bytes drifted from actual (informational, not a failure) |
| winsrv2022-server-amd64 | windows | Reachable | Not run | Not run | |
| winxp-pro-i386 | windows | Reachable | Not run | Not run | |
| kali-gui-amd64 | linux | Reachable | Not run | Not run | approx_size_bytes drifted from actual (informational) |
| kali-netinst-amd64 | linux | Reachable | Not run | Not run | |
| parrot-security-gui-amd64 | linux | Reachable | Not run | Not run | |
| parrot-home-gui-amd64 | linux | Reachable | Not run | Not run | approx_size_bytes drifted from actual (informational) |
| parrot-core-tty-amd64 | linux | Reachable | Not run | Not run | approx_size_bytes drifted from actual (informational) |
| ubuntu-desktop-amd64 | linux | Reachable | Not run | Not run | approx_size_bytes drifted from actual (informational) |
| ubuntu-server-amd64 | linux | Reachable | Not run | Not run | approx_size_bytes drifted from actual (informational) |
| linux-mint-cinnamon-amd64 | linux | Reachable | Not run | Not run | |
| debian-netboot-amd64 | linux | Reachable | Not run | Not run | |
| fedora-workstation-amd64 | linux | Reachable | Not run | Not run | |
| fedora-server-amd64 | linux | Reachable | Not run | Not run | approx_size_bytes drifted from actual (informational) |
| opensuse-tumbleweed-gui-amd64 | linux | Reachable | Not run | Not run | approx_size_bytes drifted from actual (informational) |
| opensuse-tumbleweed-netinst-amd64 | linux | Reachable | Not run | Not run | |
| arch-linux-amd64 | linux | Reachable | Not run | Not run | |
| **alpine-linux-amd64** | linux | Reachable | **Incompatible** — no squashfs, no `/live` dir (see finding below) | **Boots** — reaches `localhost login:` in 48s under TCG | |
| centos-7-dvd-amd64 | linux | Reachable | Not run | Not run | |
| centos-7-minimal-amd64 | linux | Reachable | Not run | Not run | |
| centos-stream-9-amd64 | linux | Reachable | Not run | Not run | approx_size_bytes drifted from actual (informational) |
| rocky-9-workstation-amd64 | linux | Reachable | Not run | Not run | approx_size_bytes drifted from actual (informational) |
| rocky-9-minimal-amd64 | linux | Reachable | Not run | Not run | approx_size_bytes drifted from actual (informational) |
| almalinux-9-minimal-amd64 | linux | Reachable | Not run | Not run | approx_size_bytes drifted from actual (informational) |
| **gentoo-stage3-amd64** | linux | **UNREACHABLE** (HTTP 404, both mirrors) | Not run | Not run | **Manifest bug — see below** |
| slackware-15-amd64 | linux | Reachable | Not run | Not run | |
| **macos-sonoma-virt-amd64** | macos | **UNREACHABLE** (HTTP 403) | N/A — never booted (see Tier 4) | N/A — never booted | **Manifest bug — see below** |
| **macos-sequoia-virt-amd64** | macos | **UNREACHABLE** (HTTP 403) | N/A — never booted (see Tier 4) | N/A — never booted | **Manifest bug — see below** |

Raw evidence: `testing/reports/tier1-catalog-report-2026-11-18.json` (full catalog, all 29
entries), `testing/reports/tier2-alpine-linux-amd64-2026-11-18.json`,
`testing/reports/tier3-alpine-linux-amd64-2026-11-18.json`.

## Real findings from this pass

1. **`gentoo-stage3-amd64`'s manifest URLs are dead (HTTP 404 on both the primary and
   alternate mirror).** Both embed the identical autobuild timestamp
   (`install-amd64-minimal-20260830T151604Z.iso` / `livegui-amd64-20260830T151604Z.iso`) —
   Gentoo publishes a new dated autobuild roughly daily, so a filename with a baked-in
   timestamp goes stale almost immediately. This is a manifest data bug (`discovery/manifests/linux.json`),
   not a code bug: the fix is to point at Gentoo's "latest" symlink/redirect pattern instead of
   a dated snapshot, which is a data-curation decision, not something to guess at here.
2. **Both macOS entries' recovery-image URLs return HTTP 403.** `oscdn.apple.com` recovery
   fetch URLs are normally generated per-request by Apple's own SoftwareUpdate protocol and
   are time-limited/signed — a static URL hardcoded into a manifest is expected to expire.
   Same category of bug as #1 (stale pinned URL), same conclusion: a data-curation fix, not
   a code fix, and consistent with this tool's own `requires_license`/EOL-tracking fields
   already existing for exactly this kind of entry that needs periodic human review.
3. **`alpine-linux-amd64`'s ISO is not compatible with this tool's autonomous dual-boot
   installer logic as currently written.** Confirmed via Tier 2: Alpine's standard ISO has no
   `*.squashfs` file anywhere (it uses `boot/modloop-lts`, a squashfs-*formatted* module
   loopback file, but not named `*.squashfs` and not at a path
   `renderAutonomousInstallerScript`'s `SQUASH_FILE` search recognizes) and no `/live`
   directory either. Before audit follow-up G1 (`PROGRESS.md` item 50), selecting Alpine for a
   dual-boot autonomous install would have silently fallen through to the installer script's
   generic `rsync -aHAX /mnt/source/ /mnt/target/` fallback — copying the **live ISO's own
   read-only root**, not an installable rootfs, onto the target partition — only after the
   target partition had already been destructively reformatted.
   **Status update**: item 50 fixed the safety consequence for real (both real install paths
   now detect an unsupported layout and abort *before* any disk write — see
   `engine/autonomous_installer_script.go`'s `detectInstallerLayoutOrAbort` and
   `engine/orchestrator.go`'s `locateSquashfsOrErr`, both with real tests proving it). A later
   follow-up (item 65 at the time of this note) went further and marked `alpine-linux-amd64`
   `Unavailable` in the manifest (`"incompatible with the autonomous installer"`) — hidden from
   the TUI's catalog listing and refused outright in headless mode, the same treatment
   `gentoo-stage3-amd64` and both macOS entries already had — rather than leaving it
   selectable only to fail loudly at image-deploy time. See `READINESS_GATE.md` and
   `PROGRESS.md` for the real commits/tests.
4. **approx_size_bytes drift is widespread but not itself a bug.** 12 of 29 entries show a
   real `Content-Length` more than 256MiB off from the manifest's `approx_size_bytes`. Nearly
   all of these point at a distro's own "latest"/rolling URL (Rocky "latest", Fedora point
   release, CentOS Stream "latest", openSUSE Tumbleweed's daily snapshot, ...) whose real size
   naturally drifts as the upstream project ships point updates. Informational only — Tier 1
   does not fail the run over this, only over an outright unreachable mirror.

## Running the full matrix

Tier 1 is cheap enough to just run directly:
```bash
go run ./cmd/catalog-verify -out catalog-report.json
```

Tiers 2-4 run via the `OS Status Matrix` GitHub Actions workflow
(`.github/workflows/os-matrix.yml`), `workflow_dispatch` only (same reasoning as
`download-verify.yml`: real multi-gigabyte downloads and, for Tier 3, real emulated boots —
never on push/PR):
```bash
gh workflow run "OS Status Matrix" --ref test/os-matrix \
  -f entry_ids="alpine-linux-amd64 debian-netboot-amd64 slackware-15-amd64"
gh run list --workflow="OS Status Matrix" --limit 1
gh run watch <run-id>
gh run download <run-id> --dir ./os-matrix-reports
```
Leaving `entry_ids` at its default (`alpine-linux-amd64`) re-runs just the one entry already
proven here. Passing a longer list runs Tiers 2/3 against each of those IDs in turn — mind the
runner's disk space and the job's `timeout-minutes` (60 for Tier 2, 90 for Tier 3) when
choosing how many/how large. Tier 4 always runs (no input needed) since it never downloads an
OS image at all.

**Per-entry boot-success patterns** (`testing/scripts/boot_success_pattern.py`) are VERIFIED
only for `alpine-linux-amd64` so far; every other entry falls back to a generic, best-effort
pattern (`GRUB|isolinux|SYSLINUX|Loading Linux|Booting|login:|BusyBox`) that only confirms the
firmware handed off to *some* bootloader/kernel text on the serial console — not a confirmed
login prompt. Running Tier 3 against a new entry for the first time should be treated as
"does this pattern need tightening," not "this entry is now fully verified," until its exact
boot behavior has actually been observed once (the same honesty convention `PROGRESS.md`
already uses for "reproduced vs. inferred").
