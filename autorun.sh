#!/usr/bin/env bash
# ==============================================================================
#  autorun.sh — Universal Boot Orchestrator Launcher & Cache Purger
#  Supports:
#    ./autorun.sh         -> Standard elevated launch (builds if missing)
#    ./autorun.sh -f      -> Force clean compilation + purge caches + launch
#    ./autorun.sh --hard  -> Deep purge of .runtime, Git, Go cache + rebuild + launch
#    ./autorun.sh -h      -> Display help & usage instructions
# ==============================================================================

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# 1. Immediate Help Check (No root required)
for arg in "$@"; do
    case "$arg" in
        -h|--help)
            echo "Universal Boot Orchestrator :: Runtime Help & Diagnostics"
            echo ""
            echo "Usage:"
            echo "  ./autorun.sh         Standard launch (elevates via sudo, builds if missing)"
            echo "  ./autorun.sh -f      Force recompilation + cache purge + launch"
            echo "  ./autorun.sh --hard  Deep aggressive purge (.runtime, git, caches) + rebuild + launch"
            echo "  ./autorun.sh -h      Display this help menu"
            echo ""
            exit 0
            ;;
    esac
done

# 2. Require Root Privileges BEFORE Running Housekeeping & Execution
if [ "$EUID" -ne 0 ]; then
    echo "======================================================================"
    echo " [SECURITY NOTICE] Hardware block operations require root privileges."
    echo " Authenticating sudo session..."
    echo "======================================================================"
    exec sudo bash "$0" "$@"
fi

# 3. Cache Purge Function
purge_cache() {
    local mode="$1"
    echo "======================================================================"
    echo "=> [HOUSEKEEPING] Initiating repository and cache purge ($mode)..."
    echo "======================================================================"

    # Remove dangling temporary files and build artifacts
    echo "=> Removing dangling build artifacts and temporary files..."
    rm -rf /tmp/boot_orchestrator_* 2>/dev/null || true
    rm -f *.tmp *.part *.log 2>/dev/null || true

    # Deep Git compaction
    if [ -d ".git" ]; then
        echo "=> Expiring Git reflogs and running aggressive garbage collection..."
        git reflog expire --expire=now --all 2>/dev/null || true
        git gc --prune=now --aggressive 2>/dev/null || true
    fi

    # Go build cache
    echo "=> Purging Go build cache..."
    go clean -cache 2>/dev/null || true

    # If --hard is requested, purge the 253MB .runtime folder
    if [ "$mode" == "hard" ]; then
        if [ -d "$SCRIPT_DIR/.runtime" ]; then
            echo "=> Purging heavy .runtime directory (reclaiming 250MB+)..."
            rm -rf "$SCRIPT_DIR/.runtime"
        fi
    fi

    CURRENT_SIZE=$(du -sh "$SCRIPT_DIR" | cut -f1)
    echo "=> [CLEANUP COMPLETE] Workspace footprint: $CURRENT_SIZE"
    echo "======================================================================"
}

# 4. Parse Operational Flags
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

# 5. Build and Launch
BIN_PATH="$SCRIPT_DIR/bin/boot-orchestrator"

if [ "$FORCE_REBUILD" -eq 1 ] || [ ! -f "$BIN_PATH" ]; then
    echo "=> Compiling cmd/orchestrator..."
    mkdir -p "$SCRIPT_DIR/bin"
    CGO_ENABLED=0 go build -ldflags="-s -w" -o "$BIN_PATH" ./cmd/orchestrator
    
    if [ -n "$SUDO_USER" ]; then
        chown -R "$SUDO_USER:$SUDO_USER" "$SCRIPT_DIR/bin" 2>/dev/null || true
    fi
fi

echo "=> Launching Universal Boot Orchestrator..."
exec "$BIN_PATH"
