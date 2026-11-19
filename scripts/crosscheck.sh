#!/usr/bin/env bash
# crosscheck.sh — cross-platform build+vet verification.
#
# Why this exists: a 2026-11 CI run (PR #1, run 44501105882) failed
# `go build ./...` on windows-latest and macos-latest with "imported and
# not used" errors in engine_windows.go/revert_windows.go/
# engine_darwin.go/revert_darwin.go. The local development sandbox this
# project was built in is Linux-only, and `go build ./...`/`go vet ./...`
# run there never compile files gated by `//go:build windows`/
# `//go:build darwin` at all — so a large exec.Command-to-execCommand
# rename across ~20 files (closing audit 2.4) silently left four unused
# imports in GOOS-gated files nothing on that machine ever tried to
# compile, and the project's own "go build/vet/test must pass before
# every commit" gate never had a chance to catch it. This script is the
# fix for the PROCESS gap, not just the four imports: it runs the same
# build+vet matrix a contributor would otherwise only discover was broken
# when CI's real windows-latest/macos-latest runners hit it.
#
# Usage: ./scripts/crosscheck.sh  (or: make crosscheck)
# Exits non-zero (and prints every failure, not just the first) if any
# platform/arch combination fails to build or vet cleanly. Cross-compiling
# never needs the target OS/arch actually present — `go build`/`go vet`
# under GOOS/GOARCH only type-checks and compiles, it never runs anything
# — so this runs anywhere `go` is installed, including this Linux
# sandbox, without weakening any build tag or skipping any real check.
set -u

cd "$(dirname "${BASH_SOURCE[0]}")/.."

# The exact matrix requested: GOOS=windows (amd64, the default/only arch
# this project ships for on Windows), GOOS=darwin for both amd64 and
# arm64 (Apple Silicon), and GOOS=linux GOARCH=arm64 (this project's one
# non-native-host Linux target). GOOS=linux GOARCH=amd64 (the sandbox's
# own native platform) is deliberately not repeated here — the existing
# `go build ./... && go vet ./... && go test ./...` gate already covers
# it on every commit.
TARGETS=(
  "windows amd64"
  "darwin amd64"
  "darwin arm64"
  "linux arm64"
)

failures=0

for target in "${TARGETS[@]}"; do
  read -r os arch <<<"$target"
  echo "======================================================================"
  echo " GOOS=$os GOARCH=$arch"
  echo "======================================================================"

  if ! GOOS="$os" GOARCH="$arch" go build ./...; then
    echo "[FAIL] GOOS=$os GOARCH=$arch: go build ./..."
    failures=$((failures + 1))
  fi

  if ! GOOS="$os" GOARCH="$arch" go vet ./...; then
    echo "[FAIL] GOOS=$os GOARCH=$arch: go vet ./..."
    failures=$((failures + 1))
  fi
done

echo "======================================================================"
if [ "$failures" -eq 0 ]; then
  echo "[OK] all ${#TARGETS[@]} platform/arch targets built and vetted cleanly."
  exit 0
fi

echo "[FAIL] $failures build/vet failure(s) above across ${#TARGETS[@]} targets."
exit 1
