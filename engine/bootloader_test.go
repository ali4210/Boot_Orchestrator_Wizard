package engine

import "testing"

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
