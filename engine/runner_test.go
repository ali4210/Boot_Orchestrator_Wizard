package engine

import (
	"errors"
	"strings"
)

// errMockFailure is returned by mockRunner for any command name listed in
// FailNames, or that Handler decides should fail.
var errMockFailure = errors.New("mock runner: simulated command failure")

// mockRunner is a test double for Runner, used so engine tests never shell
// out to real system binaries or touch real disks. If Handler is set, it is
// consulted for every call; otherwise any command name present in FailNames
// returns errMockFailure and everything else succeeds with empty output.
type mockRunner struct {
	Handler   func(name string, args []string) ([]byte, error)
	FailNames map[string]bool
	Calls     []string
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
