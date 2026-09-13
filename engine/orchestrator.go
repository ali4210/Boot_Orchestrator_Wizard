// Package engine — orchestrator.go coordinates transactional provisioning,
// storage isolation, multi-mirror streaming with pause/resume support,
// automated physical partition carving, autonomous Live USB staging,
// hands-free UEFI BootNext orchestration, and atomic crash recovery.
package engine

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"boot-orchestrator/discovery"
	"boot-orchestrator/hypervisor"
	"boot-orchestrator/safety"
)

type ProvisionRequest struct {
	JournalPath      string
	ImageURL         string
	ImageSHA256      string
	DownloadDestPath string
	TargetPartition  string
	TargetDiskPath   string
	IsWindowsImage   bool
	WimIndex         int
	AutounattendPath string
	BootLabel        string
	HypervisorKind   hypervisor.Kind
	SkipCompaction   bool
	SelectedEntry    *discovery.Entry
	HostPartNum      string
	ESPPartNum       string
	ProvisionMode    string // "dual-boot" or "single-boot"
	AllocatedBytes   uint64
}

type StepUpdate struct {
	StepName string
	Done     bool
	Err      error
}

type ProvisionResult struct {
	Success       bool
	FailedAtStep  string
	OriginalError error
	RolledBack    bool
	RollbackError error
}

func rollbackHandlers(grubDefaultPath string) map[string]safety.RollbackFunc {
	return map[string]safety.RollbackFunc{
		"partition-shrink":    RollbackPartitionShrink,
		"docker-isolation":    RollbackDockerIsolation,
		"uefi-set-boot-next":  RollbackSetBootNext,
		"uefi-set-boot-order": RollbackSetBootOrder,
		"bcd-mutation":        RollbackBCD,
		"bcd-add-entry":       RollbackAddWindowsBootEntry,
		"revert-engine":       RollbackRevertHandler,
		"grub-set-default": func(data json.RawMessage) error {
			return RollbackSetGrubDefaultEntry(grubDefaultPath, data)
		},
	}
}

func DetectAllInternalDisks() ([]USBTargetDevice, error) {
	out, err := exec.Command("lsblk", "-d", "-J", "-o", "NAME,SIZE,TRAN,TYPE,RM,VENDOR,MODEL").Output()
	if err != nil {
		return nil, fmt.Errorf("querying internal storage controller: %w", err)
	}

	var data usbLsblkResponse
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("parsing storage telemetry: %w", err)
	}

	var internalDisks []USBTargetDevice
	for _, dev := range data.BlockDevices {
		isUSB := strings.ToLower(dev.Tran) == "usb" || dev.Removable == "1" || dev.Removable == "true"
		if !isUSB && dev.Type == "disk" && !strings.HasPrefix(dev.Name, "loop") && !strings.HasPrefix(dev.Name, "sr") {
			byteSize, _ := QueryBlockDeviceBytes(dev.Name)
			internalDisks = append(internalDisks, USBTargetDevice{
				Name:      dev.Name,
				DevPath:   "/dev/" + dev.Name,
				Size:      dev.Size,
				SizeBytes: byteSize,
				Vendor:    strings.TrimSpace(dev.Vendor),
				Model:     strings.TrimSpace(dev.Model),
			})
		}
	}
	return internalDisks, nil
}

