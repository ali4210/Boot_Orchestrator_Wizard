// Audit follow-up (Windows CI): CarveHardwarePartitionAtBoundary's
// swap-detection logic (what this file tests) is part of the same
// Linux-only ext4/parted/mkswap disk-shrink flow as partition_test.go —
// see that file's own //go:build comment for the full investigation.
// Also shares partition_test.go's calledPartedRemove/partedFreeNoHeadroom
// helpers, so this file must carry the same tag or those would be
// undefined on a non-Linux build.
//go:build linux

package engine

import (
	"strings"
	"testing"
)

// partedTrailingNonSwapThreePartitionPrint simulates a disk whose last
// partition happens to be numbered 3 but is a real ext4 partition, not
// swap. Before audit 1B.6's fix, CarveHardwarePartitionAtBoundary treated
// ANY disk whose last partition was numbered 3 as swap regardless of its
// actual filesystem type (`lastPart.num == 3`), which would have deleted
// this partition's real data.
const partedTrailingNonSwapThreePartitionPrint = `Model: Fake
Disk /dev/sdz: 1000215216s
Sector size (logical/physical): 512B/512B
Partition Table: gpt
Disk Flags:

Number  Start        End          Size         File system  Name  Flags
 1      2048s        1000000s     997952s      ext4
 2      1000001s     500000000s   499000000s   ext4
 3      500000001s   600000000s   100000000s   ext4
`

// partedTrailingSwapPrint simulates a disk whose trailing partition 3 is
// genuinely swap, both in parted's own fs-type column and (via the mock's
// blkid handler below) independently confirmed by blkid.
const partedTrailingSwapPrint = `Model: Fake
Disk /dev/sdz: 1000215216s
Sector size (logical/physical): 512B/512B
Partition Table: gpt
Disk Flags:

Number  Start        End          Size         File system     Name  Flags
 1      2048s        1000000s     997952s      ext4
 2      1000001s     500000000s   499000000s   ext4
 3      500000001s   600000000s   100000000s   linux-swap(v1)
`

// newTrailingPartitionMock builds a mockRunner for the "trailing swap"
// branch of CarveHardwarePartitionAtBoundary: printOutput is the `parted
// unit s print` response, and blkidType is what the mock's blkid handler
// reports for devPath's TYPE (used by isConfirmedSwapPartition's
// independent check).
func newTrailingPartitionMock(printOutput, blkidType string) *mockRunner {
	m := &mockRunner{}
	m.Handler = func(name string, args []string) ([]byte, error) {
		switch name {
		case "parted":
			if containsAll(args, "free") {
				return []byte(partedFreeNoHeadroom), nil
			}
			if containsAll(args, "unit", "s", "print") {
				return []byte(printOutput), nil
			}
			return []byte(""), nil
		case "blockdev":
			return []byte("512110190592\n"), nil
		}
		return []byte(""), nil
	}
	m.StdoutHandler = func(name string, args []string) ([]byte, error) {
		if name == "blkid" {
			return []byte(blkidType + "\n"), nil
		}
		return m.Handler(name, args)
	}
	return m
}

// TestCarveHardwarePartitionAtBoundary_DoesNotDeleteNonSwapPartitionThree is
// the regression test for audit 1B.6: a disk whose last partition is
// numbered 3 but is NOT swap must never have that partition removed.
func TestCarveHardwarePartitionAtBoundary_DoesNotDeleteNonSwapPartitionThree(t *testing.T) {
	mock := newTrailingPartitionMock(partedTrailingNonSwapThreePartitionPrint, "ext4")
	origRunner := cmdRunner
	t.Cleanup(func() { cmdRunner = origRunner })
	cmdRunner = mock

	// The result/error here is not the point (there's no real free space to
	// carve from in this fixture, so it will fail further down) — what
	// matters is that partition 3 was never removed to make room.
	_, _ = CarveHardwarePartitionAtBoundary("/dev/sdz", "ext4", 20*1024*1024*1024, nil)

	if calledPartedRemove(mock.Calls) {
		t.Fatalf("expected no `parted rm` call against a non-swap partition 3, got calls: %v", mock.Calls)
	}
}

// TestCarveHardwarePartitionAtBoundary_ConvertsConfirmedSwap proves the
// positive case still works: a genuinely swap-typed trailing partition,
// confirmed by both parted's fs column and an independent blkid probe, is
// still removed and converted.
func TestCarveHardwarePartitionAtBoundary_ConvertsConfirmedSwap(t *testing.T) {
	mock := newTrailingPartitionMock(partedTrailingSwapPrint, "swap")
	origRunner := cmdRunner
	t.Cleanup(func() { cmdRunner = origRunner })
	cmdRunner = mock

	result, err := CarveHardwarePartitionAtBoundary("/dev/sdz", "ext4", 20*1024*1024*1024, nil)
	if err != nil {
		t.Fatalf("expected the confirmed-swap conversion to succeed, got: %v", err)
	}
	if result == nil || result.NewPartNum != "3" {
		t.Fatalf("expected partition 3 to be reused for the new OS partition, got: %+v", result)
	}

	found := false
	for _, c := range mock.Calls {
		if strings.Contains(c, "parted") && strings.Contains(c, "rm") && strings.Contains(c, "3") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a `parted rm 3` call for the confirmed swap partition, got calls: %v", mock.Calls)
	}
}

// TestCarveHardwarePartitionAtBoundary_DoesNotTrustPartedAloneForSwap proves
// that a mismatch between parted's fs column (says swap) and blkid's
// independent probe (says something else — e.g. a stale parted cache after
// the partition was reformatted) blocks the removal.
func TestCarveHardwarePartitionAtBoundary_DoesNotTrustPartedAloneForSwap(t *testing.T) {
	mock := newTrailingPartitionMock(partedTrailingSwapPrint, "ext4")
	origRunner := cmdRunner
	t.Cleanup(func() { cmdRunner = origRunner })
	cmdRunner = mock

	_, _ = CarveHardwarePartitionAtBoundary("/dev/sdz", "ext4", 20*1024*1024*1024, nil)

	if calledPartedRemove(mock.Calls) {
		t.Fatalf("expected no removal when blkid disagrees with parted's fs column, got calls: %v", mock.Calls)
	}
}

// TestIsConfirmedSwapPartition_RequiresBothSignals is a focused unit test
// of the helper itself, independent of the larger carve function.
func TestIsConfirmedSwapPartition_RequiresBothSignals(t *testing.T) {
	cases := []struct {
		name         string
		partedFSName string
		blkidType    string
		want         bool
	}{
		{"both confirm swap", "linux-swap(v1)", "swap", true},
		{"parted says ext4", "ext4", "swap", false},
		{"blkid disagrees", "linux-swap(v1)", "ext4", false},
		{"neither confirms", "ext4", "ext4", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origRunner := cmdRunner
			t.Cleanup(func() { cmdRunner = origRunner })
			cmdRunner = &mockRunner{
				StdoutHandler: func(name string, args []string) ([]byte, error) {
					return []byte(tc.blkidType + "\n"), nil
				},
			}
			if got := isConfirmedSwapPartition("/dev/sdz3", tc.partedFSName); got != tc.want {
				t.Errorf("isConfirmedSwapPartition(%q, %q) = %v, want %v", "/dev/sdz3", tc.partedFSName, got, tc.want)
			}
		})
	}
}
