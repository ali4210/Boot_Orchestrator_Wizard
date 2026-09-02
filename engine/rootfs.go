// Package engine — rootfs.go implements the Raw ext4/Btrfs, ISO, and cloud-image
// extraction engine: supports raw images, ISO9660 live installers, QCOW2, and tar rootfs tarballs.
package engine

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ImageFormat identifies what kind of file we were handed.
type ImageFormat string

const (
	FormatRawImg  ImageFormat = "raw"    // .img / .raw — dd-able directly
	FormatISO     ImageFormat = "iso"    // .iso — bootable hybrid ISO / squashfs
	FormatQCOW2   ImageFormat = "qcow2"  // needs qemu-img convert to raw first
	FormatTarGz   ImageFormat = "tar.gz" // rootfs tarball
	FormatTarXz   ImageFormat = "tar.xz"
	FormatUnknown ImageFormat = "unknown"
)

// DetectImageFormat sniffs the file by magic bytes and fallbacks to extension.
func DetectImageFormat(path string) (ImageFormat, error) {
	f, err := os.Open(path)
	if err != nil {
		return FormatUnknown, fmt.Errorf("opening image for inspection: %w", err)
	}
	defer f.Close()

	// 1. Read first 37KB to inspect both header magic and ISO9660 PVD
	buf := make([]byte, 36864)
	n, _ := f.Read(buf)
	magic := buf[:n]

	// ISO9660 Standard: Primary Volume Descriptor identifier "CD001" at sector 16 (byte offset 32768)
	if len(magic) >= 32773 && string(magic[32769:32774]) == "CD001" {
		return FormatISO, nil
	}

	switch {
	case len(magic) >= 4 && string(magic[:4]) == "QFI\xfb":
		return FormatQCOW2, nil
	case len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		return FormatTarGz, nil
	case len(magic) >= 6 && string(magic[:6]) == "\xfd7zXZ\x00":
		return FormatTarXz, nil
	}

	// 2. Extension Fallback
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".iso"):
		return FormatISO, nil
	case strings.HasSuffix(lower, ".qcow2"):
		return FormatQCOW2, nil
	case strings.HasSuffix(lower, ".img") || strings.HasSuffix(lower, ".raw"):
		return FormatRawImg, nil
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		return FormatTarGz, nil
	case strings.HasSuffix(lower, ".tar.xz"):
		return FormatTarXz, nil
	}

	return FormatUnknown, fmt.Errorf("unsupported image format for %s (magic: %q)", path, magic[:min(len(magic), 8)])
}

type DeployPlan struct {
	ImagePath       string
	Format          ImageFormat
	TargetPartition string
	Steps           []string
	RequiresTools   []string
}

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
	case FormatISO, FormatRawImg:
		plan.RequiresTools = []string{"dd", "sync"}
		plan.Steps = []string{
			fmt.Sprintf("Direct raw stream write to %s via dd (block size 4M, fsync enabled)", targetPartition),
			fmt.Sprintf("Synchronizing block buffers to disk"),
		}
	case FormatQCOW2:
		plan.RequiresTools = []string{"qemu-img", "dd", "sync"}
		plan.Steps = []string{
			fmt.Sprintf("qemu-img convert -O raw %s %s.raw", imagePath, imagePath),
			fmt.Sprintf("dd raw payload directly into target %s", targetPartition),
			"purge temporary conversion image",
		}
	case FormatTarGz, FormatTarXz:
		plan.RequiresTools = []string{"mkfs.ext4", "tar", "mount", "umount"}
		plan.Steps = []string{
			fmt.Sprintf("mkfs.ext4 -F %s", targetPartition),
			"mount target partition to isolated directory",
			fmt.Sprintf("tar -xf %s --numeric-owner into root partition", imagePath),
			"normalize root filesystem layout if wrapped in subdirectory",
			"umount and flush filesystem buffers",
		}
	default:
		return nil, fmt.Errorf("unsupported image format %s for deployment", format)
	}

	for _, tool := range plan.RequiresTools {
		if _, err := exec.LookPath(tool); err != nil {
			return nil, fmt.Errorf("required system tool %q not found on PATH", tool)
		}
	}

	return plan, nil
}

