package engine

import (
	"strings"
	"testing"
)

func withMockRunner(t *testing.T, mock *mockRunner) {
	t.Helper()
	orig := cmdRunner
	t.Cleanup(func() { cmdRunner = orig })
	cmdRunner = mock
}

// blkidTypeMock returns a mockRunner whose RunStdout("blkid", ...) answers
// per-device from typeByDevice (device path -> blkid TYPE value), and whose
// RunStdout("lsblk", ...) reports allDevices as the disk's partition list.
func blkidTypeMock(allDevices []string, typeByDevice map[string]string) *mockRunner {
	return &mockRunner{
		StdoutHandler: func(name string, args []string) ([]byte, error) {
			switch name {
			case "lsblk":
				return []byte(strings.Join(allDevices, "\n") + "\n"), nil
			case "blkid":
				dev := args[len(args)-1]
				return []byte(typeByDevice[dev] + "\n"), nil
			case "mokutil":
				return []byte(""), nil
			}
			return []byte(""), nil
		},
	}
}

func TestPreflightStorageSafetyCheck_AbortsOnLVM(t *testing.T) {
	withMockRunner(t, blkidTypeMock(
		[]string{"/dev/sdz", "/dev/sdz1", "/dev/sdz2"},
		map[string]string{"/dev/sdz2": "LVM2_member"},
	))

	err := PreflightStorageSafetyCheck("/dev/sdz")
	if err == nil {
		t.Fatalf("expected PreflightStorageSafetyCheck to abort on an LVM physical volume")
	}
	if !strings.Contains(err.Error(), "LVM") {
		t.Fatalf("expected the error to name LVM, got: %v", err)
	}
}

func TestPreflightStorageSafetyCheck_AbortsOnLUKS(t *testing.T) {
	withMockRunner(t, blkidTypeMock(
		[]string{"/dev/sdz", "/dev/sdz1"},
		map[string]string{"/dev/sdz1": "crypto_LUKS"},
	))

	err := PreflightStorageSafetyCheck("/dev/sdz")
	if err == nil {
		t.Fatalf("expected PreflightStorageSafetyCheck to abort on a LUKS volume")
	}
	if !strings.Contains(err.Error(), "LUKS") {
		t.Fatalf("expected the error to name LUKS, got: %v", err)
	}
}

func TestPreflightStorageSafetyCheck_AbortsOnMdraid(t *testing.T) {
	withMockRunner(t, blkidTypeMock(
		[]string{"/dev/sdz", "/dev/sdz1"},
		map[string]string{"/dev/sdz1": "linux_raid_member"},
	))

	err := PreflightStorageSafetyCheck("/dev/sdz")
	if err == nil {
		t.Fatalf("expected PreflightStorageSafetyCheck to abort on an mdraid member")
	}
	if !strings.Contains(err.Error(), "RAID") {
		t.Fatalf("expected the error to name RAID, got: %v", err)
	}
}

func TestPreflightStorageSafetyCheck_AbortsOnBitLocker(t *testing.T) {
	withMockRunner(t, blkidTypeMock(
		[]string{"/dev/sdz", "/dev/sdz1"},
		map[string]string{"/dev/sdz1": "BitLocker"},
	))

	err := PreflightStorageSafetyCheck("/dev/sdz")
	if err == nil {
		t.Fatalf("expected PreflightStorageSafetyCheck to abort on a BitLocker volume")
	}
	if !strings.Contains(err.Error(), "BitLocker") {
		t.Fatalf("expected the error to name BitLocker, got: %v", err)
	}
}

func TestPreflightStorageSafetyCheck_PassesOnPlainExt4(t *testing.T) {
	withMockRunner(t, blkidTypeMock(
		[]string{"/dev/sdz", "/dev/sdz1", "/dev/sdz2"},
		map[string]string{"/dev/sdz1": "vfat", "/dev/sdz2": "ext4"},
	))

	if err := PreflightStorageSafetyCheck("/dev/sdz"); err != nil {
		t.Fatalf("expected a plain ext4/vfat disk to pass, got: %v", err)
	}
}

// TestPreflightStorageSafetyCheck_BlkidExitCode2DoesNotAbort is the
// regression test for follow-up F2: blkid exits 2 (with empty stdout) when
// a partition has no recognized filesystem at all — a normal, common case
// (e.g. an unformatted or freshly-created partition), not a sign of
// LVM/LUKS/mdraid/BitLocker. This must never be treated as a dangerous
// finding; detectDangerousStorage's "blkid error -> skip, don't flag" path
// must keep working for this exact real-world exit code, not just any
// error.
func TestPreflightStorageSafetyCheck_BlkidExitCode2DoesNotAbort(t *testing.T) {
	withMockRunner(t, &mockRunner{
		StdoutHandler: func(name string, args []string) ([]byte, error) {
			switch name {
			case "lsblk":
				return []byte("/dev/sdz\n/dev/sdz1\n"), nil
			case "blkid":
				// Real blkid: exit code 2, no stdout, when it finds no
				// recognized superblock/filesystem on the device.
				return []byte(""), fakeExitError(2)
			case "mokutil":
				return []byte(""), nil
			}
			return []byte(""), nil
		},
	})

	if err := PreflightStorageSafetyCheck("/dev/sdz"); err != nil {
		t.Fatalf("expected blkid exit code 2 (no filesystem) to be treated as a non-finding, got: %v", err)
	}
}

func TestPreflightStorageSafetyCheck_RefusesEmptyDisk(t *testing.T) {
	if err := PreflightStorageSafetyCheck(""); err == nil {
		t.Fatalf("expected an empty disk path to be refused")
	}
}

func TestDetectSecureBootAndTPM_WarnsWhenSecureBootEnabled(t *testing.T) {
	withMockRunner(t, &mockRunner{
		StdoutHandler: func(name string, args []string) ([]byte, error) {
			if name == "mokutil" {
				return []byte("SecureBoot enabled\n"), nil
			}
			return []byte(""), nil
		},
	})

	secureBootOn, _, warnings := DetectSecureBootAndTPM()
	if !secureBootOn {
		t.Fatalf("expected Secure Boot to be reported as enabled")
	}
	if len(warnings) == 0 {
		t.Fatalf("expected at least one warning when Secure Boot is enabled")
	}
}

func TestDetectSecureBootAndTPM_NoWarningsWhenDisabledAndNoTPM(t *testing.T) {
	withMockRunner(t, &mockRunner{
		StdoutHandler: func(name string, args []string) ([]byte, error) {
			if name == "mokutil" {
				return []byte("SecureBoot disabled\n"), nil
			}
			return []byte(""), nil
		},
	})

	secureBootOn, _, warnings := DetectSecureBootAndTPM()
	if secureBootOn {
		t.Fatalf("expected Secure Boot to be reported as disabled")
	}
	// TPM presence depends on /sys/class/tpm/tpm0 on the host running the
	// test, which this test does not control; only assert Secure Boot's
	// contribution is absent.
	for _, w := range warnings {
		if strings.Contains(w, "Secure Boot") {
			t.Fatalf("did not expect a Secure Boot warning, got: %v", warnings)
		}
	}
}
