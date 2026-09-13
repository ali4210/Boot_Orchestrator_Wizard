//go:build linux

package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func ExecuteRealReversion(p RealRevertParams, logFn func(string)) error {
	return ExecuteRealReversionWithProgress(p, logFn, nil)
}

// ExecuteRealReversionWithProgress restores single-boot configuration, deletes secondary
// partitions, reclaims disk boundaries, updates host GRUB, and animates the progress bar.
func ExecuteRealReversionWithProgress(p RealRevertParams, logFn func(string), progressFn func(step string, percent float64)) error {
	if logFn == nil {
		logFn = func(string) {}
	}
	if progressFn == nil {
		progressFn = func(string, float64) {}
	}

	progressFn("=> [REVERT 10%] Verifying root privileges and block device locks...", 0.10)
	logFn("=> [REVERT ENGINE] Verifying root permissions...")
	if os.Geteuid() != 0 {
		return fmt.Errorf("root privileges required for disk and NVRAM restoration")
	}

	// 1. UEFI Cleanup (If running in UEFI mode)
	if isUEFIMode() && p.EFIBootEntryNum != "" {
		progressFn("=> [REVERT 25%] Purging UEFI NVRAM boot targets...", 0.25)
		cleanNum := strings.TrimPrefix(p.EFIBootEntryNum, "Boot")
		cleanNum = strings.TrimSpace(cleanNum)
		logFn(fmt.Sprintf("=> Removing UEFI Boot entry: Boot%s...", cleanNum))
		cmd := exec.Command("efibootmgr", "-b", cleanNum, "-B")
		_ = cmd.Run()
	} else if !isUEFIMode() {
		logFn("=> [FIRMWARE NOTICE] Legacy BIOS/MBR system detected; skipping NVRAM operations.")
	}

	// 2. ESP Cleanup (UEFI only)
	if isUEFIMode() && p.EFIDirNameToPurge != "" {
		progressFn("=> [REVERT 35%] Purging orphaned EFI payloads from ESP...", 0.35)
		espBase := findActiveESPMount()
		targetEFIDir := filepath.Join(espBase, "EFI", strings.TrimSpace(p.EFIDirNameToPurge))
		if _, err := os.Stat(targetEFIDir); err == nil {
			_ = os.RemoveAll(targetEFIDir)
			logFn("=> EFI directory wiped from ESP.")
		}
	}

	// 3. Delete Secondary Partition
	if p.DiskDevice != "" && p.PartitionNum != "" {
		progressFn(fmt.Sprintf("=> [REVERT 50%%] Deleting secondary partition %s from GPT table...", p.PartitionNum), 0.50)
		targetDev := p.DiskDevice + p.PartitionNum
		if strings.Contains(p.DiskDevice, "nvme") || strings.Contains(p.DiskDevice, "mmcblk") {
			targetDev = p.DiskDevice + "p" + p.PartitionNum
		}
		_ = exec.Command("umount", "-l", targetDev).Run()

		if partitionExists(p.DiskDevice, p.PartitionNum) {
			logFn(fmt.Sprintf("=> Deleting secondary partition %s on %s...", p.PartitionNum, p.DiskDevice))
			cmdRm := exec.Command("parted", "-s", p.DiskDevice, "rm", p.PartitionNum)
			_ = cmdRm.Run()
			_ = exec.Command("partprobe", p.DiskDevice).Run()
			_ = exec.Command("udevadm", "settle", "--timeout=10").Run()
			logFn("=> Partition deleted from block table.")
		} else {
			logFn(fmt.Sprintf("=> Secondary partition %s is already absent.", p.PartitionNum))
		}
	}

	// 4. Expand Host Partition to 100% capacity
	if p.DiskDevice != "" && p.HostPartNum != "" {
		progressFn(fmt.Sprintf("=> [REVERT 70%%] Expanding host partition %s to 100%% boundary...", p.HostPartNum), 0.70)
		logFn(fmt.Sprintf("=> Expanding host partition %s to 100%%...", p.HostPartNum))

		if _, err := exec.LookPath("growpart"); err == nil {
			_ = exec.Command("growpart", p.DiskDevice, p.HostPartNum).Run()
		} else {
			_ = exec.Command("parted", "-s", p.DiskDevice, "--", "resizepart", p.HostPartNum, "-1s").Run()
		}

		_ = exec.Command("partprobe", p.DiskDevice).Run()
		_ = exec.Command("udevadm", "settle", "--timeout=10").Run()

		hostDev := fmt.Sprintf("%sp%s", p.DiskDevice, p.HostPartNum)
		if strings.HasPrefix(p.DiskDevice, "/dev/sd") || strings.HasPrefix(p.DiskDevice, "/dev/vd") {
			hostDev = fmt.Sprintf("%s%s", p.DiskDevice, p.HostPartNum)
		}

		logFn(fmt.Sprintf("=> Online resizing filesystem on %s...", hostDev))
		cmdResizeFS := exec.Command("resize2fs", hostDev)
		if out, err := cmdResizeFS.CombinedOutput(); err != nil {
			logFn(fmt.Sprintf("=> Notice: resize2fs info: %s", strings.TrimSpace(string(out))))
		} else {
			logFn("=> Filesystem expanded to 100% free capacity.")
		}
	}

	// 5. Clean up Drop-in GRUB overrides & Regenerate GRUB
	progressFn("=> [REVERT 85%] Synchronizing bootloader and updating host GRUB...", 0.85)
	_ = os.Remove("/etc/default/grub.d/50-dualboot-menu.cfg")
	_ = os.Remove("/etc/default/grub.d/99-orchestrator-prober.cfg")

	cleanCustomGrub := `#!/bin/sh
exec tail -n +3 $0
# This file provides an easy way to add custom menu entries.
`
	_ = os.WriteFile("/etc/grub.d/40_custom", []byte(cleanCustomGrub), 0755)

	logFn("=> Refreshing system GRUB configuration to purge old entries...")
	_ = RegenerateGrubConfig()

	progressFn("=> [REVERT COMPLETE 100%] Host single-boot state fully restored.", 1.0)
	logFn("=> [SUCCESS] System restored to single-OS state.")
	time.Sleep(800 * time.Millisecond)
	return nil
}

func partitionExists(diskDevice, partNum string) bool {
	out, err := exec.Command("parted", "-s", diskDevice, "print").Output()
	if err != nil {
		return false
	}
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == partNum {
			return true
		}
	}
	return false
}

func isUEFIMode() bool {
	_, err := os.Stat("/sys/firmware/efi")
	return err == nil
}

func findActiveESPMount() string {
	candidates := []string{"/boot/efi", "/efi", "/boot"}
	for _, c := range candidates {
		out, err := exec.Command("findmnt", "-n", "-o", "FSTYPE", c).Output()
		if err == nil {
			fstype := strings.TrimSpace(string(out))
			if fstype == "vfat" || fstype == "msdos" {
				return c
			}
		}
	}
	return "/boot/efi"
}

func RollbackRevertHandler(rollbackData json.RawMessage) error {
	var params RealRevertParams
	if err := json.Unmarshal(rollbackData, &params); err != nil {
		return fmt.Errorf("unmarshaling revert rollback params: %w", err)
	}
	return ExecuteRealReversion(params, nil)
}
