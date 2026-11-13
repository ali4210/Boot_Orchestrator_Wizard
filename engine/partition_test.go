package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"boot-orchestrator/safety"
)

// fakeExitError returns a genuine *exec.ExitError reporting exit code code,
// produced by actually running `sh -c "exit N"` — a real, safe, no-op
// subprocess. Used so mocks can simulate exact exit codes for exitCodeOf
// without needing a hand-rolled error type. Returns nil for code 0.
func fakeExitError(code int) error {
	return exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
}

// calledPartedRemove reports whether any call in calls was a `parted ... rm
// ...` invocation (the destructive step that must never run before all
// preflight checks pass).
func calledPartedRemove(calls []string) bool {
	for _, c := range calls {
		fields := strings.Fields(c)
		if len(fields) > 0 && fields[0] == "parted" && containsAll(fields, "rm") {
			return true
		}
	}
	return false
}

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
	mounted        bool // findmnt reports a mountpoint
	e2fsckExitCode int  // 0 = success; e2fsck's real "0=clean,1=corrected,2+=failed" scale
	resize2fsFail  bool
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
			if cfg.e2fsckExitCode != 0 {
				return []byte(fmt.Sprintf("mock: e2fsck exited %d", cfg.e2fsckExitCode)), fakeExitError(cfg.e2fsckExitCode)
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
	if calledPartedRemove(mock.Calls) {
		t.Fatalf("expected no destructive `parted ... rm` call when the mounted check fails, calls=%v", mock.Calls)
	}
}

// TestCarveHardwarePartitionAtBoundary_E2fsckExitCode4Aborts covers audit
// 2.2's "e2fsck failure" case: e2fsck exiting >=2 (here 4: "errors not
// corrected") must abort the shrink before any destructive step runs,
// instead of being silently ignored.
func TestCarveHardwarePartitionAtBoundary_E2fsckExitCode4Aborts(t *testing.T) {
	withScratchMtab(t)
	origRunner := cmdRunner
	t.Cleanup(func() { cmdRunner = origRunner })
	mock := newShrinkPathMock(shrinkMockConfig{e2fsckExitCode: 4})
	cmdRunner = mock

	_, err := CarveHardwarePartitionAtBoundary("/dev/sdz", "ext4", 50*1024*1024*1024, nil)
	if err == nil {
		t.Fatalf("expected an error when pre-resize e2fsck exits 4, got nil")
	}
	if !strings.Contains(err.Error(), "e2fsck") || !strings.Contains(err.Error(), "exit code 4") {
		t.Fatalf("expected an e2fsck exit-code-4 failure error, got: %v", err)
	}
	for _, c := range mock.Calls {
		if strings.HasPrefix(c, "resize2fs ") {
			t.Fatalf("expected shrink to abort before running resize2fs, but it ran: %q", c)
		}
	}
	if calledPartedRemove(mock.Calls) {
		t.Fatalf("expected no destructive `parted ... rm` call when e2fsck fails, calls=%v", mock.Calls)
	}
}

