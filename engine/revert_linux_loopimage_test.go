//go:build linux

package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// This file is bug F's reproduction: a real GPT disk image, loop-mounted,
// with an ESP and a host ext4 root containing test files, already in the
// "dual-booted" end state (host partition shrunk, a second OS partition
// carved after it) — then ExecuteDecommissionWithOptions is run against it
// for real and the result is verified: the added partition is gone, the
// host partition is grown back and its filesystem resized, and the host's
// own files are byte-for-byte unchanged.
//
// ExecuteDecommissionWithOptions requires os.Geteuid() == 0 (it's meant to
// run inside the already-elevated orchestrator binary), so these tests
// only run the destructive portion when the test process itself is root
// (e.g. `sudo go test ./engine/... -run TestExecuteDecommission`), and
// skip cleanly otherwise rather than making normal `go test ./...` runs
// require root.

func requireLoopImageTools(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"losetup", "parted", "mkfs.ext4", "mkfs.vfat", "sgdisk", "growpart", "resize2fs", "blkid", "e2fsck", "mkswap"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available, skipping loop-image test", bin)
		}
	}
}

func requireRootForDestructiveLoopTest(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("ExecuteDecommissionWithOptions requires the test process itself to run as root (e.g. `sudo go test ./engine/... -run TestExecuteDecommission`); skipping under a normal, unprivileged `go test` run")
	}
}

