// Package engine — orchestrator.go coordinates transactional provisioning,
// storage isolation, multi-mirror streaming with pause/resume support, and atomic crash recovery.
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
	ProvisionMode    string // e.g., "dual-boot" or "single-boot"
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

// DetectActiveRootDisk parses /proc/mounts to find the real physical disk and root partition.
func DetectActiveRootDisk() (parentDisk string, hostPart string, hostNum string, targetPart string, err error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return "/dev/sda", "/dev/sda1", "1", "/dev/sda2", nil
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
		return "/dev/sda", "/dev/sda1", "1", "/dev/sda2", nil
	}

	base := filepath.Base(rootDev)
	if strings.HasPrefix(base, "nvme") || strings.HasPrefix(base, "mmcblk") {
		idx := strings.LastIndex(base, "p")
		if idx != -1 && idx < len(base)-1 {
			pDisk := "/dev/" + base[:idx]
			pNum := base[idx+1:]
			tNum := "2"
			if pNum == "2" {
				tNum = "3"
			}
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
		tNum := "2"
		if pNum == "2" {
			tNum = "3"
		}
		return pDisk, rootDev, pNum, pDisk + tNum, nil
	}

	return "/dev/sda", "/dev/sda1", "1", "/dev/sda2", nil
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

	pDisk, hostPart, hNum, targetPart, dErr := DetectActiveRootDisk()
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

	// =========================================================================
	// SINGLE-BOOT PRODUCTION GUARD: DOWNLOAD & VERIFY PAYLOAD BEFORE TOUCHING DISK
	// =========================================================================
	if strings.EqualFold(req.ProvisionMode, "single-boot") {
		onUpdate(StepUpdate{StepName: "=> [DISASTER GUARD] Staging & verifying OS payload prior to disk modification...", Done: false})

		dlStep, err := journal.Begin("image-download-preflight", nil)
		if err != nil {
			return fail("image-download-preflight", err)
		}

		mirrors := []string{req.ImageURL}
		if req.SelectedEntry != nil {
			entryMirrors := req.SelectedEntry.GetMirrors()
			if len(entryMirrors) > 0 {
				mirrors = entryMirrors
			}
		}

		streamResult, sErr := StreamDownloadWithFailover(ctx, mirrors, req.DownloadDestPath, req.ImageSHA256, func(p Progress) {
			onUpdate(StepUpdate{StepName: fmt.Sprintf("preflight download: %.1f%% (%.1f MB/s)", p.Percent(), p.BytesPerSecond()/(1024*1024)), Done: false})
		})
		if sErr != nil {
			journal.Fail(dlStep, sErr)
			return fail("image-download-preflight", fmt.Errorf("payload preflight failed: host OS preserved without changes: %w", sErr))
		}

		// Cryptographic SHA256 assertion
		if req.ImageSHA256 != "" {
			onUpdate(StepUpdate{StepName: "verifying cryptographic payload checksum...", Done: false})
			if err := verifyFileSHA256(streamResult.DestPath, req.ImageSHA256); err != nil {
				_ = os.Remove(streamResult.DestPath)
				journal.Fail(dlStep, err)
				return fail("checksum-verification", fmt.Errorf("CORRUPTED PAYLOAD ABORT: %w", err))
			}
		}
		journal.Commit(dlStep)
		onUpdate(StepUpdate{StepName: "payload verified intact; proceeding with single-boot formatting", Done: true})

		// Format Disk for Single-Boot
		wipeStep, wErr := journal.Begin("single-boot-wipe", nil)
		if wErr != nil {
			return fail("single-boot-wipe", wErr)
		}
		onUpdate(StepUpdate{StepName: fmt.Sprintf("partitioning %s as single-boot primary...", req.TargetDiskPath), Done: false})

		// Unmount anything using the target
		_ = exec.Command("swapoff", "-a").Run()

		// Write fresh GPT/MBR partition table
		_ = exec.Command("parted", "-s", req.TargetDiskPath, "mklabel", "msdos").Run()
		_ = exec.Command("parted", "-s", req.TargetDiskPath, "mkpart", "primary", "ext4", "1MiB", "100%").Run()
		_ = exec.Command("parted", "-s", req.TargetDiskPath, "set", "1", "boot", "on").Run()
		_ = exec.Command("partprobe", req.TargetDiskPath).Run()
		time.Sleep(1 * time.Second)

		singlePart := req.TargetDiskPath + "1"
		if strings.Contains(req.TargetDiskPath, "nvme") {
			singlePart = req.TargetDiskPath + "p1"
		}
		req.TargetPartition = singlePart

		// Format filesystem
		_ = exec.Command("mkfs.ext4", "-F", "-L", "ROOT", req.TargetPartition).Run()
		journal.Commit(wipeStep)

		// Deploy payload
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
		_ = os.Remove(streamResult.DestPath)

		// Synchronize bootloader & credentials
		bootStep, _ := journal.Begin("bootloader-configure", nil)
		_ = AutoConfigureDualBoot(req.TargetPartition, grubDefaultPath, func(msg string) {
			onUpdate(StepUpdate{StepName: msg, Done: false})
		})
		journal.Commit(bootStep)

		_ = journal.Finalize()
		return ProvisionResult{Success: true}
	}

	// =========================================================================
	// DUAL-BOOT ISOLATION PIPELINE
	// =========================================================================
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

		requiredBytes := uint64(20 * 1024 * 1024 * 1024)
		if req.SelectedEntry != nil && req.SelectedEntry.MinDiskGB > 0 {
			requiredBytes = uint64(req.SelectedEntry.MinDiskGB) * 1024 * 1024 * 1024
		}

		// Check for unallocated free sectors at the end of the disk first
		freeBytes, freeErr := CheckUnallocatedHeadroom(req.TargetDiskPath)
		if freeErr == nil && freeBytes >= requiredBytes {
			onUpdate(StepUpdate{StepName: "unallocated space detected: carving secondary partition directly", Done: false})
			res, carveErr := CarvePartitionInFreeSpace(req.TargetDiskPath, "ext4", int(requiredBytes/(1024*1024*1024)))
			if carveErr != nil {
				journal.Fail(step, carveErr)
				return fail("partition-shrink", carveErr)
			}
			req.TargetPartition = res.NewPartitionPath
			onUpdate(StepUpdate{StepName: "partition carved & formatted: " + req.TargetPartition, Done: true})
		} else {
			mountCheck, _ := DetectRootMountStatus(hostPart)
			if mountCheck {
				guidanceErr := fmt.Errorf(
					"INSUFFICIENT UNALLOCATED DISK SPACE\n\n"+
						"Host partition %s occupies 100%% of the partition table.\n"+
						"The Linux kernel strictly prohibits shrinking a mounted ext4 filesystem online to prevent data corruption.\n\n"+
						"=> HOW TO EXPAND YOUR DISK IN VIRTUALBOX (Step-by-Step):\n"+
						"   1. Shut down this VM completely.\n"+
						"   2. In VirtualBox Manager (Host OS), go to Tools -> Media (Ctrl+D).\n"+
						"   3. Under Hard disks, select your VM's .vdi file.\n"+
						"   4. Adjust Size by +30 GB to +40 GB and click Apply.\n"+
						"   5. Start the VM and re-run: the free space will be detected automatically.\n\n"+
						"=> PHYSICAL / BARE-METAL HARDWARE:\n"+
						"   Choose Option [2] 'Single-Boot Replace' from the Hub menu, or use a live USB to resize.",
					hostPart,
				)
				journal.Fail(step, guidanceErr)
				return fail("partition-shrink", guidanceErr)
			}

			plan, err := PlanShrink(hostPart, requiredBytes)
			if err != nil {
				journal.Fail(step, err)
				return fail("partition-shrink", err)
			}
			if err := ApplyShrink(plan); err != nil {
				journal.Fail(step, err)
				return fail("partition-shrink", err)
			}
		}

		journal.Commit(step)
		onUpdate(StepUpdate{StepName: "partition-shrink", Done: true})
	}

	if !req.SkipCompaction && req.HypervisorKind != hypervisor.KindBareMetal && !journal.Session.IsPaused {
		compactPlan, err := hypervisor.PlanHostCompact(req.HypervisorKind, req.TargetDiskPath)
		if err == nil {
			onUpdate(StepUpdate{StepName: "hypervisor-compact-planned: " + compactPlan.FormatPlanForDisplay(), Done: true})
		}
	}

	// Transactional Image Streaming with Failover & Ranges
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
	journal.Commit(dlStep)
	onUpdate(StepUpdate{StepName: "image-download", Done: true})

	// Deploy Payload
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
			onUpdate(StepUpdate{StepName: "configuring autonomous dual-boot & updating GRUB...", Done: false})
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
	_ = os.Remove(streamResult.DestPath)

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
	if _, err := io.Copy(h, f); err != nil {
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
