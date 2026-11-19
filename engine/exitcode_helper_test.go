package engine

import (
	"fmt"
	"os/exec"
)

// fakeExitError returns a genuine *exec.ExitError reporting exit code code,
// produced by actually running `sh -c "exit N"` — a real, safe, no-op
// subprocess. Used so mocks can simulate exact exit codes for exitCodeOf
// without needing a hand-rolled error type. Returns nil for code 0.
//
// Deliberately kept in its own, untagged file (not partition_test.go,
// where it originally lived): partition_test.go is now //go:build linux
// only, but preflight_safety_test.go — which runs on every platform —
// also calls this helper, so it must stay reachable regardless of
// partition_test.go's tag.
func fakeExitError(code int) error {
	return exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
}