// TestCarveHardwarePartitionAtBoundary_E2fsckExitCode1Continues covers the
// other half of audit 2.2's e2fsck handling: exit code 1 ("errors were
// found and corrected") must be treated as success, not an abort.
func TestCarveHardwarePartitionAtBoundary_E2fsckExitCode1Continues(t *testing.T) {
	withScratchMtab(t)
	origRunner := cmdRunner
	t.Cleanup(func() { cmdRunner = origRunner })
	mock := newShrinkPathMock(shrinkMockConfig{e2fsckExitCode: 1})
	cmdRunner = mock

	// See TestCarveHardwarePartitionAtBoundary_ShrinkSucceeds for why the
	// downstream "node missing in /dev" error is tolerated here.
	_, err := CarveHardwarePartitionAtBoundary("/dev/sdz", "ext4", 50*1024*1024*1024, nil)
	if err != nil && !strings.Contains(err.Error(), "node missing in /dev") {
		t.Fatalf("expected e2fsck exit code 1 to be treated as success, got unrelated error: %v", err)
	}

	sawResize := false
	for _, c := range mock.Calls {
		if strings.HasPrefix(c, "resize2fs ") && !strings.Contains(c, "-P") {
			sawResize = true
		}
	}
	if !sawResize {
		t.Fatalf("expected resize2fs to have been invoked after e2fsck exit code 1, calls=%v", mock.Calls)
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

// applyShrinkMockConfig controls a mockRunner simulating ApplyShrink's
// commands. e2fsck is called twice (pre- and post-resize); preExitCode and
// postExitCode let tests drive each call independently.
type applyShrinkMockConfig struct {
	mounted       bool
	preExitCode   int
	postExitCode  int
	resize2fsFail bool
}

func newApplyShrinkMock(cfg applyShrinkMockConfig) *mockRunner {
	e2fsckCalls := 0
	m := &mockRunner{}
	m.Handler = func(name string, args []string) ([]byte, error) {
		switch name {
		case "findmnt":
			if cfg.mounted {
				return []byte("/mnt/fake-target\n"), nil
			}
			return []byte(""), fmt.Errorf("findmnt: no mount found")
		case "e2fsck":
			e2fsckCalls++
			exitCode := cfg.preExitCode
			if e2fsckCalls > 1 {
				exitCode = cfg.postExitCode
			}
			if exitCode != 0 {
				return []byte(fmt.Sprintf("mock: e2fsck exited %d", exitCode)), fakeExitError(exitCode)
			}
			return []byte(""), nil
		case "resize2fs":
			if cfg.resize2fsFail {
				return []byte("mock: resize2fs failed"), fmt.Errorf("resize2fs failed: mock simulated failure")
			}
			return []byte(""), nil
		}
		return []byte(""), nil
	}
	return m
}

func newTestShrinkPlan() *ShrinkPlan {
	return &ShrinkPlan{
		Filesystem: FilesystemInfo{
			PartitionPath: "/dev/sdz1",
			TotalBytes:    100 * 1024 * 1024 * 1024,
		},
		TargetNewSizeBytes: 50 * 1024 * 1024 * 1024,
	}
}

// TestApplyShrink_PreResizeE2fsckExitCode1Continues covers audit 2.2's e2fsck
// handling in ApplyShrink: exit code 1 on the pre-resize check must be
// treated as success.
func TestApplyShrink_PreResizeE2fsckExitCode1Continues(t *testing.T) {
	origRunner := cmdRunner
	t.Cleanup(func() { cmdRunner = origRunner })
	cmdRunner = newApplyShrinkMock(applyShrinkMockConfig{preExitCode: 1})

	if err := ApplyShrink(newTestShrinkPlan()); err != nil {
		t.Fatalf("expected e2fsck exit code 1 to be treated as success, got: %v", err)
	}
}

// TestApplyShrink_PreResizeE2fsckExitCode4Aborts covers the other half: exit
// code 4 on the pre-resize check must abort before resize2fs ever runs.
func TestApplyShrink_PreResizeE2fsckExitCode4Aborts(t *testing.T) {
	origRunner := cmdRunner
	t.Cleanup(func() { cmdRunner = origRunner })
	mock := newApplyShrinkMock(applyShrinkMockConfig{preExitCode: 4})
	cmdRunner = mock

	err := ApplyShrink(newTestShrinkPlan())
	if err == nil {
		t.Fatalf("expected an error when pre-resize e2fsck exits 4, got nil")
	}
	if !strings.Contains(err.Error(), "pre-resize e2fsck failed") || !strings.Contains(err.Error(), "exit code 4") {
		t.Fatalf("expected a pre-resize e2fsck exit-code-4 failure error, got: %v", err)
	}
	for _, c := range mock.Calls {
		if strings.HasPrefix(c, "resize2fs ") {
			t.Fatalf("expected ApplyShrink to abort before running resize2fs, but it ran: %q", c)
		}
	}
}

// TestApplyShrink_PostResizeE2fsckExitCode4Aborts covers the post-resize
// verification e2fsck: exit code 4 there must also surface as a failure.
func TestApplyShrink_PostResizeE2fsckExitCode4Aborts(t *testing.T) {
	origRunner := cmdRunner
	t.Cleanup(func() { cmdRunner = origRunner })
	cmdRunner = newApplyShrinkMock(applyShrinkMockConfig{postExitCode: 4})

	err := ApplyShrink(newTestShrinkPlan())
	if err == nil {
		t.Fatalf("expected an error when post-resize e2fsck exits 4, got nil")
	}
	if !strings.Contains(err.Error(), "post-resize e2fsck verification failed") || !strings.Contains(err.Error(), "exit code 4") {
		t.Fatalf("expected a post-resize e2fsck exit-code-4 failure error, got: %v", err)
	}
}

// TestReadDiskRegion_UsesStdoutOnly proves readDiskRegion (which backs up
// raw GPT bytes for rollback) parses `dd`'s output via RunStdout, not Run:
// appending stderr noise to raw binary partition-table bytes read via Run
// would silently corrupt the backup instead of erroring.
func TestReadDiskRegion_UsesStdoutOnly(t *testing.T) {
	origRunner := cmdRunner
	t.Cleanup(func() { cmdRunner = origRunner })
	cleanBytes := []byte{0x45, 0x46, 0x49, 0x20, 0x50, 0x41, 0x52, 0x54} // "EFI PART"
	cmdRunner = &mockRunner{
		Handler: func(name string, args []string) ([]byte, error) {
			noisy := append(append([]byte{}, cleanBytes...), []byte("\ndd: a stray stderr notice\n")...)
			return noisy, nil
		},
		StdoutHandler: func(name string, args []string) ([]byte, error) {
			return cleanBytes, nil
		},
	}

	got, err := readDiskRegion("/dev/sdz", 0, int64(len(cleanBytes)))
	if err != nil {
		t.Fatalf("readDiskRegion: %v", err)
	}
	if string(got) != string(cleanBytes) {
		t.Fatalf("readDiskRegion: got %q, want %q (must use RunStdout, not Run)", got, cleanBytes)
	}
}
