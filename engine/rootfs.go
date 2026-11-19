// Package engine — rootfs.go implements the Universal Multi-OS Engine:
// Supports Native Linux (SquashFS / ext4 / btrfs), Windows (WIM / ESD / NTFS),
// macOS (DMG / APFS / Raw Images), QCOW2, and Tarball Rootfs archives.
package engine

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// ImageFormat identifies what kind of operating system payload was supplied.
type ImageFormat string

const (
	FormatLinuxISO ImageFormat = "linux-iso" // Linux live/installer ISO (SquashFS)
	FormatWinISO   ImageFormat = "win-iso"   // Windows 10/11/Server ISO (WIM/ESD)
	FormatMacOSImg ImageFormat = "macos-img" // macOS BaseSystem.dmg or APFS raw image
	FormatRawImg   ImageFormat = "raw"       // Generic dd-able raw block disk image
	FormatQCOW2    ImageFormat = "qcow2"     // QEMU Copy-On-Write format
	FormatTarGz    ImageFormat = "tar.gz"    // Rootfs tarball archive
	FormatTarXz    ImageFormat = "tar.xz"
	FormatUnknown  ImageFormat = "unknown"
)

// AssertHostTooling verifies whether critical extraction tools exist on the host.
// Autonomously installs missing packages for Linux, Windows (wimtools, ntfs-3g), and macOS.
func AssertHostTooling(logFn func(string)) {
	if logFn == nil {
		logFn = func(string) {}
	}

	missing := []string{}
	checkTools := map[string]string{
		"unsquashfs": "squashfs-tools",
		"mkfs.ntfs":  "ntfs-3g",
		"wimapply":   "wimtools",
		"parted":     "parted",
	}

	for cmd, pkg := range checkTools {
		if _, err := exec.LookPath(cmd); err != nil {
			missing = append(missing, pkg)
		}
	}

	if len(missing) == 0 {
		return
	}

	logFn(fmt.Sprintf("=> [HOST SELF-HEAL] Provisioning missing extraction tooling: %v...", missing))

	var cmd *exec.Cmd
	switch {
	case fileExists("/usr/bin/apt-get"):
		args := append([]string{"install", "-y", "--no-install-recommends"}, missing...)
		cmd = exec.Command("apt-get", args...)
		cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	case fileExists("/usr/bin/pacman"):
		args := append([]string{"-Sy", "--noconfirm"}, missing...)
		cmd = exec.Command("pacman", args...)
	case fileExists("/usr/bin/dnf"):
		args := append([]string{"install", "-y"}, missing...)
		cmd = exec.Command("dnf", args...)
	case fileExists("/usr/bin/zypper"):
		args := append([]string{"--non-interactive", "install"}, missing...)
		cmd = exec.Command("zypper", args...)
	}

	if cmd != nil {
		if out, err := cmd.CombinedOutput(); err == nil {
			logFn("=> [HOST SELF-HEAL] Prerequisites deployed successfully.")
		} else {
			logFn(fmt.Sprintf("=> [HOST SELF-HEAL] Warning during package installation: %v (%s)", err, string(out)))
		}
	}
}

// DetectImageFormat analyzes magic bytes, ISO filesystem structures, and file extensions.
func DetectImageFormat(path string) (ImageFormat, error) {
	lower := strings.ToLower(path)

	// Quick extension check for DMG
	if strings.HasSuffix(lower, ".dmg") {
		return FormatMacOSImg, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return FormatUnknown, fmt.Errorf("opening image for inspection: %w", err)
	}
	defer f.Close()

	buf := make([]byte, 36864)
	n, _ := f.Read(buf)
	magic := buf[:n]

	// ISO9660 Standard Check: Primary Volume Descriptor "CD001" at sector 16
	if len(magic) >= 32773 && string(magic[32769:32774]) == "CD001" {
		if isWindowsISO(path) {
			return FormatWinISO, nil
		}
		return FormatLinuxISO, nil
	}

	switch {
	case len(magic) >= 4 && string(magic[:4]) == "QFI\xfb":
		return FormatQCOW2, nil
	case len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		return FormatTarGz, nil
	case len(magic) >= 6 && string(magic[:6]) == "\xfd7zXZ\x00":
		return FormatTarXz, nil
	case len(magic) >= 4 && string(magic[:4]) == "koly": // Apple DMG footer signature
		return FormatMacOSImg, nil
	}

	switch {
	case strings.HasSuffix(lower, ".iso"):
		if isWindowsISO(path) {
			return FormatWinISO, nil
		}
		return FormatLinuxISO, nil
	case strings.HasSuffix(lower, ".qcow2"):
		return FormatQCOW2, nil
	case strings.HasSuffix(lower, ".img") || strings.HasSuffix(lower, ".raw"):
		return FormatRawImg, nil
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		return FormatTarGz, nil
	case strings.HasSuffix(lower, ".tar.xz"):
		return FormatTarXz, nil
	}

	return FormatUnknown, fmt.Errorf("unsupported image format for %s", path)
}

