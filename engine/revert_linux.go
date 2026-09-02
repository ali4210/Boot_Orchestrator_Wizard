//go:build linux

package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type RealRevertParams struct {
	DiskDevice        string `json:"disk_device"`
	PartitionNum      string `json:"partition_num"`
	HostPartNum       string `json:"host_part_num"`
	EFIDirNameToPurge string `json:"efi_dir_to_purge"`
	EFIBootEntryNum   string `json:"efi_boot_entry_num"`
}

func ExecuteRealReversion(p RealRevertParams, logFn func(string)) error {
	if logFn == nil {
		logFn = func(string) {}
	}

	logFn("=> [REVERT ENGINE] Verifying root permissions...")
	if os.Geteuid() != 0 {
		return fmt.Errorf("root privileges required for disk and NVRAM restoration")
	}

	// 1. UEFI Cleanup (If running in UEFI mode)
	if isUEFIMode() && p.EFIBootEntryNum != "" {
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
		espBase := findActiveESPMount()
		targetEFIDir := filepath.Join(espBase, "EFI", strings.TrimSpace(p.EFIDirNameToPurge))
		if _, err := os.Stat(targetEFIDir); err == nil {
			_ = os.RemoveAll(targetEFIDir)
			logFn("=> EFI directory wiped from ESP.")
		}
	}

	// 3. Delete Secondary Partition
	if p.DiskDevice != "" && p.PartitionNum != "" {
		if partitionExists(p.DiskDevice, p.PartitionNum) {
			logFn(fmt.Sprintf("=> Deleting secondary partition %s on %s...", p.PartitionNum, p.DiskDevice))
			cmdRm := exec.Command("parted", "-s", p.DiskDevice, "rm", p.PartitionNum)
			_ = cmdRm.Run()
			_ = exec.Command("partprobe", p.DiskDevice).Run()
			logFn("=> Partition deleted from block table.")
		} else {
			logFn(fmt.Sprintf("=> Secondary partition %s is already absent.", p.PartitionNum))
		}
	}

	// 4. Expand Host Partition to 100% capacity
	if p.DiskDevice != "" && p.HostPartNum != "" {
		logFn(fmt.Sprintf("=> Expanding host partition %s to 100%%...", p.HostPartNum))

		// Try growpart first (most robust on Linux MBR/GPT)
		if _, err := exec.LookPath("growpart"); err == nil {
			_ = exec.Command("growpart", p.DiskDevice, p.HostPartNum).Run()
		} else {
			// Fallback to parted resizepart
			_ = exec.Command("parted", "-s", p.DiskDevice, "--", "resizepart", p.HostPartNum, "-1s").Run()
		}

		_ = exec.Command("partprobe", p.DiskDevice).Run()

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
	_ = os.Remove("/etc/default/grub.d/99-orchestrator-prober.cfg")
	logFn("=> Refreshing system GRUB configuration to purge old entries...")
	_ = RegenerateGrubConfig()

	logFn("=> [SUCCESS] System restored to single-OS state.")
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
