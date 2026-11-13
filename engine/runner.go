package engine

import "os/exec"

// Runner abstracts external command execution so engine logic that shells
// out to disk/bootloader tools (parted, resize2fs, e2fsck, blkid, ...) can be
// exercised by tests without invoking real system binaries or touching real
// hardware. Commands that need to write to Stdin (e.g. `dd`, or `parted`
// invocations piping a confirmation) are not modeled here and continue to
// use exec.Command directly, since this interface intentionally stays
// minimal.
type Runner interface {
	// Run executes name with args and returns its combined stdout+stderr
	// output, mirroring exec.Command(...).CombinedOutput().
	Run(name string, args ...string) ([]byte, error)
}

// execRunner is the real, production Runner backed by exec.Command.
type execRunner struct{}

func (execRunner) Run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// DefaultRunner is the real command runner used outside of tests.
var DefaultRunner Runner = execRunner{}

// cmdRunner is the active Runner used by engine package code. Tests may
// replace it with a mock and must restore it (e.g. via t.Cleanup) when done,
// since it is shared process-wide state.
var cmdRunner Runner = DefaultRunner