// isWindowsISO checks for the presence of sources/install.wim or sources/install.esd.
func isWindowsISO(isoPath string) bool {
	tmpMount, err := os.MkdirTemp("", "orch-probe-*")
	if err != nil {
		return false
	}
	defer os.RemoveAll(tmpMount)

	if out, err := exec.Command("mount", "-o", "loop,ro", isoPath, tmpMount).CombinedOutput(); err != nil {
		_ = out
		return false
	}
	defer exec.Command("umount", "-l", tmpMount).Run()

	wimPath := filepath.Join(tmpMount, "sources", "install.wim")
	esdPath := filepath.Join(tmpMount, "sources", "install.esd")

	_, errWim := os.Stat(wimPath)
	_, errEsd := os.Stat(esdPath)
	return errWim == nil || errEsd == nil
}

type DeployPlan struct {
	ImagePath       string
	Format          ImageFormat
	TargetPartition string
	Steps           []string
	RequiresTools   []string
	Credentials     Credentials
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

	AssertHostTooling(nil)

	switch format {
	case FormatLinuxISO:
		plan.RequiresTools = []string{"mkfs.ext4", "mount", "umount"}
		plan.Steps = []string{
			fmt.Sprintf("Format %s with persistent ext4 filesystem", targetPartition),
			"Mount Linux ISO and unpack compressed squashfs into root",
			"Neutralize live-boot volatile overlays and compile native initramfs",
			"Purge demo installer shortcuts and setup persistent accounts",
		}
	case FormatWinISO:
		plan.RequiresTools = []string{"mkfs.ntfs", "wimapply", "mount", "umount"}
		plan.Steps = []string{
			fmt.Sprintf("Format %s with native NTFS filesystem (label: WINDOWS)", targetPartition),
			"Mount Windows ISO payload and locate install.wim / install.esd",
			fmt.Sprintf("Extract Windows image directly to %s via wimapply", targetPartition),
			"Inject autonomous unattended answer file (bypass OOBE, configure Admin)",
			"Prepare Windows Boot Manager EFI structures",
		}
	case FormatMacOSImg, FormatRawImg:
		plan.RequiresTools = []string{"dd", "sync"}
		plan.Steps = []string{
			fmt.Sprintf("Sector-aligned block stream write to %s via dd", targetPartition),
			"Flush physical block buffers to NVMe/SATA controller",
			"Synchronize EFI chainloader structures",
		}
	case FormatQCOW2:
		plan.RequiresTools = []string{"qemu-img", "dd", "sync"}
		plan.Steps = []string{
			fmt.Sprintf("qemu-img convert -O raw %s %s.raw", imagePath, imagePath),
			fmt.Sprintf("dd raw payload directly into %s", targetPartition),
			"Reclaim temporary raw disk conversion space",
		}
	case FormatTarGz, FormatTarXz:
		plan.RequiresTools = []string{"mkfs.ext4", "tar", "mount", "umount"}
		plan.Steps = []string{
			fmt.Sprintf("mkfs.ext4 -F %s", targetPartition),
			fmt.Sprintf("Extract rootfs archive directly into %s", targetPartition),
			"Normalize root filesystem hierarchy and commit sync buffers",
		}
	default:
		return nil, fmt.Errorf("unsupported image format %s for deployment", format)
	}

	for _, tool := range plan.RequiresTools {
		if _, err := exec.LookPath(tool); err != nil {
			return nil, fmt.Errorf("required system tool %q not found on host PATH", tool)
		}
	}

	return plan, nil
}

type DeployRollbackData struct {
	TargetPartition string `json:"target_partition"`
	PreviouslyEmpty bool   `json:"previously_empty"`
}

