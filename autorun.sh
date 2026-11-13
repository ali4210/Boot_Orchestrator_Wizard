#!/usr/bin/env bash
# ==============================================================================
#  autorun.sh — Universal Zero-Touch Boot Orchestrator Launcher
#  Handles automated permissions, compilation, and execution.
#
#  Audit 1D.9: this used to self-elevate to root before doing anything else,
#  so dependency resolution and compilation (go get/go mod tidy/go build) ran
#  as root — writing root-owned files into the invoking user's own GOPATH/
#  GOCACHE and the repo's bin/ directory. It also auto-installed system
#  packages with no confirmation, used unguarded `rm -rf "$SCRIPT_DIR/..."`
#  (catastrophic if SCRIPT_DIR were ever empty), and ran `go get`/
#  `go mod tidy` on every launch (network calls that can silently change
#  go.mod/go.sum). Root is now requested only once, right before launching
#  the already-built binary; everything before that runs as the invoking
#  user.
# ==============================================================================

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${SCRIPT_DIR:?}"

# 1. Quick Help Check
for arg in "$@"; do
    case "$arg" in
        -h|--help)
            echo "Universal Boot Orchestrator :: Runtime Help"
            echo ""
            echo "Usage:"
            echo "  ./autorun.sh          Standard launch (builds if missing, then elevates to run)"
            echo "  ./autorun.sh -f       Force clean rebuild + launch"
            echo "  ./autorun.sh --hard   Deep purge of modules, caches, journals + fresh rebuild + launch"
            echo "  ./autorun.sh -y       Assume 'yes' to any confirmation (e.g. installing missing packages)"
            echo "  ./autorun.sh -h       Display this help menu"
            echo ""
            exit 0
            ;;
    esac
done

if [ "$EUID" -eq 0 ] && [ -z "$SUDO_USER" ]; then
    echo "======================================================================"
    echo " [NOTICE] Running directly as root (no SUDO_USER) — dependency"
    echo " resolution and compilation will produce root-owned files in this"
    echo " repository. Prefer running this script as your normal user; it"
    echo " will ask for root only when it actually needs it."
    echo "======================================================================"
fi

ASSUME_YES=0
for arg in "$@"; do
    case "$arg" in
        -y|--yes)
            ASSUME_YES=1
            ;;
    esac
done

# confirm_or_abort prompts before any action that installs system packages
# or otherwise needs root, unless -y/--yes was passed (audit 1D.9).
confirm_or_abort() {
    local prompt="$1"
    if [ "$ASSUME_YES" -eq 1 ]; then
        return 0
    fi
    if [ ! -t 0 ]; then
        echo "=> [ABORT] '$prompt' needs confirmation but stdin is not a terminal; re-run with -y/--yes to proceed non-interactively." >&2
        exit 1
    fi
    read -r -p "$prompt [y/N] " reply
    case "$reply" in
        [yY]|[yY][eE][sS]) return 0 ;;
        *) echo "Aborted."; exit 1 ;;
    esac
}

