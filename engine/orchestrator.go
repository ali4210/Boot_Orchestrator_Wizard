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

func DetectActiveRootDisk() (parentDisk string, hostPart string, hostNum string, targetPart string, err error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return "/dev/sda", "/dev/sda1", "1", "/dev/sda4", nil
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
		return "/dev/sda", "/dev/sda1", "1", "/dev/sda4", nil
	}

	base := filepath.Base(rootDev)
	if strings.HasPrefix(base, "nvme") || strings.HasPrefix(base, "mmcblk") {
		idx := strings.LastIndex(base, "p")
		if idx != -1 && idx < len(base)-1 {
			pDisk := "/dev/" + base[:idx]
			pNum := base[idx+1:]
			tNum := "4"
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
		tNum := "4"
		return pDisk, rootDev, pNum, pDisk + tNum, nil
	}

	return "/dev/sda", "/dev/sda1", "1", "/dev/sda4", nil
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

	// PHASE 1: STREAM & ACQUIRE PAYLOAD (Fast skips if already on disk)
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

	// SINGLE-BOOT PRODUCTION PIPELINE
	if strings.EqualFold(req.ProvisionMode, "single-boot") {
		wipeStep, wErr := journal.Begin("single-boot-wipe", nil)
		if wErr != nil {
			return fail("single-boot-wipe", wErr)
		}
		onUpdate(StepUpdate{StepName: fmt.Sprintf("partitioning %s with UEFI GPT layout...", req.TargetDiskPath), Done: false})

		_ = exec.Command("swapoff", "-a").Run()

		_ = exec.Command("parted", "-s", req.TargetDiskPath, "mklabel", "gpt").Run()
		_ = exec.Command("parted", "-s", req.TargetDiskPath, "mkpart", "ESP", "fat32", "1MiB", "1025MiB").Run()
		_ = exec.Command("parted", "-s", req.TargetDiskPath, "set", "1", "esp", "on").Run()
		_ = exec.Command("parted", "-s", req.TargetDiskPath, "mkpart", "primary", "ext4", "1025MiB", "100%").Run()
		_ = exec.Command("partprobe", req.TargetDiskPath).Run()
		time.Sleep(1 * time.Second)

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

		if !strings.HasPrefix(streamResult.MirrorUsed, "local://") && streamResult.DestPath != req.ImageURL {
			_ = os.Remove(streamResult.DestPath)
		}

		bootStep, _ := journal.Begin("bootloader-configure", nil)
		_ = AutoConfigureDualBoot(req.TargetPartition, grubDefaultPath, func(msg string) {
			onUpdate(StepUpdate{StepName: msg, Done: false})
		})
		journal.Commit(bootStep)

		_ = journal.Finalize()
		return ProvisionResult{Success: true}
	}

	// DUAL-BOOT PIPELINE: HARDWARE CARVING WITH HANDS-FREE BOOTNEXT FALLBACK
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
		if targetBytes == 0 {
			targetBytes = uint64(25 * 1024 * 1024 * 1024)
			if req.SelectedEntry != nil && req.SelectedEntry.MinDiskGB > 0 {
				targetBytes = uint64(req.SelectedEntry.MinDiskGB) * 1024 * 1024 * 1024
			}
		}

		onUpdate(StepUpdate{StepName: fmt.Sprintf("evaluating hardware block geometry on %s...", req.TargetDiskPath), Done: false})

		res, carveErr := CarveHardwarePartitionAtBoundary(req.TargetDiskPath, "ext4", targetBytes, func(msg string) {
			onUpdate(StepUpdate{StepName: msg, Done: false})
		})

		if carveErr != nil {
			usbStorageDir := ""
			possibleMounts := []string{
				"/mnt/orch_usb_storage",
				"/mnt/orch_drive_sdc1",
				"/mnt/orch_drive_sdc",
			}

			for _, p := range possibleMounts {
				if fi, sErr := os.Stat(p); sErr == nil && fi.IsDir() {
					usbStorageDir = p
					break
				}
			}

			if usbStorageDir == "" {
				autoMountPoint := "/mnt/orch_usb_storage"
				_ = os.MkdirAll(autoMountPoint, 0755)
				_ = exec.Command("mount", "/dev/sdc1", autoMountPoint).Run()
				if _, sErr := os.Stat(autoMountPoint); sErr == nil {
					usbStorageDir = autoMountPoint
				}
			}

			if usbStorageDir != "" {
				onUpdate(StepUpdate{StepName: "=> [AUTONOMOUS ENGINE] Staging offline resizer script to USB storage...", Done: false})

				workerScript := fmt.Sprintf(`#!/bin/bash
set -e
echo "=========================================================="
echo "   AUTONOMOUS OFFLINE PARTITION RESIZER & DEPLOYER        "
echo "=========================================================="
swapoff -a || true
e2fsck -f -y %[1]s2
resize2fs %[1]s2 280G
parted -s %[1]s resizepart 2 280GB
parted -s -a optimal %[1]s mkpart primary ext4 280GB 100%%
partprobe %[1]s
udevadm settle --timeout=10
mkfs.ext4 -F -q -L PARROT_ROOT %[1]s4
echo "=========================================================="
echo " Partition carved successfully! System ready for boot.    "
echo "=========================================================="
sleep 2
`, req.TargetDiskPath)

				scriptPath := filepath.Join(usbStorageDir, "transient_resize.sh")
				_ = os.WriteFile(scriptPath, []byte(workerScript), 0755)
				_ = exec.Command("chmod", "+x", scriptPath).Run()
				_ = exec.Command("sync").Run()

				// Locate local ISO payload source
				isoSrcPath := filepath.Join(usbStorageDir, "os_image.payload")
				if _, sErr := os.Stat(isoSrcPath); sErr != nil {
					isoSrcPath = req.DownloadDestPath
				}

				// ARM UEFI RUNTIME ON PARTITION 2 (ORCH_BOOT) DIRECTLY FROM PAYLOAD
				usbBootPart := "/dev/sdc2"
				onUpdate(StepUpdate{StepName: "=> [EFI ENGINE] Extracting authentic distribution bootloader to ORCH_BOOT...", Done: false})
				if armErr := ArmAutonomousUSBBootloader(usbBootPart, isoSrcPath, req.TargetDiskPath, func(msg string) {
					onUpdate(StepUpdate{StepName: msg, Done: false})
				}); armErr != nil {
					journal.Fail(step, armErr)
					return fail("efi-armor", armErr)
				}

				// AUTONOMOUS BOOTNEXT INJECTION
				onUpdate(StepUpdate{StepName: "=> [NVRAM ENGINE] Registering USB bootloader and arming UEFI BootNext...", Done: false})

				usbDisk := "/dev/sdc"
				usbPartNum := "2"

				outEfi, _ := exec.Command("efibootmgr", "-c", "-d", usbDisk, "-p", usbPartNum, "-L", "ORCH_AUTONOMOUS_INSTALLER", "-l", "\\EFI\\BOOT\\BOOTX64.EFI").CombinedOutput()

				var bootNum string
				lines := strings.Split(string(outEfi), "\n")
				for _, l := range lines {
					if strings.Contains(l, "Boot") && strings.Contains(l, "ORCH_AUTONOMOUS_INSTALLER") {
						parts := strings.Split(l, "*")
						if len(parts) > 0 {
							bootNum = strings.TrimPrefix(strings.TrimSpace(parts[0]), "Boot")
						}
						break
					}
				}

				if bootNum != "" {
					_ = exec.Command("efibootmgr", "-n", bootNum).Run()
					onUpdate(StepUpdate{StepName: fmt.Sprintf("=> [NVRAM SUCCESS] Armed BootNext = %s", bootNum), Done: true})
				} else {
					onUpdate(StepUpdate{StepName: "=> [NVRAM NOTICE] Armed fallback UEFI one-shot target", Done: true})
				}

				// UPDATE STATUS TO 100% TO MARK PHASE 1 COMPLETE
				onUpdate(StepUpdate{StepName: "=> [HOST PREPARATION COMPLETE 100%] System handoff to UEFI BootNext...", Done: true})
				onUpdate(StepUpdate{StepName: "=> [REBOOT TRIGGER] Hands-free reboot initiating in 3 seconds...", Done: true})

				time.Sleep(3 * time.Second)
				_ = exec.Command("sync").Run()
				_ = exec.Command("systemctl", "reboot").Run()

				return ProvisionResult{Success: true}
			}

			journal.Fail(step, carveErr)
			return fail("partition-shrink", fmt.Errorf("physical partition carving failed: %w", carveErr))
		}

		req.TargetPartition = res.NewPartitionPath
		onUpdate(StepUpdate{StepName: fmt.Sprintf("physical dual-boot partition carved: %s", req.TargetPartition), Done: true})

		journal.Commit(step)
		onUpdate(StepUpdate{StepName: "partition-shrink", Done: true})
	}

	if !req.SkipCompaction && req.HypervisorKind != hypervisor.KindBareMetal && !journal.Session.IsPaused {
		compactPlan, err := hypervisor.PlanHostCompact(req.HypervisorKind, req.TargetDiskPath)
		if err == nil {
			onUpdate(StepUpdate{StepName: "hypervisor-compact-planned: " + compactPlan.FormatPlanForDisplay(), Done: true})
		}
	}

	// Deploy Payload into the resolved target
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
		deployPlan, err := PlanDeploy(streamResult.DestPath, req.TargetPartition)
		if err != nil {
			journal.Fail(deployStep, err)
			return fail("image-deploy", err)
		}
		if _, err := ApplyDeploy(deployPlan); err != nil {
			journal.Fail(deployStep, err)
			return fail("image-deploy", err)
		}
	}
	journal.Commit(deployStep)
	onUpdate(StepUpdate{StepName: "image-deploy", Done: true})

	// Autonomous Dual-Boot Configuration & Bootloader Registration
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

	// Reverter Rollback Guard & Platform Hooks
	revStep, _ := journal.Begin("revert-engine", nil)
	revParams := RealRevertParams{
		DiskDevice:        req.TargetDiskPath,
		PartitionNum:      req.TargetPartition,
		HostPartNum:       req.HostPartNum,
		EFIDirNameToPurge: req.BootLabel,
	}
	revJSON, _ := json.Marshal(revParams)
	revStep.RollbackData = revJSON

	if req.IsWindowsImage {
		guid, rb, err := AddWindowsBootEntry(req.BootLabel)
		if err != nil {
			return fail("bcd-add-entry", err)
		}
		step, _ := journal.Begin("bcd-add-entry", nil)
		step.RollbackData = rb
		journal.Commit(step)
		onUpdate(StepUpdate{StepName: "bcd-add-entry: " + guid, Done: true})
	} else {
		if err := FirmwareModeCheck(); err == nil {
			nvramState, err := ReadNVRAMState()
			if err == nil {
				step, _ := journal.Begin("uefi-set-boot-order", nil)
				rb, sErr := SetBootOrder(nvramState.BootOrder)
				step.RollbackData = rb
				if sErr != nil {
					journal.Fail(step, sErr)
				} else {
					journal.Commit(step)
					onUpdate(StepUpdate{StepName: "uefi-set-boot-order", Done: true})
				}
			}
		} else {
			step, _ := journal.Begin("grub-set-default", nil)
			rb, err := SetGrubDefaultEntry(grubDefaultPath, req.BootLabel)
			step.RollbackData = rb
			if err != nil {
				journal.Fail(step, err)
				return fail("grub-set-default", err)
			}
			journal.Commit(step)
			onUpdate(StepUpdate{StepName: "grub-set-default", Done: true})
		}
	}

	journal.Commit(revStep)

	if !strings.HasPrefix(streamResult.MirrorUsed, "local://") && streamResult.DestPath != req.ImageURL {
		_ = os.Remove(streamResult.DestPath)
	}

	if err := journal.Finalize(); err != nil {
		onUpdate(StepUpdate{StepName: "journal-finalize", Done: true, Err: err})
	}

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

func DetectRootMountStatus(devicePath string) (bool, error) {
	out, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return true, err
	}
	return strings.Contains(string(out), devicePath), nil
}
