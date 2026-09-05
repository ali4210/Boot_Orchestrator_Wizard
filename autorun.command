#!/bin/bash
# ==============================================================================
#  autorun.command — Universal Boot Orchestrator macOS Runtime Launcher
#  Self-elevating macOS launcher using native AppleScript privileges.
# ==============================================================================

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
cd "$DIR"

# 1. Require Root / Administrator Privileges
if [ "$EUID" -ne 0 ]; then
    echo "======================================================================"
    echo " [SECURITY NOTICE] Hardware block operations require root privileges."
    echo " Requesting Administrator elevation via macOS security dialog..."
    echo "======================================================================"
    osascript -e "do shell script \"cd '$DIR' && ./autorun.command\" with administrator privileges"
    exit $?
fi

echo "======================================================================"
echo " UNIVERSAL OS BOOT ORCHESTRATOR :: MACOS (DARWIN) RUNTIME"
echo " Privilege Status: Root / Administrator Verified"
echo "======================================================================"

# 2. Check for pre-compiled binary
BIN_PATH="$DIR/bin/boot-orchestrator-darwin"

if [ -f "$BIN_PATH" ]; then
    echo "=> Native macOS engine detected: $BIN_PATH"
    chmod +x "$BIN_PATH"
    exec "$BIN_PATH"
fi

# 3. Fallback: Compile from Source
echo "=> Compiling cmd/orchestrator for macOS..."
export CGO_ENABLED=0
mkdir -p "$DIR/bin"
go build -ldflags="-s -w" -o "$BIN_PATH" ./cmd/orchestrator
chmod +x "$BIN_PATH"

echo "=> Launching orchestrator..."
exec "$BIN_PATH"
