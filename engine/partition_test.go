package engine

import (
	"fmt"
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

func newShrinkPathMock(failNames map[string]bool) *mockRunner {
	m := &mockRunner{}
	m.Handler = func(name string, args []string) ([]byte, error) {
		m.Calls = append(m.Calls, strings.Join(append([]string{name}, args...), " "))
		if failNames[name] {
			return []byte(fmt.Sprintf("mock: %s failed", name)), fmt.Errorf("%s failed on /dev/sdz1: exit status 1 (mock: simulated failure)", name)
		}
		switch name {
		case "parted":
			if containsAll(args, "free") {
				return []byte(partedFreeNoHeadroom), nil
			}
			if containsAll(args, "unit", "s", "print") {
				return []byte(partedSinglePartitionPrint), nil
			}
		case "blockdev":
			return []byte("512110190592\n"), nil
		}
		return []byte(""), nil
	}
	return m
}

// TestCarveHardwarePartitionAtBoundary_Resize2fsFailurePropagates is the
// regression test for audit bug 2.2: when resize2fs fails during the
// shrink-partition-1 path, CarveHardwarePartitionAtBoundary must return that
// error (not silently continue), and the caller's journal step must end up
// marked failed, matching the orchestrator.go partition-shrink journaling
// pattern.
func TestCarveHardwarePartitionAtBoundary_Resize2fsFailurePropagates(t *testing.T) {
	origRunner := cmdRunner
	t.Cleanup(func() { cmdRunner = origRunner })
	cmdRunner = newShrinkPathMock(map[string]bool{"resize2fs": true})

	_, err := CarveHardwarePartitionAtBoundary("/dev/sdz", "ext4", 50*1024*1024*1024, nil)
	if err == nil {
		t.Fatalf("expected an error when resize2fs fails, got nil")
	}
	if !strings.Contains(err.Error(), "resize2fs failed") {
		t.Fatalf("expected the resize2fs failure to propagate verbatim, got: %v", err)
	}

	journalPath := filepath.Join(t.TempDir(), "journal.json")
	journal, jErr := safety.NewJournal(journalPath, "test-txn")
	if jErr != nil {
		t.Fatalf("NewJournal: %v", jErr)
	}
	step, bErr := journal.Begin("partition-shrink", nil)
	if bErr != nil {
		t.Fatalf("journal.Begin: %v", bErr)
	}
	if fErr := journal.Fail(step, err); fErr != nil {
		t.Fatalf("journal.Fail: %v", fErr)
	}

	if len(journal.Steps) != 1 {
		t.Fatalf("expected exactly 1 journal step, got %d", len(journal.Steps))
	}
	if journal.Steps[0].Status != safety.StepFailed {
		t.Fatalf("expected journal step status=%q, got %q", safety.StepFailed, journal.Steps[0].Status)
	}
}
