package engine

import "testing"

// TestSplitDiskAndPartition_HandlesEveryNamingScheme is the regression test
// for audit 1C.8: SplitDiskAndPartition used to special-case only
// "nvme"/"mmcblk" by substring before falling back to a bare
// "trailing digits" parse, which mis-parsed loop device partitions
// (/dev/loop0p1 -> disk "/dev/loop0p", part "1" — wrong). It now detects
// the "pN" infix structurally, so loop devices (used for all safe
// disk-image testing per this tool's own safety rules) parse the same way
// nvme/mmcblk do, with no naming-substring allowlist.
func TestSplitDiskAndPartition_HandlesEveryNamingScheme(t *testing.T) {
	cases := []struct {
		input     string
		wantDisk  string
		wantPart  string
		wantError bool
	}{
		{"/dev/sda1", "/dev/sda", "1", false},
		{"/dev/sdz12", "/dev/sdz", "12", false},
		{"/dev/nvme0n1p3", "/dev/nvme0n1", "3", false},
		{"/dev/mmcblk0p2", "/dev/mmcblk0", "2", false},
		{"/dev/loop0p1", "/dev/loop0", "1", false},
		{"/dev/loop12p2", "/dev/loop12", "2", false},
		{"/dev/vda1", "/dev/vda", "1", false},
		{"sda1", "sda", "1", false}, // relative/bare name, no /dev prefix
		{"/dev/sda", "", "", true},  // no partition number at all
	}
	for _, tc := range cases {
		disk, part, err := SplitDiskAndPartition(tc.input)
		if tc.wantError {
			if err == nil {
				t.Errorf("SplitDiskAndPartition(%q): expected an error, got disk=%q part=%q", tc.input, disk, part)
			}
			continue
		}
		if err != nil {
			t.Errorf("SplitDiskAndPartition(%q): unexpected error: %v", tc.input, err)
			continue
		}
		if disk != tc.wantDisk || part != tc.wantPart {
			t.Errorf("SplitDiskAndPartition(%q) = (%q, %q), want (%q, %q)", tc.input, disk, part, tc.wantDisk, tc.wantPart)
		}
	}
}

// TestJoinDiskAndPartition_HandlesEveryNamingScheme covers the inverse
// direction for the same set of device-naming schemes.
func TestJoinDiskAndPartition_HandlesEveryNamingScheme(t *testing.T) {
	cases := []struct {
		disk    string
		partNum string
		want    string
	}{
		{"/dev/sda", "1", "/dev/sda1"},
		{"/dev/nvme0n1", "3", "/dev/nvme0n1p3"},
		{"/dev/mmcblk0", "2", "/dev/mmcblk0p2"},
		{"/dev/loop0", "1", "/dev/loop0p1"},
		{"/dev/vda", "1", "/dev/vda1"},
	}
	for _, tc := range cases {
		if got := JoinDiskAndPartition(tc.disk, tc.partNum); got != tc.want {
			t.Errorf("JoinDiskAndPartition(%q, %q) = %q, want %q", tc.disk, tc.partNum, got, tc.want)
		}
	}
}

// TestSplitJoinDiskAndPartition_RoundTrip proves Split and Join agree with
// each other across every naming scheme, not just each in isolation.
func TestSplitJoinDiskAndPartition_RoundTrip(t *testing.T) {
	partitionPaths := []string{
		"/dev/sda1", "/dev/sdz12", "/dev/nvme0n1p3", "/dev/mmcblk0p2",
		"/dev/loop0p1", "/dev/loop12p2", "/dev/vda1",
	}
	for _, p := range partitionPaths {
		disk, num, err := SplitDiskAndPartition(p)
		if err != nil {
			t.Fatalf("SplitDiskAndPartition(%q): %v", p, err)
		}
		if rejoined := JoinDiskAndPartition(disk, num); rejoined != p {
			t.Errorf("round trip failed for %q: split -> (%q, %q) -> join -> %q", p, disk, num, rejoined)
		}
	}
}