// ApplyDeploy executes the deployment plan with real-time step streaming.
func ApplyDeploy(plan *DeployPlan, stepFn ...func(StepUpdate)) (rollbackData []byte, err error) {
	var notify func(StepUpdate)
	if len(stepFn) > 0 && stepFn[0] != nil {
		notify = stepFn[0]
	} else {
		notify = func(StepUpdate) {}
	}

	rb := DeployRollbackData{TargetPartition: plan.TargetPartition, PreviouslyEmpty: true}
	rollbackData, _ = json.Marshal(rb)

	switch plan.Format {
	case FormatLinuxISO:
		if err := deploySquashfsFromISO(plan.ImagePath, plan.TargetPartition, notify); err != nil {
			notify(StepUpdate{StepName: "extracting squashfs failed; triggering raw fallback", Done: false})
			if ddErr := ddImageToDevice(plan.ImagePath, plan.TargetPartition); ddErr != nil {
				return rollbackData, fmt.Errorf("Linux squashfs extraction failed (%v); fallback failed: %w", err, ddErr)
			}
		}
	case FormatWinISO:
		if err := deployWindowsFromISO(plan.ImagePath, plan.TargetPartition, plan.Credentials, notify); err != nil {
			return rollbackData, fmt.Errorf("Windows deployment failed: %w", err)
		}
	case FormatMacOSImg, FormatRawImg:
		notify(StepUpdate{StepName: "deploying raw image blocks to disk...", Done: false})
		if err := ddImageToDevice(plan.ImagePath, plan.TargetPartition); err != nil {
			return rollbackData, err
		}
	case FormatQCOW2:
		notify(StepUpdate{StepName: "converting QCOW2 image to raw...", Done: false})
		rawPath := plan.ImagePath + ".raw"
		if out, err := exec.Command("qemu-img", "convert", "-O", "raw", plan.ImagePath, rawPath).CombinedOutput(); err != nil {
			return rollbackData, fmt.Errorf("qemu-img convert failed: %w\n%s", err, string(out))
		}
		defer os.Remove(rawPath)
		notify(StepUpdate{StepName: "deploying converted raw image blocks...", Done: false})
		if err := ddImageToDevice(rawPath, plan.TargetPartition); err != nil {
			return rollbackData, err
		}
	case FormatTarGz, FormatTarXz:
		notify(StepUpdate{StepName: "extracting rootfs tarball archive...", Done: false})
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

// deploySquashfsFromISO formats ext4 and streams live percentage extraction telemetry.
func deploySquashfsFromISO(isoPath, targetPartition string, notify func(StepUpdate)) error {
	AssertHostTooling(nil)

	notify(StepUpdate{StepName: fmt.Sprintf("formatting %s as persistent ext4...", targetPartition), Done: false})
	_ = exec.Command("umount", "-l", targetPartition).Run()
	if out, err := exec.Command("mkfs.ext4", "-F", "-q", "-L", "PARROT_ROOT", targetPartition).CombinedOutput(); err != nil {
		return fmt.Errorf("formatting target %s as ext4: %w\n%s", targetPartition, err, string(out))
	}

	targetMount, err := os.MkdirTemp("", "orch-target-*")
	if err != nil {
		return fmt.Errorf("creating target mountpoint: %w", err)
	}
	defer os.RemoveAll(targetMount)

	if out, err := exec.Command("mount", targetPartition, targetMount).CombinedOutput(); err != nil {
		return fmt.Errorf("mounting target %s to %s: %w\n%s", targetPartition, targetMount, err, string(out))
	}
	defer func() {
		_ = exec.Command("sync").Run()
		_ = exec.Command("umount", "-l", targetMount).Run()
	}()

	isoMount, err := os.MkdirTemp("", "orch-iso-*")
	if err != nil {
		return fmt.Errorf("creating iso mountpoint: %w", err)
	}
	defer os.RemoveAll(isoMount)

	if out, err := exec.Command("mount", "-o", "loop,ro", isoPath, isoMount).CombinedOutput(); err != nil {
		return fmt.Errorf("mounting iso %s: %w\n%s", isoPath, err, string(out))
	}
	defer exec.Command("umount", "-l", isoMount).Run()

	var squashPath string
	candidates := []string{
		filepath.Join(isoMount, "live", "filesystem.squashfs"),
		filepath.Join(isoMount, "casper", "filesystem.squashfs"),
		filepath.Join(isoMount, "install", "filesystem.squashfs"),
		filepath.Join(isoMount, "arch", "x86_64", "airootfs.sfs"),
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			squashPath = c
			break
		}
	}

	if squashPath == "" {
		_ = filepath.Walk(isoMount, func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.HasSuffix(info.Name(), ".squashfs") {
				squashPath = path
				return filepath.SkipAll
			}
			return nil
		})
	}

	if squashPath == "" {
		return fmt.Errorf("no squashfs filesystem detected inside Linux ISO image")
	}

	notify(StepUpdate{StepName: "extracting squashfs filesystem: 0%", Done: false})

	if _, err := exec.LookPath("unsquashfs"); err == nil {
		cmd := exec.Command("unsquashfs", "-f", "-percentage", "-d", targetMount, squashPath)
		stdout, pipeErr := cmd.StdoutPipe()
		if pipeErr == nil {
			cmd.Stderr = cmd.Stdout
			if err := cmd.Start(); err == nil {
				scanner := bufio.NewScanner(stdout)
				pctRegex := regexp.MustCompile(`(\d{1,3})%`)
				lastReported := -1

				for scanner.Scan() {
					txt := scanner.Text()
					matches := pctRegex.FindStringSubmatch(txt)
					if len(matches) > 1 {
						var pct int
						if _, scanErr := fmt.Sscanf(matches[1], "%d", &pct); scanErr == nil {
							if pct != lastReported && pct >= 0 && pct <= 100 {
								lastReported = pct
								notify(StepUpdate{
									StepName: fmt.Sprintf("extracting squashfs filesystem: %d%%", pct),
									Done:     false,
								})
							}
						}
					}
				}
				if wErr := cmd.Wait(); wErr != nil {
					return fmt.Errorf("unsquashfs extraction failed: %w", wErr)
				}
			} else {
				out, cErr := cmd.CombinedOutput()
				if cErr != nil {
					return fmt.Errorf("unsquashfs extraction failed: %w\n%s", cErr, string(out))
				}
			}
		} else {
			out, cErr := cmd.CombinedOutput()
			if cErr != nil {
				return fmt.Errorf("unsquashfs extraction failed: %w\n%s", cErr, string(out))
			}
		}
	} else {
		sqMount, sqErr := os.MkdirTemp("", "orch-sq-*")
		if sqErr != nil {
			return fmt.Errorf("creating squash mountpoint: %w", sqErr)
		}
		defer os.RemoveAll(sqMount)

		if out, err := exec.Command("mount", "-t", "squashfs", "-o", "loop,ro", squashPath, sqMount).CombinedOutput(); err != nil {
			return fmt.Errorf("mounting squashfs %s: %w\n%s", squashPath, err, string(out))
		}
		defer exec.Command("umount", "-l", sqMount).Run()

		var copyCmd *exec.Cmd
		if _, rErr := exec.LookPath("rsync"); rErr == nil {
			copyCmd = exec.Command("rsync", "-aHAX", sqMount+"/", targetMount+"/")
		} else {
			copyCmd = exec.Command("cp", "-a", sqMount+"/.", targetMount+"/")
		}

		if out, err := copyCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("extracting squashfs into target failed: %w\n%s", err, string(out))
		}
	}

	notify(StepUpdate{StepName: "extracting squashfs filesystem: 100%", Done: false})

	_ = exec.Command("tune2fs", "-m", "1", targetPartition).Run()

	// Ensure PID 1 systemd link is established immediately
	initTarget := filepath.Join(targetMount, "sbin", "init")
	_ = os.Remove(initTarget)
	_ = os.Symlink("/lib/systemd/systemd", initTarget)

	// Populate deterministic root filesystem fstab mapping
	uuidOut, bErr := exec.Command("blkid", "-s", "UUID", "-o", "value", targetPartition).Output()
	if bErr == nil && len(strings.TrimSpace(string(uuidOut))) > 0 {
		uuidStr := strings.TrimSpace(string(uuidOut))
		fstabEntry := fmt.Sprintf("UUID=%s / ext4 defaults,noatime,errors=remount-ro 0 1\n", uuidStr)
		_ = os.MkdirAll(filepath.Join(targetMount, "etc"), 0755)
		_ = os.WriteFile(filepath.Join(targetMount, "etc", "fstab"), []byte(fstabEntry), 0644)
	}

	targetBoot := filepath.Join(targetMount, "boot")
	_ = os.MkdirAll(targetBoot, 0755)
	if !hasVmlinuz(targetBoot) {
		notify(StepUpdate{StepName: "syncing kernel and initramfs to target boot...", Done: false})
		isoLive := filepath.Join(isoMount, "live")
		if entries, err := os.ReadDir(isoLive); err == nil {
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "vmlinuz") || strings.HasPrefix(e.Name(), "initrd") {
					_ = copyFile(filepath.Join(isoLive, e.Name()), filepath.Join(targetBoot, e.Name()))
				}
			}
		}
	}

	notify(StepUpdate{StepName: "regenerating initramfs for persistent root (mandatory)...", Done: false})
	if err := regenerateInitramfs(targetMount); err != nil {
		return fmt.Errorf("initramfs regeneration failed: %w", err)
	}

	purgeLiveInstallerShortcuts(targetMount)
	return nil
}

