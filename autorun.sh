#!/usr/bin/env bash
# ==============================================================================
#  autorun.sh — Universal Zero-Touch Boot Orchestrator Launcher
#  Handles self-elevation, automated permissions, compilation, and execution.
# ==============================================================================

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# 1. Quick Help Check
for arg in "$@"; do
    case "$arg" in
        -h|--help)
            echo "Universal Boot Orchestrator :: Runtime Help"
            echo ""
            echo "Usage:"
            echo "  ./autorun.sh          Standard launch (elevates, sets permissions, builds if missing)"
            echo "  ./autorun.sh -f       Force clean rebuild + auto-resolve modules + launch"
            echo "  ./autorun.sh --hard   Deep purge of modules, caches, journals + fresh rebuild + launch"
            echo "  ./autorun.sh -h       Display this help menu"
            echo ""
            exit 0
            ;;
    esac
done

# 2. Enforce Root Privileges While Preserving User Desktop & Go Environments
if [ "$EUID" -ne 0 ]; then
    echo "======================================================================"
    echo " [SECURITY NOTICE] Hardware block operations require root privileges."
    echo " Authenticating sudo session..."
    echo "======================================================================"
    exec sudo --preserve-env=PATH,DISPLAY,XAUTHORITY,GOPATH,GOCACHE bash "$0" "$@"
fi

# Ensure GUI permissions for root on user desktop
if [ -n "$SUDO_USER" ]; then
    xhost +local:root 2>/dev/null || true
    xhost "+si:localuser:$SUDO_USER" 2>/dev/null || true
fi

# ==============================================================================
# 2.1 Autonomous Host Prerequisites Assertion Layer
# ==============================================================================
assert_host_dependencies() {
    local missing=()
    local critical_cmds=("unsquashfs" "parted" "mkfs.ext4" "mkfs.vfat" "rsync" "blkid" "losetup")

    for cmd in "${critical_cmds[@]}"; do
        if ! command -v "$cmd" >/dev/null 2>&1; then
            missing+=("$cmd")
        fi
    done

    if [ ${#missing[@]} -gt 0 ]; then
        echo "======================================================================"
        echo "=> [HOST SELF-HEAL] Missing critical extraction tooling: ${missing[*]}"
        echo "=> Autonomously deploying required system packages..."
        echo "======================================================================"

        if command -v apt-get >/dev/null 2>&1; then
            export DEBIAN_FRONTEND=noninteractive
            apt-get update -qq || true
            apt-get install -y --no-install-recommends squashfs-tools parted e2fsprogs dosfstools rsync util-linux
        elif command -v pacman >/dev/null 2>&1; then
            pacman -Sy --noconfirm squashfs-tools parted e2fsprogs dosfstools rsync util-linux
        elif command -v dnf >/dev/null 2>&1; then
            dnf install -y squashfs-tools parted e2fsprogs dosfstools rsync util-linux
        elif command -v zypper >/dev/null 2>&1; then
            zypper --non-interactive install squashfs-tools parted e2fsprogs dosfstools rsync util-linux
        fi
        echo "=> [HOST SELF-HEAL] Prerequisites satisfied."
    fi
}
assert_host_dependencies

# 3. Cache Purge Routine
purge_cache() {
    local mode="$1"
    echo "======================================================================"
    echo "=> [HOUSEKEEPING] Initiating repository and cache purge ($mode)..."
    echo "======================================================================"

    # Reclaim space on /tmp (tmpfs) from aborted builds, ISO chunks, and payloads
    rm -rf /tmp/go-build* 2>/dev/null || true
    rm -rf /tmp/boot_orchestrator_* 2>/dev/null || true
    rm -rf /tmp/orch_* 2>/dev/null || true
    rm -f /tmp/*.payload /tmp/*.part /tmp/*.iso 2>/dev/null || true
    rm -f *.tmp *.part *.log 2>/dev/null || true

    # Clean Go build cache
    go clean -cache 2>/dev/null || true

    if [ "$mode" == "hard" ]; then
        echo "=> [HARD] Purging module caches, journals, and runtime files..."
        go clean -modcache 2>/dev/null || true
        rm -f "$SCRIPT_DIR/orchestrator_journal.json" 2>/dev/null || true

        if [ -d "$SCRIPT_DIR/.runtime" ]; then
            echo "=> Purging heavy .runtime directory..."
            rm -rf "$SCRIPT_DIR/.runtime"
        fi

        if [ -d "$SCRIPT_DIR/bin" ]; then
            echo "=> Pruning binaries in bin/..."
            rm -rf "$SCRIPT_DIR/bin"
        fi

        if [ -d "$SCRIPT_DIR/.git" ]; then
            echo "=> Running deep Git packfile repacking..."
            git reflog expire --expire-unreachable=now --all 2>/dev/null || true
            git repack -a -d -f --depth=250 --window=250 2>/dev/null || true
            git prune --expire=now 2>/dev/null || true
            git gc --prune=now --aggressive 2>/dev/null || true
        fi
    fi

    CURRENT_SIZE=$(du -sh "$SCRIPT_DIR" | cut -f1)
    echo "=> [CLEANUP COMPLETE] Workspace footprint: $CURRENT_SIZE"
    echo "======================================================================"
}

# 4. Process Operational Flags
FORCE_REBUILD=0
for arg in "$@"; do
    case "$arg" in
        --hard)
            FORCE_REBUILD=1
            purge_cache "hard"
            ;;
        -f|--force)
            FORCE_REBUILD=1
            purge_cache "standard"
            ;;
    esac
done

# 5. Dependency Sync, Build & Auto-Fix Permission Layer
BIN_PATH="$SCRIPT_DIR/bin/boot-orchestrator"
LOCAL_TMP="$SCRIPT_DIR/.build_tmp"

if [ "$FORCE_REBUILD" -eq 1 ] || [ ! -f "$BIN_PATH" ]; then
    echo "======================================================================"
    echo "=> Resolving Go dependencies & cryptographic modules..."
    echo "======================================================================"
    
    # Ensure native SSH crypto module is installed and synchronized
    go get golang.org/x/crypto/ssh 2>/dev/null || true
    go mod tidy

    echo "======================================================================"
    echo "=> Compiling cmd/orchestrator/main.go into bin/boot-orchestrator..."
    echo "======================================================================"
    mkdir -p "$SCRIPT_DIR/bin" "$LOCAL_TMP"
    rm -f "$BIN_PATH" 2>/dev/null || true

    # Redirect TMPDIR to physical disk storage to prevent tmpfs RAM exhaustion
    TMPDIR="$LOCAL_TMP" go build -ldflags="-s -w" -o "$BIN_PATH" "$SCRIPT_DIR/cmd/orchestrator/main.go"
    rm -rf "$LOCAL_TMP" 2>/dev/null || true

    if [ -n "$SUDO_USER" ]; then
        chown -R "$SUDO_USER:$SUDO_USER" "$SCRIPT_DIR/bin" "$SCRIPT_DIR/go.mod" "$SCRIPT_DIR/go.sum" 2>/dev/null || true
    fi
    echo "=> Compilation successful."
fi

# Universal Execution Shield: Ensure binary has executable rights
chmod 755 "$BIN_PATH" 2>/dev/null || chmod +x "$BIN_PATH"

echo "======================================================================"
echo "=> Launching Universal Boot Orchestrator..."
echo "======================================================================"
exec "$BIN_PATH"