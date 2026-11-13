package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"boot-orchestrator/safety"
)

// partedFreeNoHeadroom simulates `parted ... print free` reporting no
// unallocated space, forcing CarveHardwarePartitionAtBoundary past the
// "carve from free space" shortcut and into the shrink-partition-1 path.
const partedFreeNoHeadroom = "BYT;\n/dev/sdz:1000215216s:scsi:512:512:gpt:Fake;\n"

// partedSinglePartitionPrint simulates `parted ... unit s print` reporting a
// single ext4 partition occupying the whole disk, the layout that triggers
// CarveHardwarePartitionAtBoundary's "Shrink Partition 1" branch.
const partedSinglePartitionPrint = `Model: Fake
Disk /dev/sdz: 1000215216s
Sector size (logical/physical): 512B/512B
Partition Table: gpt
Disk Flags:

Number  Start   End          Size         File system  Name  Flags
 1      2048s   1000213167s  1000211120s  ext4
`

// withScratchMtab points mtabPath at a path inside a fresh temp directory
// for the duration of the test and restores the real path afterwards, so
// ensureMtabSymlink (exercised transitively by CarveHardwarePartitionAtBoundary)
// never touches the real /etc/mtab during tests.
func withScratchMtab(t *testing.T) string {
	t.Helper()
	orig := mtabPath
	t.Cleanup(func() { mtabPath = orig })
	mtabPath = filepath.Join(t.TempDir(), "mtab")
	return mtabPath
}

// shrinkMockConfig controls a mockRunner that simulates every command
// CarveHardwarePartitionAtBoundary's shrink-partition-1 path invokes.
type shrinkMockConfig struct {
	mounted       bool // findmnt reports a mountpoint
	e2fsckFail    bool
	resize2fsFail bool
}

func newShrinkPathMock(cfg shrinkMockConfig) *mockRunner {
	m := &mockRunner{}
	m.Handler = func(name string, args []string) ([]byte, error) {
		switch name {
		case "parted":
			if containsAll(args, "free") {
				return []byte(partedFreeNoHeadroom), nil
			}
			if containsAll(args, "unit", "s", "print") {
				return []byte(partedSinglePartitionPrint), nil
			}
			return []byte(""), nil
		case "blockdev":
			return []byte("512110190592\n"), nil
		case "findmnt":
			if cfg.mounted {
				return []byte("/mnt/fake-target\n"), nil
			}
			return []byte(""), fmt.Errorf("findmnt: no mount found")
		case "e2fsck":
			if cfg.e2fsckFail {
				return []byte("mock: e2fsck found uncorrectable errors"), fmt.Errorf("e2fsck failed on /dev/sdz1: exit status 4")
			}
			return []byte(""), nil
		case "resize2fs":
			if containsAll(args, "-P") {
				return []byte("Estimated minimum size of the filesystem: 1000000\n"), nil
			}
			if cfg.resize2fsFail {
				return []byte("mock: resize2fs failed"), fmt.Errorf("resize2fs failed on /dev/sdz1: exit status 1 (mock: simulated failure)")
			}
			return []byte("resize2fs: ok\n"), nil
		case "dumpe2fs":
			return []byte("Block size:               4096\n"), nil
		}
		return []byte(""), nil
	}
	return m
}

func newTestJournal(t *testing.T) *safety.Journal {
	t.Helper()
	journalPath := filepath.Join(t.TempDir(), "journal.json")
	journal, err := safety.NewJournal(journalPath, "test-txn")
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}
	return journal
}

// TestCarveHardwarePartitionAtBoundary_Resize2fsFailurePropagates is the
// regression test for audit bug 2.2's error-propagation half: when
// resize2fs fails during the shrink-partition-1 path,
// CarveHardwarePartitionAtBoundary must return that error (not silently
// continue), and the caller's journal step must end up marked failed,
// matching the orchestrator.go partition-shrink journaling pattern.
func TestCarveHardwarePartitionAtBoundary_Resize2fsFailurePropagates(t *testing.T) {
	withScratchMtab(t)
	origRunner := cmdRunner
	t.Cleanup(func() { cmdRunner = origRunner })
	cmdRunner = newShrinkPathMock(shrinkMockConfig{resize2fsFail: true})

	_, err := CarveHardwarePartitionAtBoundary("/dev/sdz", "ext4", 50*1024*1024*1024, nil)
	if err == nil {
		t.Fatalf("expected an error when resize2fs fails, got nil")
	}
	if !strings.Contains(err.Error(), "resize2fs failed") {
		t.Fatalf("expected the resize2fs failure to propagate verbatim, got: %v", err)
	}

	journal := newTestJournal(t)
	step, bErr := journal.Begin("partition-shrink", nil)
	if bErr != nil {
		t.Fatalf("journal.Begin: %v", bErr)
	}
	if fErr := journal.Fail(step, err); fErr != nil {
		t.Fatalf("journal.Fail: %v", fErr)
	}
	if journal.Steps[0].Status != safety.StepFailed {
		t.Fatalf("expected journal step status=%q, got %q", safety.StepFailed, journal.Steps[0].Status)
	}
}

// TestEnsureMtabSymlink_CreatesWhenMissing covers audit 2.2's "mtab missing"
// case directly: resize2fs/ext2fs_check_mount_point requires mtabPath to
// exist, so when it is missing we must create it as a symlink to
// /proc/self/mounts.
func TestEnsureMtabSymlink_CreatesWhenMissing(t *testing.T) {
	withScratchMtab(t)

	if _, err := os.Lstat(mtabPath); !os.IsNotExist(err) {
		t.Fatalf("expected mtabPath to not exist yet, stat err=%v", err)
	}

	if err := ensureMtabSymlink(); err != nil {
		t.Fatalf("ensureMtabSymlink: %v", err)
	}

	target, err := os.Readlink(mtabPath)
	if err != nil {
		t.Fatalf("expected mtabPath to be a symlink, Readlink err: %v", err)
	}
	if target != "/proc/self/mounts" {
		t.Fatalf("expected symlink target /proc/self/mounts, got %q", target)
	}
}

