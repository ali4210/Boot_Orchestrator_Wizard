# Maintenance scripts (historical, not maintained)

Audit item 2.17: these one-off Python scripts were used at various points to hand-regenerate
or audit `discovery/manifests/*.json` during the catalog's early development. Nothing in this
repo's Go source, launchers, or CI invokes any of them — confirmed via a repo-wide grep before
moving them here from the repo root, where they cluttered the top level and (by their names
alone) suggested prior ad-hoc patching sessions rather than part of the shipped tool.

They are kept (not deleted) in case their logic is useful reference for a future manifest
change, but are **not maintained** and may be stale: several already embed catalog entry IDs
that no longer match the current manifests (e.g. `win11-gui-amd64` vs. the current
`win11-workstation-amd64`). Treat them as historical snapshots, not working tools — verify and
update before running any of them again.

| Script | What it was for |
|---|---|
| `audit_and_fix_all.py` | Checked/patched Windows manifest entries against hardcoded data |
| `audit_non_linux.py` | Audited Windows/macOS manifest entries for missing fields |
| `fix_linux_ids.py` | One-off fix for missing/blank `id` fields in `linux.json` |
| `generate_perfect_linux.py` | Regenerated `linux.json` from a hardcoded entry list |
| `test_manifest_mirrors.py` | Checked Linux manifest mirror URLs for liveness |
| `test_win_mac_mirrors.py` | Checked Windows/macOS manifest mirror URLs for liveness |
| `update_macos_manifest.py` | Regenerated `macos.json` from a hardcoded entry list |
| `update_manifest.py` | Regenerated the full manifest set from a hardcoded entry list |
| `update_windows_manifest.py` | Regenerated `windows.json` from a hardcoded entry list |

The project's real, maintained equivalents of "check the manifests for problems" are
`cmd/catalog-verify` (mirror reachability — see `OS_STATUS_MATRIX.md`), `cmd/download-verify`
(full-download checksum verification), and `discovery.Catalog.Audit()` (structural checksum/URL
presence — see `docs/MANIFEST_AUDIT.md`). Prefer those over anything in this directory.
