#!/usr/bin/env python3
"""Prints the first working-looking mirror URL for a catalog entry ID.

Used by .github/workflows/os-matrix.yml's Tier 2/3 jobs to resolve a
catalog entry ID (passed via the workflow's entry_ids input) into a
concrete download URL, without needing a Go helper binary just for a
one-line JSON lookup. Reads the same discovery/manifests/*.json files the
real Go catalog embeds — not a separate copy of the data.
"""
import json
import pathlib
import sys

MANIFEST_DIR = pathlib.Path(__file__).resolve().parents[2] / "discovery" / "manifests"
MANIFEST_FILES = ["linux.json", "windows.json", "macos.json"]


def load_entries():
    entries = []
    for name in MANIFEST_FILES:
        path = MANIFEST_DIR / name
        if path.exists():
            entries.extend(json.loads(path.read_text()))
    return entries


def main():
    if len(sys.argv) != 2:
        print("usage: entry_download_url.py <entry-id>", file=sys.stderr)
        sys.exit(2)
    entry_id = sys.argv[1]
    for e in load_entries():
        if e.get("id") == entry_id:
            mirrors = e.get("mirrors") or ([e["download_url"]] if e.get("download_url") else [])
            if not mirrors:
                print(f"entry {entry_id!r} has no download_url or mirrors", file=sys.stderr)
                sys.exit(1)
            print(mirrors[0])
            return
    print(f"no catalog entry with id {entry_id!r}", file=sys.stderr)
    sys.exit(1)


if __name__ == "__main__":
    main()