// regenerateInitramfs runs the correct initramfs regen tool inside a chroot
// of targetMount, binding /dev /proc /sys first.
func regenerateInitramfs(targetMount string) error {
	binds := []string{"dev", "proc", "sys"}
	for _, b := range binds {
		dest := filepath.Join(targetMount, b)
		_ = os.MkdirAll(dest, 0755)
		if out, err := exec.Command("mount", "--bind", "/"+b, dest).CombinedOutput(); err != nil {
			return fmt.Errorf("bind mounting /%s: %w (%s)", b, err, string(out))
		}
	}
	defer func() {
		for i := len(binds) - 1; i >= 0; i-- {
			_ = exec.Command("umount", "-l", filepath.Join(targetMount, binds[i])).Run()
		}
	}()

	if hostDNS, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		_ = os.WriteFile(filepath.Join(targetMount, "etc", "resolv.conf"), hostDNS, 0644)
	}

	var cmd *exec.Cmd
	switch {
	case fileExists(filepath.Join(targetMount, "usr", "sbin", "update-initramfs")),
		fileExists(filepath.Join(targetMount, "usr", "bin", "update-initramfs")):
		cmd = exec.Command("chroot", targetMount, "update-initramfs", "-u", "-k", "all")
	case fileExists(filepath.Join(targetMount, "usr", "bin", "mkinitcpio")):
		cmd = exec.Command("chroot", targetMount, "mkinitcpio", "-P")
	case fileExists(filepath.Join(targetMount, "usr", "bin", "dracut")):
		cmd = exec.Command("chroot", targetMount, "dracut", "-f", "--regenerate-all")
	default:
		return fmt.Errorf("no known initramfs tool (update-initramfs/mkinitcpio/dracut) found in target rootfs")
	}

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v failed: %w\n%s", cmd.Args, err, string(out))
	}
	return nil
}

