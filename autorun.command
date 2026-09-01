#!/usr/bin/env bash
# autorun.command — macOS launcher for boot-orchestrator.
# Named .command (not .sh) so it's double-clickable from Finder as well as
# runnable from Terminal. Checks for Go, fetches module dependencies,
# builds cmd/orchestrator, runs it.
set -euo pipefail

REQUIRED_GO_MAJOR=1
REQUIRED_GO_MINOR=22

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo "=== boot-orchestrator autorun (macOS) ==="

version_ge() {
    [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -n1)" = "$2" ]
}

if ! command -v go >/dev/null 2>&1; then
    echo "Go toolchain not found on PATH."
    if command -v brew >/dev/null 2>&1; then
        echo "Homebrew detected. Install Go with:"
        echo "    brew install go"
    else
        echo "Install Go 1.22+ from https://go.dev/dl/ (or install Homebrew first: https://brew.sh)"
    fi
    exit 1
fi

GO_VER="$(go version | grep -oE 'go[0-9]+\.[0-9]+(\.[0-9]+)?' | sed 's/go//')"
GO_MAJOR_MINOR="$(echo "$GO_VER" | cut -d. -f1,2)"
if ! version_ge "$GO_MAJOR_MINOR" "${REQUIRED_GO_MAJOR}.${REQUIRED_GO_MINOR}"; then
    echo "Found Go $GO_VER, but this project requires Go ${REQUIRED_GO_MAJOR}.${REQUIRED_GO_MINOR}+."
    echo "Upgrade with: brew upgrade go   (or download from https://go.dev/dl/)"
    exit 1
fi
echo "Go $GO_VER found — OK."

echo ""
echo "--- Fetching module dependencies (go mod download) ---"
go mod download

echo ""
echo "--- Building cmd/orchestrator ---"
mkdir -p bin
go build -o bin/boot-orchestrator ./cmd/orchestrator

echo ""
echo "--- Checking for privileged operations ---"
if [ "$(id -u)" -ne 0 ]; then
    echo "NOTE: disk and NVRAM-adjacent operations on macOS need elevated privileges"
    echo "for some commands (diskutil, csrutil-gated actions). The TUI will start"
    echo "now in read-only/discovery mode; re-run with sudo when you're ready to"
    echo "actually provision a system:"
    echo "    sudo ./autorun.command"
    echo ""
    echo "Also note: macOS's own boot picker (System Settings > Startup Disk, or"
    echo "holding Option at boot) is the supported way to manage Mac boot targets;"
    echo "this tool's UEFI NVRAM functions target standard UEFI PCs, not Apple's"
    echo "EFI/APFS boot picker, which works differently under the hood."
    echo ""
fi

echo "--- Launching boot-orchestrator ---"
exec ./bin/boot-orchestrator
