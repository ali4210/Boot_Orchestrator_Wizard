package engine

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

func withEnv(t *testing.T, key, value string) {
	t.Helper()
	orig, had := os.LookupEnv(key)
	t.Cleanup(func() {
		if had {
			os.Setenv(key, orig)
		} else {
			os.Unsetenv(key)
		}
	})
	if value == "" {
		os.Unsetenv(key)
	} else {
		os.Setenv(key, value)
	}
}

// TestGetRealUserHome_ResolvesViaRealPasswdLookup is the regression test
// for bug H's core finding: GetRealUserHome used to guess
// filepath.Join("/home", sudoUser) instead of looking up the account's
// actual home directory. Uses the current real user as SUDO_USER (safe —
// read-only os/user.Lookup, never touches /root or any other real
// account) to prove the resolved path matches what the passwd database
// actually says, not a guessed pattern.
func TestGetRealUserHome_ResolvesViaRealPasswdLookup(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Skipf("user.Current unavailable in this environment: %v", err)
	}
	if current.Username == "" || current.Username == "root" {
		t.Skip("current user is root or unnamed, not a meaningful case for this test")
	}

	withEnv(t, "SUDO_USER", current.Username)

	got := GetRealUserHome()
	if got != current.HomeDir {
		t.Fatalf("GetRealUserHome() = %q, want the real passwd-database home %q", got, current.HomeDir)
	}
}

// TestGetRealUserHome_NeverReturnsRootsHomeWhenSudoUserIsSet proves the
// literal bug report: running under sudo must not make the picker browse
// /root.
func TestGetRealUserHome_NeverReturnsRootsHomeWhenSudoUserIsSet(t *testing.T) {
	current, err := user.Current()
	if err != nil || current.Username == "" || current.Username == "root" {
		t.Skip("no non-root current user available in this environment")
	}
	withEnv(t, "SUDO_USER", current.Username)

	got := GetRealUserHome()
	if got == "/root" {
		t.Fatalf("GetRealUserHome() returned /root while SUDO_USER=%s was set", current.Username)
	}
}

// TestGetRealUserHome_UsesLookupHomeNotGuessedPath is the precise
// regression test for bug H: before the fix, GetRealUserHome guessed
// filepath.Join("/home", sudoUser) instead of consulting the real passwd
// entry. A fake userLookupFunc here simulates an account whose real home
// is NOT under /home/<name> at all (e.g. a custom `useradd -d`) — no real
// system user is created or deleted. The resolved path must be the
// looked-up home, not the guessed one.
func TestGetRealUserHome_UsesLookupHomeNotGuessedPath(t *testing.T) {
	customHome := t.TempDir() // deliberately NOT under /home
	origLookup := userLookupFunc
	t.Cleanup(func() { userLookupFunc = origLookup })
	userLookupFunc = func(username string) (*user.User, error) {
		if username == "customhomeuser" {
			return &user.User{Username: username, HomeDir: customHome}, nil
		}
		return origLookup(username)
	}
	withEnv(t, "SUDO_USER", "customhomeuser")

	got := GetRealUserHome()
	if got != customHome {
		t.Fatalf("GetRealUserHome() = %q, want the looked-up home %q (not a guessed /home/customhomeuser)", got, customHome)
	}
	guessed := filepath.Join("/home", "customhomeuser")
	if got == guessed {
		t.Fatalf("GetRealUserHome() returned the guessed path %q instead of the real looked-up home", guessed)
	}
}

// TestGetRealUserHome_FallsBackSafelyForAnUnknownUser proves a SUDO_USER
// naming an account that doesn't exist at all (e.g. a stale/corrupted
// environment) doesn't crash and doesn't fabricate a guessed path for a
// nonexistent account — it falls through to the next, real signal instead.
func TestGetRealUserHome_FallsBackSafelyForAnUnknownUser(t *testing.T) {
	withEnv(t, "SUDO_USER", "this-account-definitely-does-not-exist-12345")

	got := GetRealUserHome()
	if got == filepath.Join("/home", "this-account-definitely-does-not-exist-12345") {
		t.Fatalf("GetRealUserHome() fabricated a guessed path for a nonexistent account: %q", got)
	}
}

// TestListDirectoryContents_UsesRealUserHomeNotRoot is the regression
// test for bug H at the ListDirectoryContents layer (what the TUI's
// interactive directory-browsing picker actually calls): an empty dirPath
// (the picker's initial state) must resolve to the real invoking user's
// home, not /root, when SUDO_USER is set.
func TestListDirectoryContents_UsesRealUserHomeNotRoot(t *testing.T) {
	current, err := user.Current()
	if err != nil || current.Username == "" || current.Username == "root" {
		t.Skip("no non-root current user available in this environment")
	}
	withEnv(t, "SUDO_USER", current.Username)

	items, err := ListDirectoryContents("", false)
	if err != nil {
		t.Fatalf("ListDirectoryContents: %v", err)
	}
	// Every non-".." entry should be rooted under the real home directory,
	// never under /root.
	for _, item := range items {
		if item.Name == ".." {
			continue
		}
		if filepath.Dir(item.Path) == "/root" {
			t.Fatalf("ListDirectoryContents(\"\", false) browsed /root instead of the real user's home, got entry: %+v", item)
		}
	}
}

// TestListDirectoryContents_DirectISOSelectionBypassesHomeResolution is
// the "both selection modes" half of bug H's test requirement: a direct,
// explicit path to a specific file (the --iso/--iso-path CLI flow, or a
// manually entered path in the TUI) must be used exactly as given,
// independent of SUDO_USER/home resolution — GetRealUserHome is only ever
// consulted for the empty/unset "start browsing from home" case.
func TestListDirectoryContents_DirectISOSelectionBypassesHomeResolution(t *testing.T) {
	dir := t.TempDir()
	isoPath := filepath.Join(dir, "custom.iso")
	if err := os.WriteFile(isoPath, []byte("fake-iso-bytes"), 0644); err != nil {
		t.Fatalf("writing fixture iso: %v", err)
	}

	// Even with SUDO_USER unset/empty (simulating "no sudo context at
	// all"), listing the directory containing a direct ISO selection must
	// work exactly the same, proving this path never depends on
	// GetRealUserHome's resolution.
	withEnv(t, "SUDO_USER", "")

	items, err := ListDirectoryContents(dir, true)
	if err != nil {
		t.Fatalf("ListDirectoryContents(%q): %v", dir, err)
	}
	found := false
	for _, item := range items {
		if item.Path == isoPath {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the directly-specified ISO file to appear in the listing, got: %+v", items)
	}
}

// TestIsSupportedPayload covers the extension-matching logic direct .iso
// selection relies on to validate a manually entered or CLI-provided path.
func TestIsSupportedPayload(t *testing.T) {
	cases := map[string]bool{
		"ubuntu-24.04.iso":    true,
		"image.IMG":           true,
		"rootfs.tar.gz":       true,
		"archive.tar.xz":      true,
		"notes.txt":           false,
		"no-extension-at-all": false,
	}
	for name, want := range cases {
		if got := IsSupportedPayload(name); got != want {
			t.Errorf("IsSupportedPayload(%q) = %v, want %v", name, got, want)
		}
	}
}