// deployWindowsFromISO formats NTFS, extracts install.wim via wimapply, and injects unattended setup.
func deployWindowsFromISO(isoPath, targetPartition string, creds Credentials, notify func(StepUpdate)) error {
	AssertHostTooling(nil)

	notify(StepUpdate{StepName: fmt.Sprintf("formatting %s as NTFS...", targetPartition), Done: false})
	_ = exec.Command("umount", "-l", targetPartition).Run()
	if out, err := exec.Command("mkfs.ntfs", "-Q", "-F", "-L", "WINDOWS", targetPartition).CombinedOutput(); err != nil {
		return fmt.Errorf("formatting target %s as NTFS: %w\n%s", targetPartition, err, string(out))
	}

	isoMount, err := os.MkdirTemp("", "orch-winiso-*")
	if err != nil {
		return fmt.Errorf("creating win iso mountpoint: %w", err)
	}
	defer os.RemoveAll(isoMount)

	if out, err := exec.Command("mount", "-o", "loop,ro", isoPath, isoMount).CombinedOutput(); err != nil {
		return fmt.Errorf("mounting Windows ISO %s: %w\n%s", isoPath, err, string(out))
	}
	defer exec.Command("umount", "-l", isoMount).Run()

	wimPath := filepath.Join(isoMount, "sources", "install.wim")
	if _, err := os.Stat(wimPath); os.IsNotExist(err) {
		wimPath = filepath.Join(isoMount, "sources", "install.esd")
	}

	if _, err := os.Stat(wimPath); os.IsNotExist(err) {
		return fmt.Errorf("neither install.wim nor install.esd found in Windows ISO")
	}

	notify(StepUpdate{StepName: "extracting Windows WIM image to target...", Done: false})
	cmd := exec.Command("wimapply", wimPath, "1", targetPartition)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("wimapply extraction failed: %w\n%s", err, string(out))
	}

	targetMount, err := os.MkdirTemp("", "orch-wintarget-*")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(targetMount)

	if _, err := exec.Command("mount", targetPartition, targetMount).CombinedOutput(); err == nil {
		defer func() {
			_ = exec.Command("sync").Run()
			_ = exec.Command("umount", "-l", targetMount).Run()
		}()

		pantherDir := filepath.Join(targetMount, "Windows", "Panther")
		_ = os.MkdirAll(pantherDir, 0755)
		// Windows's unattend answer file has no hashed-password option for
		// AdministratorPassword/AutoLogon, so the plaintext must be
		// embedded here verbatim — that is inherent to the mechanism, not a
		// choice made by this tool (see Credentials' doc comment). What is
		// a choice is not hardcoding a fixed account (audit 2.3): this now
		// uses whatever username/password the caller actually configured.
		adminPass := xmlEscape(creds.Password)
		logonUser := xmlEscape(creds.Username)
		unattendXML := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<unattend xmlns="urn:schemas-microsoft-com:unattend">
    <settings pass="oobeSystem">
        <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
            <OOBE>
                <HideEULAPage>true</HideEULAPage>
                <HideOnlineAccountScreens>true</HideOnlineAccountScreens>
                <HideWirelessSetupInOOBE>true</HideWirelessSetupInOOBE>
                <ProtectYourPC>3</ProtectYourPC>
            </OOBE>
            <UserAccounts>
                <AdministratorPassword>
                    <Value>%[1]s</Value>
                    <PlainText>true</PlainText>
                </AdministratorPassword>
            </UserAccounts>
            <AutoLogon>
                <Password><Value>%[1]s</Value><PlainText>true</PlainText></Password>
                <Enabled>true</Enabled>
                <LogonCount>1</LogonCount>
                <Username>%[2]s</Username>
            </AutoLogon>
        </component>
    </settings>
</unattend>`, adminPass, logonUser)
		_ = os.WriteFile(filepath.Join(pantherDir, "unattend.xml"), []byte(unattendXML), 0644)
	}

	return nil
}

func hasVmlinuz(bootDir string) bool {
	entries, err := os.ReadDir(bootDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "vmlinuz") {
			return true
		}
	}
	return false
}

func purgeLiveInstallerShortcuts(rootfs string) {
	// 1. Remove all installer desktop shortcuts from root, skel, applications, and all user homes
	desktopPaths := []string{
		filepath.Join(rootfs, "root", "Desktop"),
		filepath.Join(rootfs, "etc", "skel", "Desktop"),
		filepath.Join(rootfs, "usr", "share", "applications"),
	}

	if homeEntries, err := os.ReadDir(filepath.Join(rootfs, "home")); err == nil {
		for _, h := range homeEntries {
			if h.IsDir() {
				desktopPaths = append(desktopPaths, filepath.Join(rootfs, "home", h.Name(), "Desktop"))
			}
		}
	}

	for _, p := range desktopPaths {
		_ = filepath.Walk(p, func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				lower := strings.ToLower(info.Name())
				if strings.Contains(lower, "calamares") ||
					strings.Contains(lower, "install-parrot") ||
					strings.Contains(lower, "install-kali") ||
					strings.Contains(lower, "install debian") ||
					strings.Contains(lower, "readme") ||
					strings.Contains(lower, "ubiquity") {
					_ = os.Remove(path)
				}
			}
			return nil
		})
	}

	// 2. Bind kernel directories and purge calamares package and lingering services
	binds := []string{"dev", "proc", "sys"}
	for _, b := range binds {
		dest := filepath.Join(rootfs, b)
		_ = os.MkdirAll(dest, 0755)
		_ = exec.Command("mount", "--bind", "/"+b, dest).Run()
	}
	defer func() {
		for i := len(binds) - 1; i >= 0; i-- {
			_ = exec.Command("umount", "-l", filepath.Join(rootfs, binds[i])).Run()
		}
	}()

	cleanCmd := `
export DEBIAN_FRONTEND=noninteractive
apt-get purge -y calamares calamares-settings-debian calamares-settings-parrot 2>/dev/null || true
systemctl disable calamares.service 2>/dev/null || true
`
	_ = exec.Command("chroot", rootfs, "/bin/sh", "-c", cleanCmd).Run()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
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

	if err := normalizeRootDirectoryStructure(mountPoint); err != nil {
		return fmt.Errorf("normalizing rootfs structure: %w", err)
	}

	return nil
}

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