// TestEnsureMtabSymlink_LeavesExistingFileAlone covers the "do not overwrite
// an existing file" requirement from audit 2.2.
func TestEnsureMtabSymlink_LeavesExistingFileAlone(t *testing.T) {
	withScratchMtab(t)

	const sentinel = "pre-existing mtab content\n"
	if err := os.WriteFile(mtabPath, []byte(sentinel), 0644); err != nil {
		t.Fatalf("seeding existing mtab file: %v", err)
	}

	if err := ensureMtabSymlink(); err != nil {
		t.Fatalf("ensureMtabSymlink: %v", err)
	}

	got, err := os.ReadFile(mtabPath)
	if err != nil {
		t.Fatalf("reading mtabPath after ensureMtabSymlink: %v", err)
	}
	if string(got) != sentinel {
		t.Fatalf("expected existing mtab file to be left untouched, got %q", string(got))
	}
}

// TestCarveHardwarePartitionAtBoundary_PartitionMountedAborts covers audit
// 2.2's "partition mounted" case: the shrink must abort before touching
// e2fsck/resize2fs rather than blindly force-unmounting and proceeding.
func TestCarveHardwarePartitionAtBoundary_PartitionMountedAborts(t *testing.T) {
	withScratchMtab(t)
	origRunner := cmdRunner
	t.Cleanup(func() { cmdRunner = origRunner })
	mock := newShrinkPathMock(shrinkMockConfig{mounted: true})
	cmdRunner = mock

	_, err := CarveHardwarePartitionAtBoundary("/dev/sdz", "ext4", 50*1024*1024*1024, nil)
	if err == nil {
		t.Fatalf("expected an error when the target partition is still mounted, got nil")
	}
	if !strings.Contains(err.Error(), "mounted") {
		t.Fatalf("expected a mounted-partition error, got: %v", err)
	}
	for _, c := range mock.Calls {
		if strings.HasPrefix(c, "resize2fs ") || strings.HasPrefix(c, "e2fsck ") {
			t.Fatalf("expected shrink to abort before running %q, but it ran", c)
		}
	}
}

// TestCarveHardwarePartitionAtBoundary_E2fsckFailureAborts covers audit
// 2.2's "e2fsck failure" case: a failing pre-resize e2fsck must abort the
// shrink instead of being silently ignored.
func TestCarveHardwarePartitionAtBoundary_E2fsckFailureAborts(t *testing.T) {
	withScratchMtab(t)
	origRunner := cmdRunner
	t.Cleanup(func() { cmdRunner = origRunner })
	mock := newShrinkPathMock(shrinkMockConfig{e2fsckFail: true})
	cmdRunner = mock

	_, err := CarveHardwarePartitionAtBoundary("/dev/sdz", "ext4", 50*1024*1024*1024, nil)
	if err == nil {
		t.Fatalf("expected an error when pre-resize e2fsck fails, got nil")
	}
	if !strings.Contains(err.Error(), "e2fsck") {
		t.Fatalf("expected an e2fsck failure error, got: %v", err)
	}
	for _, c := range mock.Calls {
		if strings.HasPrefix(c, "resize2fs ") {
			t.Fatalf("expected shrink to abort before running resize2fs, but it ran: %q", c)
		}
	}
}

// TestCarveHardwarePartitionAtBoundary_ShrinkSucceeds is the success case:
// mtab missing (created on the fly), partition unmounted, e2fsck and
// resize2fs both succeed.
func TestCarveHardwarePartitionAtBoundary_ShrinkSucceeds(t *testing.T) {
	withScratchMtab(t)
	origRunner := cmdRunner
	t.Cleanup(func() { cmdRunner = origRunner })
	mock := newShrinkPathMock(shrinkMockConfig{})
	cmdRunner = mock

	// This test targets the shrink-partition-1 logic that audit 2.2 touches
	// (mtab/mount/e2fsck/resize2fs). CarveHardwarePartitionAtBoundary then
	// goes on to carve a brand-new partition and wait (via a real os.Stat
	// polling loop, not through cmdRunner) for its /dev node to appear —
	// that cannot succeed here since /dev/sdz2 isn't a real device on the
	// test machine. That is a separate, pre-existing limitation unrelated to
	// this fix, so this test tolerates but does not require that specific
	// downstream error.
	res, err := CarveHardwarePartitionAtBoundary("/dev/sdz", "ext4", 50*1024*1024*1024, nil)
	if err != nil && !strings.Contains(err.Error(), "node missing in /dev") {
		t.Fatalf("expected the shrink-partition-1 preflight+resize2fs logic to succeed, got unrelated error: %v", err)
	}
	if err == nil && (res == nil || res.NewPartitionPath == "") {
		t.Fatalf("expected a non-empty NewPartitionPath on full success, got %+v", res)
	}

	if target, rlErr := os.Readlink(mtabPath); rlErr != nil || target != "/proc/self/mounts" {
		t.Fatalf("expected mtabPath to have been created as a symlink to /proc/self/mounts, target=%q err=%v", target, rlErr)
	}

	sawResize := false
	for _, c := range mock.Calls {
		if strings.HasPrefix(c, "resize2fs ") && !strings.Contains(c, "-P") {
			sawResize = true
		}
	}
	if !sawResize {
		t.Fatalf("expected resize2fs to have been invoked on the success path, calls=%v", mock.Calls)
	}
}
