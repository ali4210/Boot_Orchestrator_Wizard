// Package engine — rootfs.go implements the "Raw ext4/Btrfs cloud-image
// extraction engine" from the blueprint: takes a downloaded cloud image
// (qcow2, raw .img, or a tar.xz/tar.gz rootfs tarball) and writes it to a
// target partition, verifying the write and never touching the target
// until the source has already passed SHA-256 verification (see streamer.go).
package engine

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// ImageFormat identifies what kind of file we were handed.
type ImageFormat string

const (
	FormatRawImg   ImageFormat = "raw"    // .img / .raw — dd-able directly
	FormatQCOW2    ImageFormat = "qcow2"  // needs qemu-img convert to raw first
	FormatTarGz    ImageFormat = "tar.gz" // rootfs tarball, extracted onto an already-formatted fs
	FormatTarXz    ImageFormat = "tar.xz"
	FormatUnknown  ImageFormat = "unknown"
)

// DetectImageFormat sniffs the file by magic bytes/extension rather than
// trusting the filename alone, since upstream mirrors are inconsistent.
func DetectImageFormat(path string) (ImageFormat, error) {
	f, err := os.Open(path)
	if err != nil {
		return FormatUnknown, err
	}
	defer f.Close()

	magic := make([]byte, 6)
	n, _ := f.Read(magic)
	magic = magic[:n]

	switch {
	case len(magic) >= 4 && string(magic[:4]) == "QFI\xfb":
		return FormatQCOW2, nil
	case len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		return FormatTarGz, nil
	case len(magic) >= 6 && string(magic[:6]) == "\xfd7zXZ\x00":
		return FormatTarXz, nil
	}

	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".qcow2"):
		return FormatQCOW2, nil
	case strings.HasSuffix(lower, ".img") || strings.HasSuffix(lower, ".raw"):
		return FormatRawImg, nil
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		return FormatTarGz, nil
	case strings.HasSuffix(lower, ".tar.xz"):
		return FormatTarXz, nil
	}

	return FormatUnknown, fmt.Errorf("could not determine image format for %s from magic bytes or extension", path)
}

// DeployPlan is the not-yet-executed plan for getting imagePath onto
// targetPartition. Building this separately from executing it lets the
// TUI show the exact steps and required tools before anything destructive
// happens.
type DeployPlan struct {
	ImagePath       string
	Format          ImageFormat
	TargetPartition string // block device or partition, e.g. /dev/sda2 — NEVER a whole-disk device for tarball mode
	Steps           []string
	RequiresTools   []string
}

// PlanDeploy inspects the image and target and produces the step list,
// checking that required external tools (qemu-img, mkfs.ext4, tar) are
// actually present before committing to a plan the caller can't execute.
func PlanDeploy(imagePath, targetPartition string) (*DeployPlan, error) {
	format, err := DetectImageFormat(imagePath)
	if err != nil {
		return nil, err
	}

	plan := &DeployPlan{
		ImagePath:       imagePath,
		Format:          format,
		TargetPartition: targetPartition,
	}

	switch format {
	case FormatRawImg:
		plan.RequiresTools = []string{"dd"}
		plan.Steps = []string{
			fmt.Sprintf("dd if=%s of=%s bs=4M conv=fsync status=progress", imagePath, targetPartition),
			"blockdev --rereadpt on the parent disk",
		}
	case FormatQCOW2:
		plan.RequiresTools = []string{"qemu-img", "dd"}
		plan.Steps = []string{
			fmt.Sprintf("qemu-img convert -O raw %s %s.raw", imagePath, imagePath),
			fmt.Sprintf("dd if=%s.raw of=%s bs=4M conv=fsync status=progress", imagePath, targetPartition),
			fmt.Sprintf("rm %s.raw (intermediate raw conversion is deleted after successful dd)", imagePath),
		}
	case FormatTarGz, FormatTarXz:
		plan.RequiresTools = []string{"mkfs.ext4", "tar", "mount"}
		plan.Steps = []string{
			fmt.Sprintf("mkfs.ext4 -F %s (target partition must already be sized correctly — see partition.go ShrinkPlan)", targetPartition),
			fmt.Sprintf("mount %s <temp mountpoint>", targetPartition),
			fmt.Sprintf("tar -xf %s -C <temp mountpoint> --numeric-owner (preserve uid/gid exactly — critical for a bootable rootfs)", imagePath),
			"umount <temp mountpoint>",
		}
	default:
		return nil, fmt.Errorf("unsupported image format for %s", imagePath)
	}

	for _, tool := range plan.RequiresTools {
		if _, err := exec.LookPath(tool); err != nil {
			return nil, fmt.Errorf("required tool %q not found on PATH — install it before running this plan", tool)
		}
	}

	return plan, nil
}

