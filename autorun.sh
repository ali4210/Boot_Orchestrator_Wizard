#!/usr/bin/env bash
# ==============================================================================
# autorun.sh — Autonomous Multi-Tier Launcher & Self-Healing Runtime
# Supported Flags:
#   (none)  : Standard build & execute
#   -f      : Force rebuild (cleans build cache & bin/)
#   -hard   : Complete deep purge (.runtime, module cache, state journals)
# ==============================================================================
set -eo pipefail

# ------------------------------------------------------------------------------
# 0. Early Root Privilege Assertion & Password Elevation Gate
# ------------------------------------------------------------------------------
if [ "$(id -u)" -ne 0 ]; then
    echo "======================================================================"
    echo " [SECURITY NOTICE] Partitioning, formatting, and NVRAM require root."
    echo " Verifying sudo credentials..."
    echo "======================================================================"
    
    # Prompt user cleanly for password if credentials are not already cached
    if ! sudo -v; then
        echo "[FATAL] Elevated root privileges are strictly mandatory. Exiting."
        exit 1
    fi

    # Keep sudo timestamp alive in background during compilation/execution
    while true; do sudo -n true; sleep 60; kill -0 "$$" || exit; done 2>/dev/null &

    # Re-execute entire launcher with root privileges preserving current environment
    exec sudo -E "$0" "$@"
fi

REQUIRED_GO_MAJOR=1
REQUIRED_GO_MINOR=22
REQUIRED_GO_PATCH=6

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

MODE="NORMAL"
for arg in "$@"; do
    case "$arg" in
        -f|--force)
            MODE="FORCE"
            ;;
        -hard|--hard|-f-hard)
            MODE="HARD"
            ;;
    esac
done

echo "======================================================================"
echo " UNIVERSAL OS & DUAL-BOOT ORCHESTRATOR :: LAUNCHER [MODE: $MODE]"
echo "======================================================================"

# ------------------------------------------------------------------------------
# 1. Self-Healing & Purge Logic
# ------------------------------------------------------------------------------
if [ "$MODE" = "HARD" ]; then
    echo "=> [HARD PURGE] Initiating complete environment cleanup..."
    rm -rf bin/ .runtime/ orchestrator_journal.json /tmp/orchestrator_* 2>/dev/null || true
    if command -v go >/dev/null 2>&1; then
        go clean -cache -modcache -testcache 2>/dev/null || true
    fi
    echo "=> [HARD PURGE] All local runtimes, journals, and caches wiped."
elif [ "$MODE" = "FORCE" ]; then
    echo "=> [FORCE CLEAN] Purging binary artifacts and build cache..."
    rm -rf bin/ 2>/dev/null || true
    if command -v go >/dev/null 2>&1; then
        go clean -cache 2>/dev/null || true
    fi
fi

# ------------------------------------------------------------------------------
# 2. Go Toolchain Verification & Autonomous Bootstrapper
# ------------------------------------------------------------------------------
version_ge() {
    [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -n1)" = "$2" ]
}

USE_PORTABLE=0
if command -v go >/dev/null 2>&1 && [ "$MODE" != "HARD" ]; then
    SYS_GO_VER="$(go version | grep -oE 'go[0-9]+\.[0-9]+(\.[0-9]+)?' | sed 's/go//')"
    SYS_MAJOR_MINOR="$(echo "$SYS_GO_VER" | cut -d. -f1,2)"
    if version_ge "$SYS_MAJOR_MINOR" "${REQUIRED_GO_MAJOR}.${REQUIRED_GO_MINOR}"; then
        GO_BIN="go"
        echo "=> System Go runtime detected: v${SYS_GO_VER}"
    else
        echo "=> System Go (v${SYS_GO_VER}) is outdated (< ${REQUIRED_GO_MAJOR}.${REQUIRED_GO_MINOR})."
        USE_PORTABLE=1
    fi
else
    USE_PORTABLE=1
fi

if [ "$USE_PORTABLE" -eq 1 ]; then
    PORTABLE_DIR="$SCRIPT_DIR/.runtime/go"
    GO_BIN="$PORTABLE_DIR/bin/go"

    if [ ! -x "$GO_BIN" ]; then
        echo "=> Bootstrapping isolated Go ${REQUIRED_GO_MAJOR}.${REQUIRED_GO_MINOR}.${REQUIRED_GO_PATCH} runtime..."
        ARCH=$(uname -m)
        case "$ARCH" in
            x86_64)  GO_ARCH="amd64" ;;
            aarch64) GO_ARCH="arm64" ;;
            armv7l)  GO_ARCH="armv6l" ;;
            *) echo "[FATAL] Unsupported architecture: $ARCH"; exit 1 ;;
        esac

        mkdir -p "$SCRIPT_DIR/.runtime"
        TAR_FILE="$SCRIPT_DIR/.runtime/go_bootstrap.tar.gz"
        DOWNLOAD_URL="https://go.dev/dl/go${REQUIRED_GO_MAJOR}.${REQUIRED_GO_MINOR}.${REQUIRED_GO_PATCH}.linux-${GO_ARCH}.tar.gz"

        echo "=> Downloading toolchain from $DOWNLOAD_URL..."
        if command -v curl >/dev/null 2>&1; then
            curl -sSL "$DOWNLOAD_URL" -o "$TAR_FILE"
        elif command -v wget >/dev/null 2>&1; then
            wget -q "$DOWNLOAD_URL" -O "$TAR_FILE"
        else
            echo "[FATAL] Missing curl/wget. Cannot bootstrap Go runtime."
            exit 1
        fi

        echo "=> Unpacking isolated runtime..."
        tar -C "$SCRIPT_DIR/.runtime" -xzf "$TAR_FILE"
        rm -f "$TAR_FILE"
    fi
    echo "=> Using isolated portable Go toolchain: $("$GO_BIN" version)"
fi

# ------------------------------------------------------------------------------
# 3. Dependency Sync & Build Engine
# ------------------------------------------------------------------------------
echo "=> Resolving dependencies (go mod download)..."
"$GO_BIN" mod tidy
"$GO_BIN" mod download

echo "=> Compiling cmd/orchestrator..."
mkdir -p bin
"$GO_BIN" build -o bin/boot-orchestrator ./cmd/orchestrator

# ------------------------------------------------------------------------------
# 4. Launch Orchestrator (Fully Elevated)
# ------------------------------------------------------------------------------
exec ./bin/boot-orchestrator "$@"
