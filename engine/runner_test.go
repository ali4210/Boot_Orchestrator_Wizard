package engine

import (
	"errors"
	"strings"
)

// errMockFailure is returned by mockRunner for any command name listed in
// FailNames, or that Handler decides should fail.
var errMockFailure = errors.New("mock runner: simulated command failure")

// mockRunner is a test double for Runner, used so engine tests never shell
// out to real system binaries or touch real disks.
//
// Run and RunStdout are deliberately independent: Handler/FailNames back
// Run (combined stdout+stderr), while StdoutHandler backs RunStdout. If
// StdoutHandler is nil, RunStdout falls back to Handler/FailNames too, so
// most tests can ignore the distinction — but a test that wants to prove a
// call site is immune to stderr noise (because it uses RunStdout) sets
// Handler to return noisy combined output and StdoutHandler to return the
// clean value, then asserts the parsed result matches the clean one.
type mockRunner struct {
	Handler       func(name string, args []string) ([]byte, error)
	StdoutHandler func(name string, args []string) ([]byte, error)
	FailNames     map[string]bool
	Calls         []string
	StdoutCalls   []string
}

func (m *mockRunner) Run(name string, args ...string) ([]byte, error) {
	m.Calls = append(m.Calls, strings.Join(append([]string{name}, args...), " "))
	if m.Handler != nil {
		return m.Handler(name, args)
	}
	if m.FailNames[name] {
		return []byte("mock: simulated failure"), errMockFailure
	}
	return []byte(""), nil
}

func (m *mockRunner) RunStdout(name string, args ...string) ([]byte, error) {
	m.StdoutCalls = append(m.StdoutCalls, strings.Join(append([]string{name}, args...), " "))
	if m.StdoutHandler != nil {
		return m.StdoutHandler(name, args)
	}
	if m.Handler != nil {
		return m.Handler(name, args)
	}
	if m.FailNames[name] {
		return []byte("mock: simulated failure"), errMockFailure
	}
	return []byte(""), nil
}

func containsAll(haystack []string, needles ...string) bool {
	for _, n := range needles {
		found := false
		for _, h := range haystack {
			if h == n {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