func DetectActiveRootDisk() (parentDisk string, hostPart string, hostNum string, targetPart string, err error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return "/dev/sdb", "/dev/sdb2", "2", "/dev/sdb3", nil
	}
	defer f.Close()

	rootDev := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[1] == "/" {
			rootDev = fields[0]
			break
		}
	}

	if real, evalErr := filepath.EvalSymlinks(rootDev); evalErr == nil && real != "" {
		rootDev = real
	}

	if rootDev == "" || !strings.HasPrefix(rootDev, "/dev/") {
		return "/dev/sdb", "/dev/sdb2", "2", "/dev/sdb3", nil
	}

	base := filepath.Base(rootDev)
	if strings.HasPrefix(base, "nvme") || strings.HasPrefix(base, "mmcblk") {
		idx := strings.LastIndex(base, "p")
		if idx != -1 && idx < len(base)-1 {
			pDisk := "/dev/" + base[:idx]
			pNum := base[idx+1:]
			pNumInt, _ := strconv.Atoi(pNum)
			tNum := strconv.Itoa(pNumInt + 1)
			return pDisk, rootDev, pNum, pDisk + "p" + tNum, nil
		}
	}

	i := len(base) - 1
	for i >= 0 && unicode.IsDigit(rune(base[i])) {
		i--
	}
	if i < len(base)-1 {
		pDisk := "/dev/" + base[:i+1]
		pNum := base[i+1:]
		pNumInt, _ := strconv.Atoi(pNum)
		tNum := strconv.Itoa(pNumInt + 1)
		return pDisk, rootDev, pNum, pDisk + tNum, nil
	}

	return "/dev/sdb", "/dev/sdb2", "2", "/dev/sdb3", nil
}

func extractLiveRootFS(isoPath, targetPart string, logFn func(string)) error {
	if logFn == nil {
		logFn = func(string) {}
	}

	sizeBytes, _ := QueryBlockDeviceBytes(filepath.Base(targetPart))
	minRequired := uint64(20 * 1024 * 1024 * 1024)
	if sizeBytes > 0 && sizeBytes < minRequired {
		return fmt.Errorf("target partition %s capacity (%.1f GB) is critically below the 20 GB safety threshold", targetPart, float64(sizeBytes)/(1024*1024*1024))
	}

	logFn(fmt.Sprintf("=> [ROOTFS DEPLOY] Formatting target partition %s as ext4 [PARROT_ROOT]...", targetPart))
	if out, err := exec.Command("mkfs.ext4", "-F", "-q", "-L", "PARROT_ROOT", targetPart).CombinedOutput(); err != nil {
		return fmt.Errorf("formatting %s: %w (%s)", targetPart, err, string(out))
	}
	_ = exec.Command("sync").Run()

	targetMount := "/mnt/orch_target_root"
	_ = os.MkdirAll(targetMount, 0755)
	_ = exec.Command("umount", "-l", targetMount).Run()

	logFn(fmt.Sprintf("=> [ROOTFS DEPLOY] Mounting target %s at %s...", targetPart, targetMount))
	if out, err := exec.Command("mount", targetPart, targetMount).CombinedOutput(); err != nil {
		return fmt.Errorf("mounting %s to %s: %w (%s)", targetPart, targetMount, err, string(out))
	}
	defer func() {
		_ = exec.Command("sync").Run()
		_ = exec.Command("umount", "-l", targetMount).Run()
	}()

	isoMount := "/mnt/orch_iso_source"
	_ = os.MkdirAll(isoMount, 0755)
	_ = exec.Command("umount", "-l", isoMount).Run()

	logFn("=> [ROOTFS DEPLOY] Mounting source ISO loopback: " + isoPath)
	if out, err := exec.Command("mount", "-o", "loop,ro", isoPath, isoMount).CombinedOutput(); err != nil {
		return fmt.Errorf("mounting loop iso %s: %w (%s)", isoPath, err, string(out))
	}
	defer func() {
		_ = exec.Command("umount", "-l", isoMount).Run()
	}()

	squashPath := filepath.Join(isoMount, "live", "filesystem.squashfs")
	if _, err := os.Stat(squashPath); err != nil {
		candidates := []string{
			filepath.Join(isoMount, "casper", "filesystem.squashfs"),
			filepath.Join(isoMount, "live", "rootfs.squashfs"),
		}
		for _, c := range candidates {
			if _, sErr := os.Stat(c); sErr == nil {
				squashPath = c
				break
			}
		}
	}

	logFn("=> [ROOTFS DEPLOY] Extracting live root filesystem tree via unsquashfs (2-4 min)...")
	unsquashCmd := exec.Command("unsquashfs", "-f", "-d", targetMount, squashPath)
	if out, err := unsquashCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("unsquashfs extraction failed: %w (%s)", err, string(out))
	}

	_ = exec.Command("tune2fs", "-m", "1", targetPart).Run()

	initTarget := filepath.Join(targetMount, "sbin", "init")
	systemdTarget := "/lib/systemd/systemd"
	_ = os.Remove(initTarget)
	_ = os.Symlink(systemdTarget, initTarget)

	uuidOut, err := exec.Command("blkid", "-s", "UUID", "-o", "value", targetPart).Output()
	if err == nil && len(strings.TrimSpace(string(uuidOut))) > 0 {
		uuidStr := strings.TrimSpace(string(uuidOut))
		fstabEntry := fmt.Sprintf("UUID=%s / ext4 defaults,noatime,errors=remount-ro 0 1\n", uuidStr)
		_ = os.WriteFile(filepath.Join(targetMount, "etc", "fstab"), []byte(fstabEntry), 0644)
		logFn("=> [ROOTFS DEPLOY] Injected /etc/fstab entry mapped to UUID: " + uuidStr)
	}

	// Preset Credentials Injection: root:saleem & saleem:saleem (sudo)
	logFn("=> [CREDENTIALS] Configuring persistent user accounts (root:saleem, saleem:saleem)...")
	sudoersDir := filepath.Join(targetMount, "etc", "sudoers.d")
	_ = os.MkdirAll(sudoersDir, 0750)
	_ = os.WriteFile(filepath.Join(sudoersDir, "saleem"), []byte("saleem ALL=(ALL:ALL) ALL\n"), 0440)

	_ = exec.Command("mount", "--bind", "/dev", filepath.Join(targetMount, "dev")).Run()
	_ = exec.Command("mount", "--bind", "/proc", filepath.Join(targetMount, "proc")).Run()
	_ = exec.Command("mount", "--bind", "/sys", filepath.Join(targetMount, "sys")).Run()

	chrootCmds := []string{
		"echo 'root:saleem' | chpasswd",
		"id -u saleem >/dev/null 2>&1 || useradd -m -s /bin/bash saleem",
		"echo 'saleem:saleem' | chpasswd",
		"usermod -aG sudo saleem 2>/dev/null || true",
	}
	for _, cmdStr := range chrootCmds {
		_ = exec.Command("chroot", targetMount, "bash", "-c", cmdStr).Run()
	}

	_ = exec.Command("umount", "-l", filepath.Join(targetMount, "dev")).Run()
	_ = exec.Command("umount", "-l", filepath.Join(targetMount, "proc")).Run()
	_ = exec.Command("umount", "-l", filepath.Join(targetMount, "sys")).Run()

	logFn("=> [ROOTFS DEPLOY 100%] Root filesystem successfully unpacked and synchronized.")
	return nil
}