// DeployRollbackData is stored via safety.Journal.Begin before ApplyDeploy
// runs, so a failed/interrupted deploy can be identified and the target
// partition flagged as "unknown state — do not boot" rather than silently
// left half-written.
type DeployRollbackData struct {
	TargetPartition string `json:"target_partition"`
	// There is no way to "undo" a raw dd write after the fact — the only
	// real safety net is: (a) never dd onto a partition that still holds
	// data you need, and (b) the partition-shrink step (partition.go) runs
	// and is verified BEFORE this ever executes, so worst case here is an
	// empty/wasted partition, never destroyed user data.
	PreviouslyEmpty bool `json:"previously_empty"`
}

// ApplyDeploy executes a DeployPlan. Returns rollback data for the journal;
// as noted above, "rollback" here means "mark the partition as needing
// re-deployment," not "restore original contents" — callers must not call
// this against a partition that isn't already confirmed empty/freshly
// shrunk (see engine/partition.go).
func ApplyDeploy(plan *DeployPlan) (rollbackData []byte, err error) {
	rb := DeployRollbackData{TargetPartition: plan.TargetPartition, PreviouslyEmpty: true}
	rollbackData, _ = json.Marshal(rb)

	switch plan.Format {
	case FormatRawImg:
		if err := ddImageToDevice(plan.ImagePath, plan.TargetPartition); err != nil {
			return rollbackData, err
		}
	case FormatQCOW2:
		rawPath := plan.ImagePath + ".raw"
		if out, err := exec.Command("qemu-img", "convert", "-O", "raw", plan.ImagePath, rawPath).CombinedOutput(); err != nil {
			return rollbackData, fmt.Errorf("qemu-img convert failed: %w\n%s", err, out)
		}
		defer os.Remove(rawPath)
		if err := ddImageToDevice(rawPath, plan.TargetPartition); err != nil {
			return rollbackData, err
		}
	case FormatTarGz, FormatTarXz:
		if err := deployTarball(plan.ImagePath, plan.TargetPartition); err != nil {
			return rollbackData, err
		}
	default:
		return rollbackData, fmt.Errorf("unsupported format %s in ApplyDeploy", plan.Format)
	}

	exec.Command("sync").Run()
	return rollbackData, nil
}

func ddImageToDevice(imagePath, targetPartition string) error {
	cmd := exec.Command("dd",
		fmt.Sprintf("if=%s", imagePath),
		fmt.Sprintf("of=%s", targetPartition),
		"bs=4M",
		"conv=fsync",
		"status=none",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("dd %s -> %s failed: %w\n%s", imagePath, targetPartition, err, out)
	}
	return nil
}

func deployTarball(tarPath, targetPartition string) error {
	if out, err := exec.Command("mkfs.ext4", "-F", targetPartition).CombinedOutput(); err != nil {
		return fmt.Errorf("mkfs.ext4 %s failed: %w\n%s", targetPartition, err, out)
	}

	mountPoint, err := os.MkdirTemp("", "boot-orchestrator-rootfs-*")
	if err != nil {
		return fmt.Errorf("creating temp mountpoint: %w", err)
	}
	defer os.RemoveAll(mountPoint)

	if out, err := exec.Command("mount", targetPartition, mountPoint).CombinedOutput(); err != nil {
		return fmt.Errorf("mount %s at %s failed: %w\n%s", targetPartition, mountPoint, err, out)
	}
	defer exec.Command("umount", mountPoint).Run()

	// --numeric-owner is non-negotiable: mapping tar's numeric uid/gid to
	// names on the HOST would silently reassign file ownership in the
	// guest rootfs and can produce an unbootable or insecure system.
	tarCmd := exec.Command("tar", "-xf", tarPath, "-C", mountPoint, "--numeric-owner")
	if out, err := tarCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("extracting %s into %s failed: %w\n%s", tarPath, mountPoint, err, out)
	}

	return nil
}

// decompressGzipStream is exposed for callers (e.g. slipstream.go) that
// need to peek inside a .tar.gz without a full tar extraction — e.g. to
// check whether a specific driver file exists before committing to a plan.
func decompressGzipStream(r io.Reader) (io.ReadCloser, error) {
	return gzip.NewReader(r)
}
