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
#
#  Follow-up F6: the function definitions below (and the variable setup that
#  precedes them) are structured so this file can be `source`d — e.g. by a
#  test — without running the `main` action sequence at the bottom, which
#  only fires when the script is executed directly.
#
#  Follow-up F10: SCRIPT_DIR is ALWAYS computed from BASH_SOURCE and never
#  trusts an ambient environment variable of the same name — this script
#  later does `rm -rf "${SCRIPT_DIR:?}/bin"`, `git gc --aggressive`, and
#  other destructive operations scoped to it, so honoring a plain
#  "SCRIPT_DIR" from whatever happened to be in the caller's environment
#  (accidental collision with another tool, or a hostile one) would let
#  that value redirect --hard's purge at an arbitrary directory. The only
#  way to override it is the explicit, narrowly-named
#  ORCH_AUTORUN_TESTING=1 + ORCH_TEST_SCRIPT_DIR pair, which a test sets on
#  purpose and nothing else is likely to set by accident.
# ==============================================================================

set -e

if [ "${ORCH_AUTORUN_TESTING:-0}" = "1" ] && [ -n "${ORCH_TEST_SCRIPT_DIR:-}" ]; then
    SCRIPT_DIR="$ORCH_TEST_SCRIPT_DIR"
else
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
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
# Autonomous Host Prerequisites Assertion Layer
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

# Cache Purge Routine — runs as the invoking user; never needs root.
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

BIN_PATH="${SCRIPT_DIR:?}/bin/boot-orchestrator"
LOCAL_TMP="${SCRIPT_DIR:?}/.build_tmp"