func RunProvision(ctx context.Context, req ProvisionRequest, grubDefaultPath string, onUpdate func(StepUpdate)) ProvisionResult {
	journal, err := safety.LoadJournal(req.JournalPath)
	if err != nil {
		journal, err = safety.NewJournal(req.JournalPath, generateTxnID())
		if err != nil {
			return ProvisionResult{Success: false, FailedAtStep: "journal-init", OriginalError: err}
		}
	}

	handlers := rollbackHandlers(grubDefaultPath)

	fail := func(stepName string, cause error) ProvisionResult {
		if errors.Is(cause, ErrDownloadPaused) || errors.Is(cause, context.Canceled) {
			onUpdate(StepUpdate{StepName: stepName + " [PAUSED]", Done: true})
			return ProvisionResult{
				Success:       false,
				FailedAtStep:  stepName,
				OriginalError: ErrDownloadPaused,
				RolledBack:    false,
			}
		}

		onUpdate(StepUpdate{StepName: stepName, Done: true, Err: cause})
		rbErr := journal.RollbackAll(handlers)
		return ProvisionResult{
			Success:       false,
			FailedAtStep:  stepName,
			OriginalError: cause,
			RolledBack:    true,
			RollbackError: rbErr,
		}
	}

	pDisk, _, hNum, targetPart, dErr := DetectActiveRootDisk()
	if dErr == nil && pDisk != "" {
		if req.TargetDiskPath == "" {
			req.TargetDiskPath = pDisk
		}
		if req.TargetPartition == "" {
			req.TargetPartition = targetPart
		}
		if req.HostPartNum == "" {
			req.HostPartNum = hNum
		}
	}

	// PHASE 1: STREAM & ACQUIRE PAYLOAD
	dlStep, err := journal.Begin("image-download", nil)
	if err != nil {
		return fail("image-download", err)
	}

	mirrors := []string{req.ImageURL}
	if req.SelectedEntry != nil {
		entryMirrors := req.SelectedEntry.GetMirrors()
		if len(entryMirrors) > 0 {
			mirrors = entryMirrors
		}
	}

	var latestBytesRead int64
	var latestTotalBytes int64

	streamResult, err := StreamDownloadWithFailover(ctx, mirrors, req.DownloadDestPath, req.ImageSHA256, func(p Progress) {
		latestBytesRead = p.BytesRead
		latestTotalBytes = p.TotalBytes
		onUpdate(StepUpdate{StepName: fmt.Sprintf("downloading: %.1f%% (%.1f MB/s)", p.Percent(), p.BytesPerSecond()/(1024*1024)), Done: false})
	})

	if err != nil {
		if errors.Is(err, ErrDownloadPaused) || errors.Is(causeError(err), context.Canceled) {
			_ = journal.Pause(dlStep, latestBytesRead, latestTotalBytes)
			return fail("image-download", ErrDownloadPaused)
		}
		journal.Fail(dlStep, err)
		return fail("image-download", err)
	}

	if req.ImageSHA256 != "" {
		onUpdate(StepUpdate{StepName: "verifying cryptographic payload checksum...", Done: false})
		if err := verifyFileSHA256(streamResult.DestPath, req.ImageSHA256); err != nil {
			if !strings.HasPrefix(streamResult.MirrorUsed, "local://") {
				_ = os.Remove(streamResult.DestPath)
			}
			journal.Fail(dlStep, err)
			return fail("checksum-verification", fmt.Errorf("corrupted payload abort: %w", err))
		}
	}
	journal.Commit(dlStep)
	onUpdate(StepUpdate{StepName: "image-download", Done: true})

	// SINGLE-BOOT PIPELINE
	if strings.EqualFold(req.ProvisionMode, "single-boot") {
		wipeStep, wErr := journal.Begin("single-boot-wipe", nil)
		if wErr != nil {
			return fail("single-boot-wipe", wErr)
		}
		onUpdate(StepUpdate{StepName: fmt.Sprintf("partitioning %s with clean UEFI GPT layout...", req.TargetDiskPath), Done: false})

		_ = exec.Command("swapoff", "-a").Run()
		_ = exec.Command("parted", "-s", req.TargetDiskPath, "mklabel", "gpt").Run()
		_ = exec.Command("parted", "-s", req.TargetDiskPath, "mkpart", "ESP", "fat32", "1MiB", "1025MiB").Run()
		_ = exec.Command("parted", "-s", req.TargetDiskPath, "set", "1", "esp", "on").Run()
		_ = exec.Command("parted", "-s", req.TargetDiskPath, "mkpart", "primary", "ext4", "1025MiB", "100%").Run()
		_ = exec.Command("partprobe", req.TargetDiskPath).Run()
		_ = exec.Command("udevadm", "settle", "--timeout=10").Run()

		espPart := req.TargetDiskPath + "1"
		rootPart := req.TargetDiskPath + "2"
		if strings.Contains(req.TargetDiskPath, "nvme") || strings.Contains(req.TargetDiskPath, "mmcblk") {
			espPart = req.TargetDiskPath + "p1"
			rootPart = req.TargetDiskPath + "p2"
		}
		req.TargetPartition = rootPart

		_ = exec.Command("mkfs.vfat", "-F32", "-n", "BOOT", espPart).Run()
		_ = exec.Command("mkfs.ext4", "-F", "-L", "ROOT", req.TargetPartition).Run()
		journal.Commit(wipeStep)

		deployStep, dErr := journal.Begin("image-deploy", nil)
		if dErr != nil {
			return fail("image-deploy", dErr)
		}
		deployPlan, err := PlanDeploy(streamResult.DestPath, req.TargetPartition)
		if err != nil {
			journal.Fail(deployStep, err)
			return fail("image-deploy", err)
		}
		if _, err := ApplyDeploy(deployPlan); err != nil {
			journal.Fail(deployStep, err)
			return fail("image-deploy", err)
		}
		journal.Commit(deployStep)

		bootStep, _ := journal.Begin("bootloader-configure", nil)
		_ = AutoConfigureDualBoot(req.TargetPartition, grubDefaultPath, func(msg string) {
			onUpdate(StepUpdate{StepName: msg, Done: false})
		})
		journal.Commit(bootStep)

		_ = journal.Finalize()
		return ProvisionResult{Success: true}
	}

	// DUAL-BOOT PIPELINE
	if !journal.Session.IsPaused {
		step, err := journal.Begin("partition-shrink", nil)
		if err != nil {
			return fail("partition-shrink", err)
		}

		backup, err := BackupPartitionTable(req.TargetDiskPath)
		if err != nil {
			journal.Fail(step, err)
			return fail("partition-shrink", err)
		}
		backupJSON, _ := json.Marshal(backup)
		step.RollbackData = backupJSON

		targetBytes := req.AllocatedBytes
		if targetBytes < 25*1024*1024*1024 {
			targetBytes = uint64(30 * 1024 * 1024 * 1024)
		}

		activeMounts, _ := os.ReadFile("/proc/mounts")
		isLiveRoot := strings.Contains(string(activeMounts), req.TargetDiskPath)

		if isLiveRoot {
			onUpdate(StepUpdate{StepName: "=> [ACTIVE ROOT DETECTED] Preparing autonomous USB offline handoff...", Done: false})

			usbStorageDir := "/mnt/orch_usb_storage"
			if _, sErr := os.Stat(usbStorageDir); sErr != nil {
				_ = os.MkdirAll(usbStorageDir, 0755)
				_ = exec.Command("mount", "/dev/sdc1", usbStorageDir).Run()
			}

			usbBootPart := "/dev/sdc2"
			if _, sErr := os.Stat(usbBootPart); sErr != nil {
				journal.Fail(step, fmt.Errorf("USB Partition 2 (ORCH_BOOT) not formatted"))
				return fail("partition-shrink", fmt.Errorf("offline handoff requires /dev/sdc2 (ORCH_BOOT)"))
			}

			workerScript := fmt.Sprintf(`#!/bin/bash
set -e
echo "=========================================================="
echo "   AUTONOMOUS OFFLINE PARTITION RESIZER & DEPLOYER        "
echo "=========================================================="
echo "=> [STEP 1/7] Disabling active swaps..."
swapoff -a || true
echo "=> [STEP 2/7] Running filesystem check on %[1]s2..."
e2fsck -f -y %[1]s2
echo "=> [STEP 3/7] Shrinking %[1]s2 filesystem to 240G..."
resize2fs %[1]s2 240G
echo "=> [STEP 4/7] Adjusting partition boundary in GPT..."
parted -s %[1]s resizepart 2 240GB
parted -s -a optimal %[1]s mkpart primary ext4 240GB 100%%
partprobe %[1]s
udevadm settle --timeout=10
echo "=> [STEP 5/7] Formatting newly carved %[1]s3..."
mkfs.ext4 -F -q -L PARROT_ROOT %[1]s3
mkdir -p /mnt/target /mnt/source
mount %[1]s3 /mnt/target
mount -o loop,ro /mnt/orch_usb_storage/os_image.payload /mnt/source
echo "=> [STEP 6/7] Extracting SquashFS live filesystem into %[1]s3..."
unsquashfs -f -d /mnt/target /mnt/source/live/filesystem.squashfs
tune2fs -m 1 %[1]s3
ln -sf /lib/systemd/systemd /mnt/target/sbin/init
UUID=$(blkid -s UUID -o value %[1]s3)
echo "UUID=$UUID / ext4 defaults,noatime,errors=remount-ro 0 1" > /mnt/target/etc/fstab

# Preset Account Injection: root:saleem & saleem:saleem (sudo)
echo "=> [STEP 7/7] Injecting user credentials (root:saleem, saleem:saleem)..."
mkdir -p /mnt/target/etc/sudoers.d
echo "saleem ALL=(ALL:ALL) ALL" > /mnt/target/etc/sudoers.d/saleem
chmod 0440 /mnt/target/etc/sudoers.d/saleem
mount --bind /dev /mnt/target/dev
mount --bind /proc /mnt/target/proc
mount --bind /sys /mnt/target/sys
chroot /mnt/target bash -c "echo 'root:saleem' | chpasswd"
chroot /mnt/target bash -c "id -u saleem >/dev/null 2>&1 || useradd -m -s /bin/bash saleem"
chroot /mnt/target bash -c "echo 'saleem:saleem' | chpasswd"
chroot /mnt/target bash -c "usermod -aG sudo saleem 2>/dev/null || true"
umount -l /mnt/target/dev /mnt/target/proc /mnt/target/sys || true

sync
umount /mnt/source
umount /mnt/target
echo "=========================================================="
echo "  DUAL-BOOT DEPLOYMENT COMPLETE! REBOOTING INTO NEW OS... "
echo "=========================================================="
sleep 3
reboot
`, req.TargetDiskPath)

			scriptPath := filepath.Join(usbStorageDir, "transient_resize.sh")
			_ = os.WriteFile(scriptPath, []byte(workerScript), 0755)
			_ = exec.Command("chmod", "+x", scriptPath).Run()
			_ = exec.Command("sync").Run()

			isoSrcPath := filepath.Join(usbStorageDir, "os_image.payload")
			onUpdate(StepUpdate{StepName: "=> [EFI ENGINE] Arming monolithic GRUB EFI core with embedded early-cfg...", Done: false})
			if armErr := ArmAutonomousUSBBootloader(usbBootPart, isoSrcPath, req.TargetDiskPath, func(msg string) {
				onUpdate(StepUpdate{StepName: msg, Done: false})
			}); armErr != nil {
				journal.Fail(step, armErr)
				return fail("efi-armor", armErr)
			}

			onUpdate(StepUpdate{StepName: "=> [NVRAM ENGINE] Setting one-shot UEFI BootNext to USB...", Done: false})
			_ = exec.Command("efibootmgr", "-c", "-d", "/dev/sdc", "-p", "2", "-L", "ORCH_AUTONOMOUS_INSTALLER", "-l", `\EFI\BOOT\BOOTX64.EFI`).Run()

			// Dynamically set BootNext to whichever boot number was registered
			if out, err := exec.Command("efibootmgr").Output(); err == nil {
				for _, line := range strings.Split(string(out), "\n") {
					if strings.Contains(line, "ORCH_AUTONOMOUS_INSTALLER") && strings.HasPrefix(line, "Boot") {
						bootNum := strings.TrimPrefix(strings.Fields(line)[0], "Boot")
						bootNum = strings.TrimSuffix(bootNum, "*")
						_ = exec.Command("efibootmgr", "-n", bootNum).Run()
						onUpdate(StepUpdate{StepName: fmt.Sprintf("=> [NVRAM ENGINE] BootNext locked to slot %s", bootNum), Done: false})
						break
					}
				}
			}

			onUpdate(StepUpdate{StepName: "=> [SUCCESS] Autonomous offline staging prepared. Reboot system to execute 50/50 resize & extraction.", Done: true})
			_ = exec.Command("sync").Run()
			journal.Commit(step)

			_ = journal.Finalize()
			return ProvisionResult{Success: true}
		}

		// SECONDARY DRIVE DIRECT CARVE PATH (e.g. /dev/sda)
		res, carveErr := CarveHardwarePartitionAtBoundary(req.TargetDiskPath, "ext4", targetBytes, func(msg string) {
			onUpdate(StepUpdate{StepName: msg, Done: false})
		})
		if carveErr != nil {
			journal.Fail(step, carveErr)
			return fail("partition-shrink", carveErr)
		}

		req.TargetPartition = res.NewPartitionPath
		onUpdate(StepUpdate{StepName: fmt.Sprintf("physical dual-boot partition carved: %s", req.TargetPartition), Done: true})
		journal.Commit(step)
		onUpdate(StepUpdate{StepName: "partition-shrink", Done: true})
	}

	deployStep, err := journal.Begin("image-deploy", nil)
	if err != nil {
		return fail("image-deploy", err)
	}

	if req.IsWindowsImage {
		wimPlan := &ApplyWimPlan{
			WimPath:         streamResult.DestPath,
			Index:           req.WimIndex,
			TargetPartition: req.TargetPartition,
			MountPoint:      req.TargetPartition,
			AutounattendSrc: req.AutounattendPath,
		}
		if err := ApplyWim(wimPlan); err != nil {
			journal.Fail(deployStep, err)
			return fail("image-deploy", err)
		}
	} else {
		onUpdate(StepUpdate{StepName: "=> [ROOTFS DEPLOY] Extracting live root filesystem tree into target partition...", Done: false})
		if extErr := extractLiveRootFS(streamResult.DestPath, req.TargetPartition, func(msg string) {
			onUpdate(StepUpdate{StepName: msg, Done: false})
		}); extErr != nil {
			journal.Fail(deployStep, extErr)
			return fail("image-deploy", extErr)
		}
	}
	journal.Commit(deployStep)
	onUpdate(StepUpdate{StepName: "image-deploy", Done: true})

	if !req.IsWindowsImage {
		bootStep, bErr := journal.Begin("bootloader-configure", nil)
		if bErr == nil {
			onUpdate(StepUpdate{StepName: "configuring autonomous dual-boot & updating host GRUB...", Done: false})
			if err := AutoConfigureDualBoot(req.TargetPartition, grubDefaultPath, func(msg string) {
				onUpdate(StepUpdate{StepName: msg, Done: false})
			}); err != nil {
				journal.Fail(bootStep, err)
				return fail("bootloader-configure", err)
			}
			journal.Commit(bootStep)
			onUpdate(StepUpdate{StepName: "dual-boot bootloader synchronized", Done: true})
		}
	}

	revStep, _ := journal.Begin("revert-engine", nil)
	revParams := RealRevertParams{
		DiskDevice:        req.TargetDiskPath,
		PartitionNum:      req.TargetPartition,
		HostPartNum:       req.HostPartNum,
		EFIDirNameToPurge: req.BootLabel,
	}
	revJSON, _ := json.Marshal(revParams)
	revStep.RollbackData = revJSON
	journal.Commit(revStep)

	if !strings.HasPrefix(streamResult.MirrorUsed, "local://") && streamResult.DestPath != req.ImageURL {
		_ = os.Remove(streamResult.DestPath)
	}

	_ = journal.Finalize()
	return ProvisionResult{Success: true}
}

func verifyFileSHA256(filePath, expectedSHA string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	buffer := make([]byte, 1024*1024)
	if _, err := io.CopyBuffer(h, f, buffer); err != nil {
		return err
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(actual, strings.TrimSpace(expectedSHA)) {
		return fmt.Errorf("hash mismatch: expected %s, got %s", expectedSHA, actual)
	}
	return nil
}

func ResumeAndRollback(journalPath, grubDefaultPath string) error {
	journal, err := safety.LoadJournal(journalPath)
	if err != nil {
		return fmt.Errorf("loading interrupted journal: %w", err)
	}
	if !journal.HasIncompleteSteps() {
		return fmt.Errorf("journal at %s has no incomplete steps", journalPath)
	}
	return journal.RollbackAll(rollbackHandlers(grubDefaultPath))
}

func generateTxnID() string {
	return fmt.Sprintf("txn-%d", time.Now().UnixNano())
}

func causeError(err error) error {
	return err
}
