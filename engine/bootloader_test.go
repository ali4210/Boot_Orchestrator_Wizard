package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests prove that queryFilesystemType/queryPartitionUUID parse
// blkid's output via RunStdout (stdout only), not Run (combined
// stdout+stderr). The mock's Handler (backing Run) returns noisy,
// multi-line combined output that would corrupt a strings.TrimSpace-based
// exact-value parse; StdoutHandler (backing RunStdout) returns the clean
// value. If either production function were ever changed back to call
// Run instead of RunStdout, these tests would fail on the noisy value.

func TestQueryPartitionUUID_UsesStdoutOnly(t *testing.T) {
	origRunner := cmdRunner
	t.Cleanup(func() { cmdRunner = origRunner })
	cmdRunner = &mockRunner{
		Handler: func(name string, args []string) ([]byte, error) {
			return []byte("ABCD-1234-NOISE\nblkid: a stray stderr warning\n"), nil
		},
		StdoutHandler: func(name string, args []string) ([]byte, error) {
			return []byte("ABCD-1234-NOISE\n"), nil
		},
	}

	const want = "ABCD-1234-NOISE"
	if got := queryPartitionUUID("/dev/sdz1"); got != want {
		t.Fatalf("queryPartitionUUID: got %q, want %q (must use RunStdout, not Run)", got, want)
	}
}

func TestQueryFilesystemType_UsesStdoutOnly(t *testing.T) {
	origRunner := cmdRunner
	t.Cleanup(func() { cmdRunner = origRunner })
	cmdRunner = &mockRunner{
		Handler: func(name string, args []string) ([]byte, error) {
			return []byte("ntfs\nblkid: a stray stderr warning\n"), nil
		},
		StdoutHandler: func(name string, args []string) ([]byte, error) {
			return []byte("ntfs\n"), nil
		},
	}

	const want = "ntfs"
	if got := queryFilesystemType("/dev/sdz1"); got != want {
		t.Fatalf("queryFilesystemType: got %q, want %q (must use RunStdout, not Run)", got, want)
	}
}

// TestAssertDefaultCredentials_NoHardcodedSaleem is the regression test for
// audit 2.3: the account-provisioning script generated for the chroot must
// reflect whatever username the user actually entered, and must never
// contain the old hardcoded "saleem" account under any circumstance.
func TestAssertDefaultCredentials_NoHardcodedSaleem(t *testing.T) {
	origRunner := cmdRunner
	t.Cleanup(func() { cmdRunner = origRunner })

	var capturedScript string
	mock := &mockRunner{
		Handler: func(name string, args []string) ([]byte, error) {
			if name == "chroot" {
				// cmdRunner.Run("chroot", rootPath, "/bin/sh", "-c", script)
				for i, a := range args {
					if a == "-c" && i+1 < len(args) {
						capturedScript = args[i+1]
					}
				}
			}
			return []byte("ORCH_ACCOUNT_VERIFIED admin_group=sudo\n"), nil
		},
	}
	cmdRunner = mock

	rootPath := t.TempDir()
	creds := Credentials{Username: "janedoe", Password: "a-fine-password", AllowRootLogin: true}

	if err := assertDefaultCredentials(rootPath, creds, nil); err != nil {
		t.Fatalf("assertDefaultCredentials: %v", err)
	}

	if capturedScript == "" {
		t.Fatalf("expected a chroot script to have been captured, calls=%v", mock.Calls)
	}
	if strings.Contains(capturedScript, "saleem") {
		t.Fatalf("generated provisioning script still contains the hardcoded account:\n%s", capturedScript)
	}
	if !strings.Contains(capturedScript, "janedoe") {
		t.Fatalf("generated provisioning script does not reference the configured username %q:\n%s", creds.Username, capturedScript)
	}

	sudoersPath := filepath.Join(rootPath, "etc", "sudoers.d", "janedoe")
	if _, statErr := os.Stat(sudoersPath); statErr != nil {
		t.Fatalf("expected a sudoers.d entry for %q, stat err: %v", creds.Username, statErr)
	}
}

// TestAssertDefaultCredentials_FailsLoudlyWhenAccountNotVerified is the
// Go-level regression test for audit 1A.3's "fail loudly if missing"
// requirement: before this fix, assertDefaultCredentials returned nil
// unconditionally as long as the chroot command itself didn't error,
// regardless of whether the account was ever actually created. Now, if the
// chroot script's output doesn't contain the ORCH_ACCOUNT_VERIFIED sentinel
// (see renderAccountProvisioningScript), assertDefaultCredentials must
// return a real error so callers can't silently proceed with no usable
// account on the target.
func TestAssertDefaultCredentials_FailsLoudlyWhenAccountNotVerified(t *testing.T) {
	origRunner := cmdRunner
	t.Cleanup(func() { cmdRunner = origRunner })
	cmdRunner = &mockRunner{
		Handler: func(name string, args []string) ([]byte, error) {
			// Simulates the chroot command itself exiting 0 (e.g. a shell
			// quoting bug skipped the verification block entirely) but
			// never printing the success sentinel.
			return []byte("useradd: some warning\n"), nil
		},
	}

	rootPath := t.TempDir()
	creds := Credentials{Username: "janedoe", Password: "a-fine-password"}

	err := assertDefaultCredentials(rootPath, creds, nil)
	if err == nil {
		t.Fatalf("expected assertDefaultCredentials to fail loudly when the account was never verified")
	}
	if !strings.Contains(err.Error(), "janedoe") {
		t.Fatalf("expected the error to name the account, got: %v", err)
	}
}

// TestDebianKernelPackageForArch_MatchesGuestArch is the regression test
// for audit finding 2.6 (x86_64 hardcoded despite ARM64 support) as it
// applies to assertBootableKernelAndPackages' self-heal install: before
// this fix, a guest OS extracted with no kernel present always had
// "linux-image-amd64" apt-get installed into its chroot, which fails
// outright against an arm64 guest's own apt (debian-netboot-arm64,
// ubuntu-server-arm64 — added in item 36), since no such package exists in
// the arm64 repos.
func TestDebianKernelPackageForArch_MatchesGuestArch(t *testing.T) {
	if got := debianKernelPackageForArch("arm64"); got != "linux-image-arm64" {
		t.Errorf("arm64: expected linux-image-arm64, got %q", got)
	}
	if got := debianKernelPackageForArch("amd64"); got != "linux-image-amd64" {
		t.Errorf("amd64: expected linux-image-amd64, got %q", got)
	}
}
