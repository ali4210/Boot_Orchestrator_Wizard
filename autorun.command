#!/usr/bin/env bash
# ==============================================================================
#  autorun.command — Universal Boot Orchestrator macOS Runtime Launcher
#  Handles interactive Terminal allocation, AppleScript elevation, architecture
#  resolution (Apple Silicon arm64 / Intel x86_64), dependencies, and compilation.
# ==============================================================================

set -e

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
cd "$DIR"

# 1. Quick Help Check
for arg in "$@"; do
    case "$arg" in
        -h|--help)
            echo "Universal Boot Orchestrator :: macOS Runtime Help"
            echo ""
            echo "Usage:"
            echo "  ./autorun.command          Standard launch (elevates, builds if missing)"
            echo "  ./autorun.command -f       Force clean rebuild + launch"
            echo "  ./autorun.command --hard   Deep purge + clean rebuild + launch"
            echo "  ./autorun.command -h       Display this help menu"
            echo ""
            exit 0
            ;;
    esac
done

# 2. Enforce Interactive TTY & Root Privileges via sudo / AppleScript
if [ "$EUID" -ne 0 ]; then
    # Test if sudo credentials are already cached
    if sudo -n true 2>/dev/null; then
        exec sudo --preserve-env=PATH,GOPATH,GOCACHE bash "$0" "$@"
    fi

    echo "======================================================================"
    echo " [SECURITY NOTICE] Hardware block operations require root privileges."
    echo " Requesting Administrator elevation via native macOS prompt..."
    echo "======================================================================"

    # Re-run inside an interactive Terminal window so Bubble Tea TUI has a pseudo-terminal
    ESCAPED_DIR=$(printf '%q' "$DIR")
    ARGS="$*"
    osascript -e "do shell script \"sudo --preserve-env=PATH,GOPATH,GOCACHE bash $ESCAPED_DIR/autorun.command $ARGS\" with administrator privileges"
    exit $?
fi

echo "======================================================================"
echo " UNIVERSAL OS BOOT ORCHESTRATOR :: MACOS (DARWIN) RUNTIME"
echo " Privilege Status: Root / Administrator Verified"
echo "======================================================================"

# 3. Resolve Architecture & Binary Target
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64) GOARCH="amd64" ;;
    arm64)  GOARCH="arm64" ;;
    *)      GOARCH="amd64" ;;
esac

BIN_PATH="$DIR/bin/boot-orchestrator-darwin-$GOARCH"
LOCAL_TMP="$DIR/.build_tmp"

# 4. Process Operational Flags & Cache Purging
FORCE_REBUILD=0
for arg in "$@"; do
    case "$arg" in
        --hard)
            FORCE_REBUILD=1
            echo "=> [HARD] Purging module caches, journals, and runtime files..."
            rm -f "$DIR/orchestrator_journal.json" "$DIR"/*.tmp "$DIR"/*.log 2>/dev/null || true
            rm -rf "$DIR/bin" "$DIR/.runtime" 2>/dev/null || true
            if command -v go >/dev/null 2>&1; then
                go clean -cache -modcache 2>/dev/null || true
            fi
            ;;
        -f|--force)
            FORCE_REBUILD=1
            echo "=> [FORCE] Rebuilding binary cleanly..."
            rm -f "$BIN_PATH" 2>/dev/null || true
            if command -v go >/dev/null 2>&1; then
                go clean -cache 2>/dev/null || true
            fi
            ;;
    esac
done

# 5. Dependency Sync & Compilation
if [ "$FORCE_REBUILD" -eq 1 ] || [ ! -f "$BIN_PATH" ]; then
    if ! command -v go >/dev/null 2>&1; then
        echo "======================================================================"
        echo "=> [PREREQUISITE] Go compiler not found in PATH."
        if command -v brew >/dev/null 2>&1; then
            echo "=> Installing Go via Homebrew..."
            brew install go
        else
            echo "[-] Error: Please install Go from https://go.dev/dl/ to compile on macOS."
            exit 1
        fi
    fi

    echo "======================================================================"
    echo "=> Resolving dependencies & compiling for Darwin ($GOARCH)..."
    echo "======================================================================"
    mkdir -p "$DIR/bin" "$LOCAL_TMP"
    rm -f "$BIN_PATH" 2>/dev/null || true

    export CGO_ENABLED=0
    export GOOS=darwin
    export GOARCH="$GOARCH"

    TMPDIR="$LOCAL_TMP" go build -ldflags="-s -w" -o "$BIN_PATH" "$DIR/cmd/orchestrator/main.go"
    rm -rf "$LOCAL_TMP" 2>/dev/null || true

    if [ -n "$SUDO_USER" ]; then
        chown -R "$SUDO_USER" "$DIR/bin" "$DIR/go.mod" "$DIR/go.sum" 2>/dev/null || true
    fi
    echo "=> Compilation successful."
fi

chmod 755 "$BIN_PATH" 2>/dev/null || chmod +x "$BIN_PATH"

echo "======================================================================"
echo "=> Launching Universal Boot Orchestrator..."
echo "======================================================================"
exec "$BIN_PATH"