# check_root_owned_files (follow-ups F5/F6/F11/F12). Since the build runs
# as the invoking user, root-owned content left by a run from before item
# 1D.9's fix — or by someone directly invoking `sudo ./autorun.sh` by hand
# — would otherwise fail with a bare, confusing "permission denied".
#
# Directories the root-run binary (or an old root-run build) can leave
# root-owned (bin/, .runtime/) are hard-blocked: removing or writing inside
# a root-owned directory genuinely fails for a non-root user, and --hard's
# purge_cache does exactly that (`rm -rf`) to both.
#
# Follow-up F12: this script no longer checks the journal
# (orchestrator_journal.json) or the installed-OS record at all. Both now
# live outside the repository by default, in a dedicated state directory
# (see safety.DefaultJournalPath in the Go source) that an unprivileged
# user cannot write to in the first place — the journal records exactly
# the state a LATER elevated run trusts to decide what to do to real
# hardware, so it must never be something this script hands back to the
# invoking user, the way bin/ legitimately is.
check_root_owned_files() {
    if [ "$EUID" -eq 0 ]; then
        return 0
    fi

    local blocking_dirs=("${SCRIPT_DIR:?}/bin" "${SCRIPT_DIR:?}/.runtime")
    local dir_offenders=()
    for d in "${blocking_dirs[@]}"; do
        if [ -d "$d" ]; then
            while IFS= read -r -d '' f; do
                dir_offenders+=("$f")
            done < <(find "$d" -user root -print0 2>/dev/null)
        fi
    done

    if [ ${#dir_offenders[@]} -gt 0 ]; then
        echo "======================================================================" >&2
        echo " [BLOCKED] Found root-owned file(s) from a previous run:" >&2
        printf '   %s\n' "${dir_offenders[@]}" >&2
        echo "" >&2
        echo " This script runs as your own user and cannot write to, or purge," >&2
        echo " these. Fix ownership, then re-run:" >&2
        echo "" >&2
        printf '   sudo chown -R "%s:%s" "%s" "%s"\n' "$(id -un)" "$(id -gn)" "${blocking_dirs[0]}" "${blocking_dirs[1]}" >&2
        echo "======================================================================" >&2
        exit 1
    fi
}

# needs_rebuild (banner follow-up, Part A). Before this, the ONLY rebuild
# trigger anywhere in this script was `[ ! -f "$BIN_PATH" ]` — once a
# binary existed from any previous build, autorun.sh launched it
# unconditionally, forever, even after real source changes (a new
# banner, a bug fix, anything) until the caller remembered to pass
# -f/--hard by hand. That is the exact bug reported: the new banner only
# appeared after a manual rebuild.
#
# Checks two independent signals, either of which means the binary is
# stale; sets REBUILD_REASON as a side effect for the one-line build
# message below.
#   1. Any .go file, go.mod, or an embedded asset (ui/*.txt, go:embed'd
#      by ui/banner.go) has a modification time newer than the binary's.
#   2. The binary's own embedded build commit (queried via
#      --print-build-commit, cmd/orchestrator/version.go) differs from
#      the working tree's current HEAD. This catches cases (1) misses —
#      a `git pull`/checkout that changes file CONTENT but not mtimes in
#      a way find can see reliably across all filesystems, or a binary
#      built from a different commit than what's currently checked out.
#      Skipped gracefully (not an error) when git isn't available or
#      this isn't a git checkout at all (e.g. a source tarball).
needs_rebuild() {
    REBUILD_REASON=""

    if [ ! -f "$BIN_PATH" ]; then
        REBUILD_REASON="no existing binary at $BIN_PATH"
        return 0
    fi

    local newer_file
    newer_file=$(find "${SCRIPT_DIR:?}" \
        -path '*/.git' -prune -o \
        \( -name '*.go' -o -name 'go.mod' -o -path '*/ui/*.txt' \) \
        -newer "$BIN_PATH" -print \
        2>/dev/null | head -n 1)
    if [ -n "$newer_file" ]; then
        REBUILD_REASON="$newer_file is newer than the built binary"
        return 0
    fi

    if command -v git >/dev/null 2>&1 && [ -d "${SCRIPT_DIR:?}/.git" ]; then
        local current_commit built_commit
        current_commit="$(git -C "${SCRIPT_DIR:?}" rev-parse HEAD 2>/dev/null || true)"
        built_commit="$("$BIN_PATH" --print-build-commit 2>/dev/null || true)"
        if [ -n "$current_commit" ] && [ "$current_commit" != "$built_commit" ]; then
            REBUILD_REASON="built commit (${built_commit:-unknown, likely an older binary built before this check existed}) differs from the current HEAD ($current_commit)"
            return 0
        fi
    fi

    return 1
}

main() {
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

    # Follow-up F11: must run before flag processing/purge_cache, not after
    # — --hard's purge_cache does `rm -rf` on bin/ and .runtime/
    # unconditionally, which fails with a raw, confusing permission error
    # if either is root-owned. Checking first means --hard always gets a
    # clear message instead.
    check_root_owned_files

    assert_host_dependencies

    # Process Operational Flags
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

    # Dependency Sync & Build — runs as the invoking user (never root), so
    # no build artifact, module cache entry, or compiled binary is ever
    # root-owned (audit 1D.9).
    REBUILD_NEEDED=0
    if [ "$FORCE_REBUILD" -eq 1 ]; then
        REBUILD_NEEDED=1
        REBUILD_REASON="forced rebuild requested (-f/--force/--hard)"
    elif needs_rebuild; then
        REBUILD_NEEDED=1
    fi

    if [ "$REBUILD_NEEDED" -eq 1 ]; then
        echo "=> [BUILD] building... ($REBUILD_REASON)"

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

        # Embeds the exact commit this binary was built from
        # (cmd/orchestrator/version.go's BuildCommit) so a LATER run of
        # this same script can tell whether the checked-out source has
        # since moved on — see needs_rebuild above. "unknown" when this
        # isn't a git checkout at all (e.g. a source tarball); that
        # disables the commit-based check gracefully (needs_rebuild
        # already requires current_commit to be non-empty before
        # comparing), relying on the file-mtime check alone instead.
        BUILD_COMMIT="unknown"
        if command -v git >/dev/null 2>&1 && [ -d "${SCRIPT_DIR:?}/.git" ]; then
            BUILD_COMMIT="$(git -C "${SCRIPT_DIR:?}" rev-parse HEAD 2>/dev/null || echo unknown)"
        fi

        # Builds the whole cmd/orchestrator PACKAGE (a directory), not a
        # single file: `go build pkg/main.go` compiles ONLY that one
        # file, silently excluding every sibling file in the same
        # package (headless.go, revert.go, version.go — this is what
        # "undefined: BuildCommit"/"undefined: runHeadless" look like
        # when hit; found and fixed as part of this same follow-up, since
        # the rebuild this script now correctly triggers would otherwise
        # immediately fail to compile at all). Redirect TMPDIR to
        # physical disk storage to prevent tmpfs RAM exhaustion.
        TMPDIR="$LOCAL_TMP" go build -ldflags="-s -w -X main.BuildCommit=$BUILD_COMMIT" -o "$BIN_PATH" "${SCRIPT_DIR:?}/cmd/orchestrator"
        rm -rf "$LOCAL_TMP" 2>/dev/null || true

        echo "=> Compilation successful."
    fi

    chmod 755 "$BIN_PATH" 2>/dev/null || chmod +x "$BIN_PATH"

    # Elevate only now, only to run the already-built binary (it performs
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
}

# Only run main when this script is executed directly, not when it is
# `source`d (e.g. by a test that wants the function definitions above
# without the actual build/launch sequence).
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    main "$@"
fi