type DeployRollbackData struct {
	TargetPartition string `json:"target_partition"`
	PreviouslyEmpty bool   `json:"previously_empty"`
}

func ApplyDeploy(plan *DeployPlan) (rollbackData []byte, err error) {
	rb := DeployRollbackData{TargetPartition: plan.TargetPartition, PreviouslyEmpty: true}
	rollbackData, _ = json.Marshal(rb)

	switch plan.Format {
	case FormatISO, FormatRawImg:
		if err := ddImageToDevice(plan.ImagePath, plan.TargetPartition); err != nil {
			return rollbackData, err
		}
	case FormatQCOW2:
		rawPath := plan.ImagePath + ".raw"
		if out, err := exec.Command("qemu-img", "convert", "-O", "raw", plan.ImagePath, rawPath).CombinedOutput(); err != nil {
			return rollbackData, fmt.Errorf("qemu-img convert failed: %w\n%s", err, string(out))
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

	_ = exec.Command("sync").Run()
	_ = exec.Command("partprobe", plan.TargetPartition).Run()
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
		return fmt.Errorf("dd extraction (%s -> %s) failed: %w\n%s", imagePath, targetPartition, err, string(out))
	}
	return nil
}

func deployTarball(tarPath, targetPartition string) error {
	if out, err := exec.Command("mkfs.ext4", "-F", targetPartition).CombinedOutput(); err != nil {
		return fmt.Errorf("formatting target %s as ext4 failed: %w\n%s", targetPartition, err, string(out))
	}

	mountPoint, err := os.MkdirTemp("", "orchestrator-rootfs-*")
	if err != nil {
		return fmt.Errorf("creating mountpoint: %w", err)
	}
	defer os.RemoveAll(mountPoint)

	if out, err := exec.Command("mount", targetPartition, mountPoint).CombinedOutput(); err != nil {
		return fmt.Errorf("mounting target partition %s failed: %w\n%s", targetPartition, err, string(out))
	}

	// Always ensure clean unmount even on panics/failures
	defer func() {
		_ = exec.Command("sync").Run()
		if out, err := exec.Command("umount", mountPoint).CombinedOutput(); err != nil {
			_ = exec.Command("umount", "-l", mountPoint).Run()
			_ = out
		}
	}()

	tarCmd := exec.Command("tar", "-xf", tarPath, "-C", mountPoint, "--numeric-owner")
	if out, err := tarCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("extracting rootfs tarball failed: %w\n%s", err, string(out))
	}

	// Dynamic Self-Healing: Normalize wrapped root directories if nested
	if err := normalizeRootDirectoryStructure(mountPoint); err != nil {
		return fmt.Errorf("normalizing rootfs structure: %w", err)
	}

	return nil
}

// normalizeRootDirectoryStructure verifies whether the extracted archive placed system
// trees (bin, usr, etc, boot) directly at root. If wrapped in an outer container directory,
// it elevates all items directly to mountPoint.
func normalizeRootDirectoryStructure(mountPoint string) error {
	entries, err := os.ReadDir(mountPoint)
	if err != nil {
		return err
	}

	hasStandardRoot := false
	var candidateSubdir string
	validItemsCount := 0

	for _, entry := range entries {
		name := entry.Name()
		if name == "lost+found" {
			continue
		}
		validItemsCount++
		if name == "bin" || name == "usr" || name == "etc" || name == "boot" {
			hasStandardRoot = true
			break
		}
		if entry.IsDir() {
			candidateSubdir = filepath.Join(mountPoint, name)
		}
	}

	// If root directories are not at top level and only one wrapper directory exists, flatten it
	if !hasStandardRoot && validItemsCount == 1 && candidateSubdir != "" {
		subEntries, subErr := os.ReadDir(candidateSubdir)
		if subErr != nil {
			return subErr
		}

		for _, sub := range subEntries {
			src := filepath.Join(candidateSubdir, sub.Name())
			dst := filepath.Join(mountPoint, sub.Name())
			if err := os.Rename(src, dst); err != nil {
				_ = exec.Command("mv", src, dst).Run()
			}
		}
		_ = os.Remove(candidateSubdir)
	}

	return nil
}

func decompressGzipStream(r io.Reader) (io.ReadCloser, error) {
	return gzip.NewReader(r)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
