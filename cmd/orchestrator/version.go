package main

// Version identifies this build for display (the banner wordmark's
// subtitle, see ui/banner.go) and for diagnostics. This project has no
// other version source (no git tags, VERSION file, or CHANGELOG as of
// this writing) — "dev" is the honest default for a build that didn't
// set this explicitly. Override at build time:
//
//	go build -ldflags "-X main.Version=v1.2.3" ./cmd/orchestrator
var Version = "dev"

// BuildCommit is the exact git commit this binary was built from,
// embedded the same way:
//
//	go build -ldflags "-X main.BuildCommit=$(git rev-parse HEAD)" ./cmd/orchestrator
//
// Empty when built without that flag (e.g. a bare `go build`/`go run`
// during local development). autorun.sh sets this on every build and
// compares it against the working tree's current HEAD to decide whether
// the binary is stale — see printBuildCommitFlag below and
// scripts/needs_rebuild.sh.
var BuildCommit = ""
