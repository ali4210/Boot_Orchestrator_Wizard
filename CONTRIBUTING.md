# Contributing

## Before anything else

Read `PROGRESS.md`. It is the authoritative, item-by-item record of what's done (with a real
commit hash and test for every "Done"), in progress, or a known, explicitly-documented
limitation — not a plan or a wishlist. If you're picking up a specific audit item or bug,
find it there first; it very likely already has investigation notes, root-cause analysis,
and a decision about what was and wasn't fixed and why.

`AUDIT_REPORT.md` is the original audit findings this whole remediation effort works through.
`OS_STATUS_MATRIX.md` and `docs/MANIFEST_AUDIT.md`/`docs/SILENT_ERROR_AUDIT.md` are
similarly real, dated survey documents — not aspirational.

## The rules this codebase's own history was built under

These aren't style preferences; they're safety rules that kept a tool capable of
partitioning real disks and writing UEFI NVRAM from ever doing so by accident during its own
development:

- **Never touch a real disk, partition, bootloader, EFI variable, or NVRAM entry in a test
  or exploratory command.** Every destructive test in this repo targets a disposable
  loop-mounted disk image (`losetup`) or a tmpfs mount it creates and tears down itself. If
  you need to prove a fix against something destructive, build a loop-image fixture — see
  `engine/revert_linux_loopimage_test.go` for the established pattern.
- **Never read, print, modify, or commit a real `orchestrator_journal.json` / the default
  journal state directory** (`safety.DefaultJournalPath()`, normally
  `/var/lib/boot-orchestrator` or `/var/tmp/boot-orchestrator`). Tests that touch
  `DefaultJournalPath()` must redirect it first via
  `safety.SetJournalCandidateDirsForTesting` — see `safety/journal_security_test.go`.
- **Never fabricate data you can't independently verify.** No invented SHA256 checksums, no
  invented OpenPGP signing keys, no guessed-but-unverified version numbers. If something is
  missing data and can't safely be backfilled in the current change, document it as a known
  gap (see `docs/MANIFEST_AUDIT.md`) rather than inventing a plausible-looking value.
- **Every bug fix needs a test that fails before the fix and passes after.** Prove this for
  real — temporarily revert just the fix (`git stash push -- <file>`, or comment out the
  change) and confirm the test actually fails with the expected symptom before restoring it.
  A test that would pass against the old, broken code isn't testing the fix.
- **`go build ./... && go vet ./... && go test ./...` must pass before every commit.**
  Root-gated tests (see below) are expected to skip cleanly, not fail, when run
  unprivileged.
- **One commit per logical fix/item.** If you need to correct something in `PROGRESS.md`
  after the real commit hash is known, that's a legitimate separate small follow-up commit
  — don't amend.
- **Never rewrite git history** (`rebase -i`, `commit --amend` on anything already
  committed) and **never force-push**.

## Running the tests: two supported commands

```bash
go test ./...                                 # as a normal (non-root) user
sudo -E go test ./engine/... ./safety/...     # for the root-gated loop-image tests
```

- **`go test ./...`, as a normal user.** This is the default, and what you should run while
  developing. A handful of tests require real root privileges to exercise their real
  behavior (see below) and will cleanly `t.Skip` themselves here rather than fail — that's
  expected, not a gap.
- **`sudo -E go test ./engine/... ./safety/...`.** A handful of tests exercise real
  partition/filesystem operations against a loop-mounted disk image file (`losetup`), and
  require the test *process itself* — not just a subcommand it shells out to — to run as
  root; they check `os.Geteuid() == 0` directly and skip cleanly otherwise. `-E` preserves
  your environment (`GOCACHE`/`GOPATH`/`PATH`) so `go` itself is still found and uses your
  already-permissioned build cache, rather than trying to rebuild one as root. These never
  touch a real block device — only a `.img` file created in a temp directory for the
  duration of the test. This is exactly what `.github/workflows/ci.yml`'s
  `test-root-gated-loopdevice` job runs in CI on every push/PR, not just something to run
  locally by hand.
  - **The reverse also holds**: `autorun_test.go`'s `TestCheckRootOwnedFiles_BinOwnedByRootBlocks`/
    `TestCheckRootOwnedFiles_RuntimeDirOwnedByRootBlocks` test `autorun.sh`'s
    `check_root_owned_files`, which intentionally does nothing when `$EUID -eq 0` (root can
    read/write/purge anything regardless of file ownership, so there's nothing to block) —
    these two specifically check `os.Geteuid() == 0` and skip under the *second* command
    above, the opposite of the loop-image tests. Both directions of "skip under the wrong
    privilege level" are deliberate, not something to "fix" by forcing a test to run
    somewhere its own premise doesn't hold.
  - If you only need one specific root-gated test while iterating, narrow it with `-run`,
    e.g. `sudo -E go test ./engine/... -run TestExecuteDecommission -v`.

## Verification tools (don't run these casually)

- `go run ./cmd/catalog-verify` — cheap, safe (HTTP HEAD only, no real downloads). Fine to
  run anytime.
- `go run ./cmd/download-verify` — downloads every catalog entry in full (real,
  multi-gigabyte files). Only via `.github/workflows/download-verify.yml`'s
  `workflow_dispatch`, never casually and never in CI on every push.
- The `OS Status Matrix` workflow (`.github/workflows/os-matrix.yml`, on `test/os-matrix`) —
  real downloads plus QEMU boots. Same rule: `workflow_dispatch` only, and mind the
  `entry_ids` input (defaults to a single small entry).

## Maintenance scripts

`tools/maintenance/` holds historical one-off Python scripts used at various points to
hand-regenerate the catalog manifests. They are not maintained and may be stale — see
`tools/maintenance/README.md`. Prefer `cmd/catalog-verify`/`cmd/download-verify`/
`discovery.Catalog.Audit()` for checking the manifests today.

## Code style

- Default to no comments; add one only when the *why* isn't obvious from the code itself (a
  hidden constraint, a workaround for a specific real-world bug, something that would
  surprise a future reader).
- Don't add error handling, fallbacks, or abstractions for scenarios that can't happen here.
  Trust internal invariants; validate only at real system boundaries.
- Match the existing pattern for making something testable: a package-level `var` holding a
  function/command (e.g. `cmdRunner`, `updateGrubFunc`, `userLookupFunc`) that production
  code calls through and a test can swap out — not a new abstraction layer.
