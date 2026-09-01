// Package hypervisor — compact.go implements the "Dynamic Virtual Disk
// Compactor" from the blueprint: it zero-fills/TRIMs free space from inside
// a guest so the host-side compaction tools can actually reclaim it, then
// generates (never silently executes) the host-side compaction command.
//
// IMPORTANT: host-side compaction (VBoxManage --compact / vmware-vdiskmanager -k)
// rewrites the entire virtual disk file. It must never run while the VM is
// running, and ideally only after a verified, journaled snapshot/backup.
// This file deliberately returns commands as data (Plan) rather than
// executing them directly from compact.go — the caller (engine, with the
// journal wired in) decides when it's safe to actually run them.
package hypervisor

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// ZeroFillResult reports what happened when we tried to zero free space
// inside the guest so the host compactor has something to reclaim.
type ZeroFillResult struct {
	MethodUsed string // e.g. "fstrim", "sdelete", "dd zero-file"
	Command    string // exact command run, for the scrolling status journal
	Output     string
	Warnings   []string
}

// ZeroFillGuestLinux runs fstrim on mountPoint if the filesystem supports
// discard, falling back to a zero'd temp file + delete otherwise (the
// classic portable trick for filesystems/hypervisors without TRIM passthrough).
func ZeroFillGuestLinux(mountPoint string) (*ZeroFillResult, error) {
	res := &ZeroFillResult{}

	if out, err := exec.Command("fstrim", "-v", mountPoint).CombinedOutput(); err == nil {
		res.MethodUsed = "fstrim"
		res.Command = fmt.Sprintf("fstrim -v %s", mountPoint)
		res.Output = strings.TrimSpace(string(out))
		return res, nil
	}

	res.Warnings = append(res.Warnings, "fstrim unavailable or filesystem doesn't support discard; falling back to zero-file method (slower, uses temp disk space)")
	zeroPath := strings.TrimRight(mountPoint, "/") + "/.boot-orchestrator-zerofill.tmp"
	ddCmd := fmt.Sprintf("dd if=/dev/zero of=%s bs=1M; rm -f %s", zeroPath, zeroPath)
	cmd := exec.Command("sh", "-c", ddCmd)
	out, err := cmd.CombinedOutput()
	res.MethodUsed = "zero-file"
	res.Command = ddCmd
	res.Output = strings.TrimSpace(string(out))
	// dd is *expected* to exit non-zero when it fills the disk (ENOSPC) —
	// that's success for this technique, not failure. Only report a real
	// error if the zero file was never created/removed.
	if err != nil && !strings.Contains(strings.ToLower(res.Output), "no space left") {
		return res, fmt.Errorf("zero-file fallback failed: %w (output: %s)", err, res.Output)
	}
	return res, nil
}

// ZeroFillGuestWindows uses sdelete (Sysinternals) if present on PATH,
// otherwise falls back to Optimize-Volume -ReTrim for SSDs/thin disks.
func ZeroFillGuestWindows(driveLetter string) (*ZeroFillResult, error) {
	res := &ZeroFillResult{}

	if _, err := exec.LookPath("sdelete64.exe"); err == nil {
		cmdStr := fmt.Sprintf("sdelete64.exe -z %s:", driveLetter)
		out, err := exec.Command("sdelete64.exe", "-z", driveLetter+":").CombinedOutput()
		res.MethodUsed = "sdelete"
		res.Command = cmdStr
		res.Output = strings.TrimSpace(string(out))
		if err != nil {
			return res, fmt.Errorf("sdelete zero-fill failed: %w", err)
		}
		return res, nil
	}

	res.Warnings = append(res.Warnings, "sdelete64.exe not found on PATH; falling back to Optimize-Volume -ReTrim (reclaims less space, but requires no extra download)")
	psCmd := fmt.Sprintf("Optimize-Volume -DriveLetter %s -ReTrim -Verbose", driveLetter)
	out, err := exec.Command("powershell", "-NoProfile", "-Command", psCmd).CombinedOutput()
	res.MethodUsed = "Optimize-Volume -ReTrim"
	res.Command = psCmd
	res.Output = strings.TrimSpace(string(out))
	if err != nil {
		return res, fmt.Errorf("Optimize-Volume -ReTrim failed: %w", err)
	}
	return res, nil
}

// HostCompactPlan is a generated, not-yet-executed plan to reclaim space on
// the host for one virtual disk. RollbackData should be the disk's size and
// a checksum captured before compaction, so the journal can at least verify
// integrity post-compaction (compaction of an already-corrupt image cannot
// be "undone" — the real safety net is requiring a snapshot first).
type HostCompactPlan struct {
	Hypervisor      Kind
	DiskPath        string
	Command         string
	Args            []string
	RequiresVMOff   bool
	RequiresBackup  bool
	Notes           []string
}

// PlanHostCompact generates (but does not run) the correct host-side
// compaction command for the given hypervisor and disk image.
func PlanHostCompact(kind Kind, diskPath string) (*HostCompactPlan, error) {
	plan := &HostCompactPlan{
		DiskPath:       diskPath,
		Hypervisor:     kind,
		RequiresVMOff:  true,
		RequiresBackup: true,
	}

	switch kind {
	case KindVirtualBox:
		if !strings.HasSuffix(strings.ToLower(diskPath), ".vdi") {
			plan.Notes = append(plan.Notes, "VBoxManage --compact only works on .vdi in dynamically-allocated format; .vmdk/.vhd attached to VirtualBox cannot be compacted this way")
		}
		plan.Command = "VBoxManage"
		plan.Args = []string{"modifymedium", "--compact", diskPath}
		plan.Notes = append(plan.Notes,
			"the VM must be fully powered off (not just saved-state) before running this",
			"only reclaims space already zero-filled from inside the guest — run ZeroFillGuestLinux/Windows first",
		)
	case KindVMware:
		binName := "vmware-vdiskmanager"
		if runtime.GOOS == "windows" {
			binName = "vmware-vdiskmanager.exe"
		}
		plan.Command = binName
		plan.Args = []string{"-k", diskPath}
		plan.Notes = append(plan.Notes,
			"vmware-vdiskmanager -k performs an in-place shrink; ensure a backup/snapshot exists first",
			"requires VMware Workstation/Fusion tools to be installed and on PATH",
		)
	case KindKVM, KindQEMU:
		plan.Command = "qemu-img"
		plan.Args = []string{"convert", "-O", "qcow2", diskPath, diskPath + ".compacted"}
		plan.Notes = append(plan.Notes,
			"qemu-img cannot compact in-place — this writes a new file that must be verified and swapped in afterward",
			"verify the new file's checksum/boot before deleting the original",
		)
	default:
		return nil, fmt.Errorf("no host-side compaction method known for hypervisor kind %q (or running on bare metal, where compaction doesn't apply)", kind.String())
	}

	return plan, nil
}

// FormatPlanForDisplay renders a HostCompactPlan as the exact shell command
// the TUI's scrolling status journal should show the user before execution.
func (p *HostCompactPlan) FormatPlanForDisplay() string {
	return fmt.Sprintf("%s %s", p.Command, strings.Join(p.Args, " "))
}