func mustRun(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// dualBootFixture is an already-dual-booted loop-image end state: a GPT
// disk with an ESP (partition 1), a shrunk host ext4 root with test files
// (partition 2), and a second "installed OS" ext4 partition (partition 3)
// occupying the rest of the disk.
type dualBootFixture struct {
	loopDev        string
	hostPart       string // e.g. /dev/loop0p2
	hostPartNum    string // "2"
	newOSPart      string // e.g. /dev/loop0p3
	newOSPartNum   string // "3"
	newOSPartUUID  string
	newOSSizeBytes int64
	hostFiles      map[string]string // relative path -> sha256
}

func buildDualBootFixture(t *testing.T) dualBootFixture {
	t.Helper()
	requireLoopImageTools(t)

	dir := t.TempDir()
	imgPath := filepath.Join(dir, "disk.img")
	mustRun(t, "truncate", "-s", "2G", imgPath)
	mustRun(t, "parted", "-s", imgPath, "mklabel", "gpt")
	mustRun(t, "parted", "-s", imgPath, "mkpart", "ESP", "fat32", "1MiB", "101MiB")
	mustRun(t, "parted", "-s", imgPath, "set", "1", "esp", "on")
	mustRun(t, "parted", "-s", imgPath, "mkpart", "primary", "ext4", "101MiB", "1125MiB")
	mustRun(t, "parted", "-s", imgPath, "mkpart", "primary", "ext4", "1125MiB", "100%")

	loopOut := mustRun(t, "sudo", "losetup", "-f", "--show", "-P", imgPath)
	loopDev := strings.TrimSpace(loopOut)
	t.Cleanup(func() {
		_ = exec.Command("sudo", "losetup", "-d", loopDev).Run()
	})

	hostPart := loopDev + "p2"
	newOSPart := loopDev + "p3"

	mustRun(t, "sudo", "mkfs.vfat", "-F32", "-n", "ESP", loopDev+"p1")
	mustRun(t, "sudo", "mkfs.ext4", "-F", "-q", "-L", "HOSTROOT", hostPart)
	mustRun(t, "sudo", "mkfs.ext4", "-F", "-q", "-L", "NEWOS", newOSPart)

	hostMount := filepath.Join(dir, "mnt_host")
	newMount := filepath.Join(dir, "mnt_new")
	mustRun(t, "mkdir", "-p", hostMount, newMount)
	mustRun(t, "sudo", "mount", hostPart, hostMount)
	t.Cleanup(func() { _ = exec.Command("sudo", "umount", "-l", hostPart).Run() })
	mustRun(t, "sudo", "mount", newOSPart, newMount)

	hostFiles := map[string]string{}
	writeHostFile := func(name string, content []byte) {
		path := filepath.Join(hostMount, name)
		if err := os.WriteFile(path, content, 0644); err != nil {
			// os.WriteFile as the test's own (non-root) user may fail on a
			// root-owned mountpoint; fall back to a root-run write.
			tmp := filepath.Join(dir, "stage-"+name)
			if werr := os.WriteFile(tmp, content, 0644); werr != nil {
				t.Fatalf("staging %s: %v", name, werr)
			}
			mustRun(t, "sudo", "cp", tmp, path)
		}
		sum := sha256.Sum256(content)
		hostFiles[name] = hex.EncodeToString(sum[:])
	}
	writeHostFile("testfile.txt", []byte("host-file-content-for-revert-regression-test\n"))
	bigData := make([]byte, 2*1024*1024)
	for i := range bigData {
		bigData[i] = byte(i % 251)
	}
	writeHostFile("bigfile.bin", bigData)

	mustRun(t, "sudo", "sh", "-c", "echo new-os-marker > "+filepath.Join(newMount, "marker.txt"))
	mustRun(t, "sync")

	newOSPartUUID := strings.TrimSpace(mustRun(t, "sudo", "blkid", "-s", "PARTUUID", "-o", "value", newOSPart))

	mustRun(t, "sudo", "umount", newMount)
	mustRun(t, "sudo", "umount", hostMount)
	mustRun(t, "sudo", "e2fsck", "-fy", hostPart)

	sizeOut := strings.TrimSpace(mustRun(t, "sudo", "blockdev", "--getsize64", newOSPart))
	var newOSSizeBytes int64
	for _, c := range sizeOut {
		if c < '0' || c > '9' {
			break
		}
		newOSSizeBytes = newOSSizeBytes*10 + int64(c-'0')
	}

	return dualBootFixture{
		loopDev:        loopDev,
		hostPart:       hostPart,
		hostPartNum:    "2",
		newOSPart:      newOSPart,
		newOSPartNum:   "3",
		newOSPartUUID:  newOSPartUUID,
		newOSSizeBytes: newOSSizeBytes,
		hostFiles:      hostFiles,
	}
}

func sha256OfFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// scratchRevertEnv bundles every injectable ExecuteDecommissionWithOptions
// side effect that withScratchRevertPaths redirects, so tests can both
// drive the function with real EFIDirNameToPurge/EFIBootEntryNum values
// (follow-up F14 closes the gap where this file previously had to pass
// those empty to avoid touching the real ESP and real NVRAM) and assert on
// what happened.
type scratchRevertEnv struct {
	fstabPath       string
	updateGrubCalls *int
	espDir          string   // stands in for the real ESP mount (/boot/efi)
	efibootmgrCalls *[]string
}

// withScratchRevertPaths redirects every one of ExecuteDecommissionWithOptions's
// real-system side effects — GRUB regeneration, fstab, ESP mount discovery,
// UEFI-mode detection, and NVRAM boot-entry removal — to scratch
// stand-ins for the duration of the test, restoring the real values
// afterward.
func withScratchRevertPaths(t *testing.T) *scratchRevertEnv {
	t.Helper()
	dir := t.TempDir()

	origCustom, origDropins, origUpdate, origFstab := grubCustomPath, grubDropinPaths, updateGrubFunc, fstabPath
	origESPFunc, origEfibootmgrFunc, origUEFIFunc := findActiveESPMountFunc, efibootmgrRemoveFunc, isUEFIModeFunc
	t.Cleanup(func() {
		grubCustomPath, grubDropinPaths, updateGrubFunc, fstabPath = origCustom, origDropins, origUpdate, origFstab
		findActiveESPMountFunc, efibootmgrRemoveFunc, isUEFIModeFunc = origESPFunc, origEfibootmgrFunc, origUEFIFunc
	})

	grubCustomPath = filepath.Join(dir, "40_custom")
	grubDropinPaths = []string{filepath.Join(dir, "50-dualboot-menu.cfg"), filepath.Join(dir, "99-orchestrator-prober.cfg")}
	calls := 0
	updateGrubFunc = func() error { calls++; return nil }
	scratchFstab := filepath.Join(dir, "fstab")
	fstabPath = scratchFstab

	espDir := filepath.Join(dir, "esp")
	if err := os.MkdirAll(espDir, 0755); err != nil {
		t.Fatalf("creating scratch ESP dir: %v", err)
	}
	findActiveESPMountFunc = func() string { return espDir }

	var efibootmgrCalls []string
	efibootmgrRemoveFunc = func(bootNum string) error {
		efibootmgrCalls = append(efibootmgrCalls, bootNum)
		return nil
	}
	isUEFIModeFunc = func() bool { return true }

	return &scratchRevertEnv{
		fstabPath:       scratchFstab,
		updateGrubCalls: &calls,
		espDir:          espDir,
		efibootmgrCalls: &efibootmgrCalls,
	}
}

// TestExecuteDecommission_RestoresHostAndRemovesNewOSPartition is bug F's
// core reproduction: builds the loop-image fixture, runs the real
// ExecuteDecommissionWithOptions against it, and verifies the added
// partition is gone, the host partition is grown back and resized, and
// every host file is byte-for-byte unchanged.
func TestExecuteDecommission_RestoresHostAndRemovesNewOSPartition(t *testing.T) {
	requireRootForDestructiveLoopTest(t)
	fx := buildDualBootFixture(t)
	env := withScratchRevertPaths(t)

	// Seed a scratch fstab with one entry for the deleted partition (by
	// PARTUUID, the realistic case for a GPT/UEFI system) and one for the
	// host partition, to prove the dangling one gets cleaned up and the
	// live one is left alone.
	hostPartUUID := strings.TrimSpace(mustRun(t, "sudo", "blkid", "-s", "PARTUUID", "-o", "value", fx.hostPart))
	fstabContent := "PARTUUID=" + hostPartUUID + " / ext4 defaults 0 1\n" +
		"PARTUUID=" + fx.newOSPartUUID + " /mnt/newos ext4 defaults 0 2\n"
	if err := os.WriteFile(env.fstabPath, []byte(fstabContent), 0644); err != nil {
		t.Fatalf("writing scratch fstab: %v", err)
	}

	// Plant a fake EFI payload under the scratch ESP so the test exercises
	// the real "/EFI/<dir> is removed" code path (follow-up F14) instead
	// of bypassing it with an empty EFIDirNameToPurge.
	efiPayloadDir := filepath.Join(env.espDir, "EFI", "parrot")
	if err := os.MkdirAll(efiPayloadDir, 0755); err != nil {
		t.Fatalf("staging fake ESP payload: %v", err)
	}
	if err := os.WriteFile(filepath.Join(efiPayloadDir, "grubx64.efi"), []byte("fake"), 0644); err != nil {
		t.Fatalf("staging fake ESP payload file: %v", err)
	}

	params := RealRevertParams{
		DiskDevice:        fx.loopDev,
		PartitionNum:      fx.newOSPartNum,
		HostPartNum:       fx.hostPartNum,
		EFIDirNameToPurge: "parrot",
		EFIBootEntryNum:   "0003",
		TargetPartUUID:    fx.newOSPartUUID,
		ExpectedFSType:    "ext4",
		ExpectedSizeBytes: fx.newOSSizeBytes,
	}

	var logLines []string
	err := ExecuteDecommissionWithOptions(params, false, func(s string) { logLines = append(logLines, s) }, nil)
	if err != nil {
		t.Fatalf("ExecuteDecommissionWithOptions: %v\nlog:\n%s", err, strings.Join(logLines, "\n"))
	}

	// 0a. The ESP payload directory must be gone (real removal, via the
	// injected findActiveESPMountFunc pointing at the scratch ESP).
	if _, statErr := os.Stat(efiPayloadDir); !os.IsNotExist(statErr) {
		t.Errorf("expected the EFI payload dir %s to be removed, stat err: %v", efiPayloadDir, statErr)
	}

	// 0b. The injected efibootmgrRemoveFunc must have been called with the
	// boot entry number ("Boot" prefix stripped), proving NVRAM removal is
	// wired up without ever shelling out to the real efibootmgr.
	if calls := *env.efibootmgrCalls; len(calls) != 1 || calls[0] != "0003" {
		t.Errorf("expected efibootmgrRemoveFunc to be called once with %q, got %v", "0003", calls)
	}

	// 1. The new-OS partition must be gone.
	partedOut := mustRun(t, "sudo", "parted", "-s", fx.loopDev, "print")
	if strings.Contains(partedOut, "NEWOS") {
		t.Errorf("expected the new-OS partition to be removed, parted still reports it:\n%s", partedOut)
	}

	// 2. The host partition must have grown to occupy the freed space.
	sizeOut := strings.TrimSpace(mustRun(t, "sudo", "blockdev", "--getsize64", fx.hostPart))
	var hostSizeBytes int64
	for _, c := range sizeOut {
		if c < '0' || c > '9' {
			break
		}
		hostSizeBytes = hostSizeBytes*10 + int64(c-'0')
	}
	const expectGrownAtLeast = int64(1700) * 1024 * 1024 // was ~1GiB, should now be ~1.9GiB
	if hostSizeBytes < expectGrownAtLeast {
		t.Errorf("expected the host partition to grow to at least %d bytes, got %d", expectGrownAtLeast, hostSizeBytes)
	}

	// 3. Host files must be byte-for-byte unchanged.
	mustRun(t, "sudo", "e2fsck", "-fy", fx.hostPart)
	dir := t.TempDir()
	mustRun(t, "sudo", "mount", fx.hostPart, dir)
	defer exec.Command("sudo", "umount", "-l", dir).Run()

	for name, wantSHA := range fx.hostFiles {
		got := sha256OfFile(t, filepath.Join(dir, name))
		if got != wantSHA {
			t.Errorf("file %s: checksum changed across revert (want %s, got %s)", name, wantSHA, got)
		}
	}

	// 4. GRUB regeneration ran against the scratch paths, not the real
	// system (proving the injectable vars actually took effect).
	if *env.updateGrubCalls != 1 {
		t.Errorf("expected update-grub to be invoked exactly once, got %d calls", *env.updateGrubCalls)
	}

	// 5. fstab cleanup: the dangling new-OS entry must be commented out;
	// the still-live host entry must be left alone.
	fstabAfter, err := os.ReadFile(env.fstabPath)
	if err != nil {
		t.Fatalf("reading scratch fstab after revert: %v", err)
	}
	fstabStr := string(fstabAfter)
	if !strings.Contains(fstabStr, "# removed by boot-orchestrator revert") || !strings.Contains(fstabStr, fx.newOSPartUUID) {
		t.Errorf("expected the dangling new-OS fstab entry to be commented out, got:\n%s", fstabStr)
	}
	if !strings.Contains(fstabStr, "PARTUUID="+hostPartUUID+" / ext4 defaults 0 1") {
		t.Errorf("expected the live host fstab entry to be left untouched, got:\n%s", fstabStr)
	}
}

// TestExecuteDecommission_AbortsOnFilesystemMismatch is the regression
// test for the OTHER bug F finding: ExecuteDecommissionWithOptions never
// used TargetPartUUID/ExpectedFSType/ExpectedSizeBytes at all on Linux
// (unlike the macOS revert path), so if device naming had shifted between
// install and revert time, it would blindly trust a stale DiskDevice/
// PartitionNum and could delete the wrong partition. With the fix, a
// recorded expected filesystem type that doesn't match what's actually on
// the target device must abort before anything destructive happens.
func TestExecuteDecommission_AbortsOnFilesystemMismatch(t *testing.T) {
	requireRootForDestructiveLoopTest(t)
	fx := buildDualBootFixture(t)
	withScratchRevertPaths(t)

	params := RealRevertParams{
		DiskDevice:        fx.loopDev,
		PartitionNum:      fx.newOSPartNum,
		HostPartNum:       fx.hostPartNum,
		TargetPartUUID:    fx.newOSPartUUID,
		ExpectedFSType:    "ntfs", // wrong on purpose: the fixture's new-OS partition is ext4
		ExpectedSizeBytes: fx.newOSSizeBytes,
	}

	err := ExecuteDecommissionWithOptions(params, false, nil, nil)
	if err == nil {
		t.Fatalf("expected a filesystem-type mismatch to abort before any destructive operation")
	}
	if !strings.Contains(err.Error(), "safety abort") {
		t.Fatalf("expected a safety-abort error, got: %v", err)
	}

	// The new-OS partition must still exist — nothing destructive should
	// have run. "NEWOS" is the ext4 filesystem label (set by mkfs.ext4 -L),
	// not the GPT partition name parted prints, so check it via blkid,
	// which only resolves if the partition and its filesystem are intact.
	label := strings.TrimSpace(mustRun(t, "sudo", "blkid", "-s", "LABEL", "-o", "value", fx.newOSPart))
	if label != "NEWOS" {
		t.Errorf("expected the new-OS partition (label NEWOS) to still exist after an aborted revert, got label %q", label)
	}
}

// TestExecuteDecommission_RecreatesOriginalSwapPartition is follow-up
// F14's reproduction for swap recreation: the fixture's "new OS" partition
// stands in for a partition that install-time partitioning carved by
// converting a confirmed trailing swap slice (same size, just reformatted)
// — so OriginalSwapSizeBytes/OriginalSwapUUID are populated exactly as
// RunProvision would populate them in that case. Before the fix, the host
// partition always grew to reclaim 100% of the freed space, permanently
// losing that swap space; with the fix, the host must stop short and an
// equivalent swap partition (same size, same restored PARTUUID) must exist
// afterward, formatted and referenced from fstab.
func TestExecuteDecommission_RecreatesOriginalSwapPartition(t *testing.T) {
	requireRootForDestructiveLoopTest(t)
	fx := buildDualBootFixture(t)
	env := withScratchRevertPaths(t)

	const originalSwapUUID = "1234abcd-5678-90ef-1234-567890abcdef"

	params := RealRevertParams{
		DiskDevice:            fx.loopDev,
		PartitionNum:          fx.newOSPartNum,
		HostPartNum:           fx.hostPartNum,
		TargetPartUUID:        fx.newOSPartUUID,
		ExpectedFSType:        "ext4",
		ExpectedSizeBytes:     fx.newOSSizeBytes,
		OriginalSwapSizeBytes: fx.newOSSizeBytes,
		OriginalSwapUUID:      originalSwapUUID,
	}

	var logLines []string
	err := ExecuteDecommissionWithOptions(params, false, func(s string) { logLines = append(logLines, s) }, nil)
	if err != nil {
		t.Fatalf("ExecuteDecommissionWithOptions: %v\nlog:\n%s", err, strings.Join(logLines, "\n"))
	}

	// 1. The host partition must have grown, but NOT all the way to 100% —
	// it must have stopped short by (approximately) the swap size, leaving
	// room for the recreated swap partition.
	hostSizeBytes := parseBlockdevBytes(t, mustRun(t, "sudo", "blockdev", "--getsize64", fx.hostPart))
	const fullyGrownAtLeast = int64(1700) * 1024 * 1024 // what the no-swap test observes
	if hostSizeBytes >= fullyGrownAtLeast {
		t.Errorf("expected the host partition to stop short of full growth to leave room for swap, got %d bytes (>= %d)", hostSizeBytes, fullyGrownAtLeast)
	}

	// 2. A swap partition must exist again at the same partition number,
	// sized approximately like the original, and formatted as swap.
	newSwapPart := JoinDiskAndPartition(fx.loopDev, fx.newOSPartNum)
	fsType := strings.TrimSpace(mustRun(t, "sudo", "blkid", "-s", "TYPE", "-o", "value", newSwapPart))
	if fsType != "swap" {
		t.Fatalf("expected %s to be formatted as swap after revert, got filesystem type %q\nlog:\n%s", newSwapPart, fsType, strings.Join(logLines, "\n"))
	}

	swapSizeBytes := parseBlockdevBytes(t, mustRun(t, "sudo", "blockdev", "--getsize64", newSwapPart))
	const marginBytes = 16 * 1024 * 1024
	diff := swapSizeBytes - fx.newOSSizeBytes
	if diff < 0 {
		diff = -diff
	}
	if diff > marginBytes {
		t.Errorf("expected the recreated swap partition to be ~%d bytes (original size), got %d bytes", fx.newOSSizeBytes, swapSizeBytes)
	}

	// 3. Its PARTUUID must have been restored to the original swap slice's
	// recorded PARTUUID, so an fstab entry referencing it by PARTUUID
	// keeps working across the revert.
	gotPartUUID := strings.ToLower(strings.TrimSpace(mustRun(t, "sudo", "blkid", "-s", "PARTUUID", "-o", "value", newSwapPart)))
	if gotPartUUID != originalSwapUUID {
		t.Errorf("expected the recreated swap partition's PARTUUID to be restored to %q, got %q", originalSwapUUID, gotPartUUID)
	}

	// 4. fstab must now reference the recreated swap partition by PARTUUID.
	fstabAfter, err := os.ReadFile(env.fstabPath)
	if err != nil {
		t.Fatalf("reading scratch fstab after revert: %v", err)
	}
	if !strings.Contains(strings.ToLower(string(fstabAfter)), "partuuid="+originalSwapUUID+" none swap sw 0 0") {
		t.Errorf("expected fstab to contain a swap entry for %s, got:\n%s", originalSwapUUID, string(fstabAfter))
	}

	// 5. Host files must still be byte-for-byte unchanged — recreating
	// swap must not have touched the host's own filesystem contents.
	mustRun(t, "sudo", "e2fsck", "-fy", fx.hostPart)
	dir := t.TempDir()
	mustRun(t, "sudo", "mount", fx.hostPart, dir)
	defer exec.Command("sudo", "umount", "-l", dir).Run()
	for name, wantSHA := range fx.hostFiles {
		got := sha256OfFile(t, filepath.Join(dir, name))
		if got != wantSHA {
			t.Errorf("file %s: checksum changed across revert (want %s, got %s)", name, wantSHA, got)
		}
	}
}

// parseBlockdevBytes parses the decimal byte count `blockdev --getsize64`
// prints (with a trailing newline) into an int64.
func parseBlockdevBytes(t *testing.T, out string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		t.Fatalf("parsing blockdev output %q: %v", out, err)
	}
	return n
}