# ==============================================================================
# 2. Autonomous Host Prerequisites Assertion Layer
# ==============================================================================
assert_host_dependencies() {
    local missing=()
    local critical_cmds=("unsquashfs" "parted" "mkfs.ext4" "mkfs.vfat" "rsync" "blkid" "losetup")

    for cmd in "${critical_cmds[@]}"; do
        if ! command -v "$cmd" >/dev/null 2>&1; then
            missing+=("$cmd")
        fi
    done

    if [ ${#missing[@]} -eq 0 ]; then
        return 0
    fi

    echo "======================================================================"
    echo "=> [HOST SELF-HEAL] Missing critical extraction tooling: ${missing[*]}"
    echo "======================================================================"
    confirm_or_abort "Install missing system packages (${missing[*]}) via the system package manager? This requires root."

    local install_cmd=()
    if command -v apt-get >/dev/null 2>&1; then
        install_cmd=(env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends squashfs-tools parted e2fsprogs dosfstools rsync util-linux)
    elif command -v pacman >/dev/null 2>&1; then
        install_cmd=(pacman -Sy --noconfirm squashfs-tools parted e2fsprogs dosfstools rsync util-linux)
    elif command -v dnf >/dev/null 2>&1; then
        install_cmd=(dnf install -y squashfs-tools parted e2fsprogs dosfstools rsync util-linux)
    elif command -v zypper >/dev/null 2>&1; then
        install_cmd=(zypper --non-interactive install squashfs-tools parted e2fsprogs dosfstools rsync util-linux)
    else
        echo "=> [HOST SELF-HEAL] No supported package manager found; install ${missing[*]} manually." >&2
        return 0
    fi

    echo "=> [HOST SELF-HEAL] Deploying required system packages (will prompt for root)..."
    if [ "$EUID" -ne 0 ]; then
        sudo "${install_cmd[@]}"
    else
        "${install_cmd[@]}"
    fi
    echo "=> [HOST SELF-HEAL] Prerequisites satisfied."
}
assert_host_dependencies

# 3. Cache Purge Routine — runs as the invoking user; never needs root.
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
    rm -f ./*.tmp ./*.part ./*.log 2>/dev/null || true

    # Clean Go build cache
    go clean -cache 2>/dev/null || true

    if [ "$mode" == "hard" ]; then
        echo "=> [HARD] Purging module caches, journals, and runtime files..."
        go clean -modcache 2>/dev/null || true
        rm -f "${SCRIPT_DIR:?}/orchestrator_journal.json" 2>/dev/null || true

        if [ -d "${SCRIPT_DIR:?}/.runtime" ]; then
            echo "=> Purging heavy .runtime directory..."
            rm -rf "${SCRIPT_DIR:?}/.runtime"
        fi

        if [ -d "${SCRIPT_DIR:?}/bin" ]; then
            echo "=> Pruning binaries in bin/..."
            rm -rf "${SCRIPT_DIR:?}/bin"
        fi

        if [ -d "${SCRIPT_DIR:?}/.git" ]; then
            echo "=> Running deep Git packfile repacking..."
            git reflog expire --expire-unreachable=now --all 2>/dev/null || true
            git repack -a -d -f --depth=250 --window=250 2>/dev/null || true
            git prune --expire=now 2>/dev/null || true
            git gc --prune=now --aggressive 2>/dev/null || true
        fi
    fi

    CURRENT_SIZE=$(du -sh "${SCRIPT_DIR:?}" | cut -f1)
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

# 5. Root-owned file detection (follow-up F5). Since the build below runs
# as the invoking user, writing into a bin/ (or a journal) left root-owned
# by a run from before item 1D.9's fix — or by someone directly invoking
# `sudo ./autorun.sh` by hand — would otherwise fail with a bare, confusing
# "permission denied" partway through the build. Detect it up front and
# print the exact fix instead.
BIN_PATH="${SCRIPT_DIR:?}/bin/boot-orchestrator"
LOCAL_TMP="${SCRIPT_DIR:?}/.build_tmp"
JOURNAL_PATH="${SCRIPT_DIR:?}/orchestrator_journal.json"

check_root_owned_files() {
    if [ "$EUID" -eq 0 ]; then
        return 0
    fi

    local offenders=()
    if [ -d "${SCRIPT_DIR:?}/bin" ]; then
        while IFS= read -r -d '' f; do
            offenders+=("$f")
        done < <(find "${SCRIPT_DIR:?}/bin" -user root -print0 2>/dev/null)
    fi
    if [ -e "$JOURNAL_PATH" ]; then
        local owner
        owner=$(stat -c '%U' "$JOURNAL_PATH" 2>/dev/null || stat -f '%Su' "$JOURNAL_PATH" 2>/dev/null || echo "")
        if [ "$owner" = "root" ]; then
            offenders+=("$JOURNAL_PATH")
        fi
    fi

    if [ ${#offenders[@]} -eq 0 ]; then
        return 0
    fi

    echo "======================================================================" >&2
    echo " [BLOCKED] Found root-owned file(s) left over from a previous run:" >&2
    printf '   %s\n' "${offenders[@]}" >&2
    echo "" >&2
    echo " This build runs as your own user and cannot write to these. Fix" >&2
    echo " ownership, then re-run this script:" >&2
    echo "" >&2
    echo "   sudo chown -R \"$(id -un):$(id -gn)\" \"${SCRIPT_DIR:?}/bin\" \"$JOURNAL_PATH\"" >&2
    echo "======================================================================" >&2
    exit 1
}
check_root_owned_files

# 6. Dependency Sync & Build — runs as the invoking user (never root), so no
# build artifact, module cache entry, or compiled binary is ever root-owned
# (audit 1D.9).
if [ "$FORCE_REBUILD" -eq 1 ] || [ ! -f "$BIN_PATH" ]; then
    echo "======================================================================"
    echo "=> Resolving Go dependencies (go mod download; go.mod/go.sum are not"
    echo "   modified — run 'go mod tidy' yourself if you intend that)..."
    echo "======================================================================"
    go mod download

    echo "======================================================================"
    echo "=> Compiling cmd/orchestrator/main.go into bin/boot-orchestrator..."
    echo "======================================================================"
    mkdir -p "${SCRIPT_DIR:?}/bin" "$LOCAL_TMP"
    rm -f "$BIN_PATH" 2>/dev/null || true

    # Redirect TMPDIR to physical disk storage to prevent tmpfs RAM exhaustion
    TMPDIR="$LOCAL_TMP" go build -ldflags="-s -w" -o "$BIN_PATH" "${SCRIPT_DIR:?}/cmd/orchestrator/main.go"
    rm -rf "$LOCAL_TMP" 2>/dev/null || true

    echo "=> Compilation successful."
fi

chmod 755 "$BIN_PATH" 2>/dev/null || chmod +x "$BIN_PATH"

# 7. Elevate only now, only to run the already-built binary (it performs
# partition/bootloader operations that genuinely require root). Nothing
# before this point ran, or needed to run, as root.
echo "======================================================================"
echo "=> Launching Universal Boot Orchestrator..."
echo "======================================================================"
if [ "$EUID" -ne 0 ]; then
    echo " [SECURITY NOTICE] Hardware block operations require root privileges."
    echo " Authenticating sudo session..."
    if [ -n "$DISPLAY" ]; then
        xhost +si:localuser:root 2>/dev/null || true
    fi
    exec sudo --preserve-env=PATH,DISPLAY,XAUTHORITY "$BIN_PATH"
fi
exec "$BIN_PATH"
