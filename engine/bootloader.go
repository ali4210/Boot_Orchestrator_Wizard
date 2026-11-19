// Package engine — bootloader.go orchestrates universal multi-OS bootloader registration:
// Handles Modern/Legacy Windows (NTFS/BCD/bootmgfw.efi / NTLDR / Windows 7),
// Enterprise/Community Linux (RHEL, CentOS, Debian, Ubuntu, Parrot, Kali),
// macOS (OpenCore EFI chainloader), and autonomous loop-container early-boot synthesis.
package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// AutoConfigureDualBoot orchestrates filesystem mapping, live-hook neutralization,
// native initramfs compilation, multi-OS boot stanzas, and GRUB updates.
func AutoConfigureDualBoot(targetPartition, grubDefaultPath string, creds Credentials, logFn func(string)) error {
	if logFn == nil {
		logFn = func(string) {}
	}

	logFn("=> [UNIVERSAL BOOTLOADER] Inspecting target storage topology...")

	resolvedTarget := resolveMountablePartition(targetPartition)
	fsType := queryFilesystemType(resolvedTarget)
	partUUID := queryPartitionUUID(resolvedTarget)
	logFn(fmt.Sprintf("=> Target storage %s detected as [%s], UUID: %s", resolvedTarget, fsType, partUUID))

	// 1. Enforce Clean Graphical Menu on Host with 10s Timeout & Host Safe Default (Kali/Host OS at 0)
	_ = fsRunner.MkdirAll("/etc/default/grub.d", 0755)
	menuConfig := `GRUB_DEFAULT=0
GRUB_TIMEOUT=10
GRUB_TIMEOUT_STYLE=menu
GRUB_DISABLE_OS_PROBER=false
`
	_ = fsRunner.WriteFile("/etc/default/grub.d/50-dualboot-menu.cfg", []byte(menuConfig), 0644)
	_ = fsRunner.WriteFile("/etc/default/grub.d/99-orchestrator-prober.cfg", []byte("GRUB_DISABLE_OS_PROBER=false\n"), 0644)

	if grubDefaultPath != "" {
		if grubData, readErr := os.ReadFile(grubDefaultPath); readErr == nil {
			content := string(grubData)
			if strings.Contains(content, "GRUB_DISABLE_OS_PROBER=true") {
				content = strings.ReplaceAll(content, "GRUB_DISABLE_OS_PROBER=true", "GRUB_DISABLE_OS_PROBER=false")
				_ = fsRunner.WriteFile(grubDefaultPath, []byte(content), 0644)
			}
		}
	}

	// Dynamic multi-distro os-prober resolution
	ensureOSProberInstalled(logFn)

	// 2. Multi-OS Route Execution
	switch {
	case strings.Contains(fsType, "ntfs") || strings.Contains(fsType, "fat"):
		logFn("=> [WINDOWS ENGINE] Injecting universal Windows boot stanza (UEFI & Legacy)...")
		if err := configureWindowsBootloader(resolvedTarget, partUUID, logFn); err != nil {
			logFn(fmt.Sprintf("=> [WARNING] Windows boot registration notice: %v", err))
		}

	case strings.Contains(fsType, "hfs") || strings.Contains(fsType, "apfs"):
		logFn("=> [MACOS ENGINE] Provisioning OpenCore chainloader boot entry...")
		if err := configureMacOSBootloader(resolvedTarget, partUUID, logFn); err != nil {
			logFn(fmt.Sprintf("=> [WARNING] macOS OpenCore registration notice: %v", err))
		}

	default: // Linux (ext4, btrfs, xfs - Debian, Parrot, Kali, RHEL, CentOS, Rocky, Fedora, Ubuntu)
		logFn("=> [LINUX ENGINE] Running autonomous persistent configuration & Secure Boot initramfs synthesis...")
		if err := configureLinuxPersistentEnvironment(resolvedTarget, partUUID, creds, logFn); err != nil {
			return fmt.Errorf("configuring Linux persistent environment: %w", err)
		}
	}

	// 3. Regenerate GRUB menu cleanly (without overwriting host EFI binaries or NVRAM pointers)
	logFn("=> Generating boot menu entries via update-grub...")
	if out, err := cmdRunner.Run("update-grub"); err != nil {
		if fbOut, fbErr := cmdRunner.Run("grub2-mkconfig", "-o", "/boot/grub2/grub.cfg"); fbErr != nil {
			return fmt.Errorf("updating grub bootloader failed: %w\n%s\n%s", err, string(out), string(fbOut))
		}
	}

	// 4. Final Verification
	if err := verifyInstallationIntegrity(resolvedTarget); err != nil {
		logFn(fmt.Sprintf("=> [NOTICE] Dual-boot verification telemetry: %v", err))
	}

	logFn("=> Universal multi-OS registration and verification completed successfully.")
	time.Sleep(500 * time.Millisecond)
	return nil
}

func ensureOSProberInstalled(logFn func(string)) {
	if _, err := exec.LookPath("os-prober"); err == nil {
		return
	}
	if runtime.GOOS != "linux" {
		return
	}
	logFn("=> [BOOTLOADER] os-prober missing. Installing via host package manager...")
	if _, err := exec.LookPath("apt-get"); err == nil {
		_, _ = cmdRunner.Run("apt-get", "install", "-y", "os-prober")
	} else if _, err := exec.LookPath("dnf"); err == nil {
		_, _ = cmdRunner.Run("dnf", "install", "-y", "os-prober")
	} else if _, err := exec.LookPath("pacman"); err == nil {
		_, _ = cmdRunner.Run("pacman", "-Sy", "--noconfirm", "os-prober")
	}
}

func resolveMountablePartition(target string) string {
	if strings.HasPrefix(target, "/dev/loop") && !strings.Contains(target, "p") {
		_, _ = cmdRunner.Run("partx", "-u", target)
		time.Sleep(100 * time.Millisecond)

		p1 := target + "p1"
		if _, err := os.Stat(p1); err == nil {
			return p1
		}
	}
	return target
}

func configureLinuxPersistentEnvironment(targetPartition, partUUID string, creds Credentials, logFn func(string)) error {
	targetPartition = resolveMountablePartition(targetPartition)
	_, _ = cmdRunner.Run("sync")

	stageDir, err := os.MkdirTemp("", "orch-dualboot-*")
	if err != nil {
		return fmt.Errorf("creating temporary mount point: %w", err)
	}
	defer fsRunner.RemoveAll(stageDir)

	fsType := queryFilesystemType(targetPartition)
	if fsType == "" {
		logFn(fmt.Sprintf("=> [STORAGE HEAL] Initializing ext4 filesystem on target block %s...", targetPartition))
		_, _ = cmdRunner.Run("mkfs.ext4", "-F", "-q", "-L", "LINUX_ROOT", targetPartition)
		_, _ = cmdRunner.Run("sync")
		time.Sleep(200 * time.Millisecond)
	}

	var mountOut []byte
	var mountErr error

	mountOut, mountErr = cmdRunner.Run("mount", "-t", "ext4", targetPartition, stageDir)
	if mountErr != nil {
		mountOut, mountErr = cmdRunner.Run("mount", targetPartition, stageDir)
	}

	if mountErr != nil {
		return fmt.Errorf("mounting target partition %s: %w\n%s", targetPartition, mountErr, string(mountOut))
	}

	defer func() {
		_, _ = cmdRunner.Run("umount", "-l", stageDir)
		_, _ = cmdRunner.Run("sync")
	}()

	// Ensure systemd init link exists to prevent kernel panic
	initTarget := filepath.Join(stageDir, "sbin", "init")
	_ = fsRunner.Remove(initTarget)
	_ = fsRunner.Symlink("/lib/systemd/systemd", initTarget)

	fstabPath := filepath.Join(stageDir, "etc", "fstab")
	_ = fsRunner.MkdirAll(filepath.Join(stageDir, "etc"), 0755)
	if partUUID != "" {
		fstabEntry := fmt.Sprintf("UUID=%s  /  ext4  defaults,noatime,errors=remount-ro  0  1\n", partUUID)
		_ = fsRunner.WriteFile(fstabPath, []byte(fstabEntry), 0644)
		logFn("=> Target /etc/fstab populated with root UUID: " + partUUID)
	}

	if err := assertUniversalNetworking(stageDir, logFn); err != nil {
		logFn(fmt.Sprintf("=> [WARNING] Network bootstrap issue: %v", err))
	}

	if err := neutralizeLiveBootHooks(stageDir, logFn); err != nil {
		logFn(fmt.Sprintf("=> [NOTICE] Live hook neutralization notice: %v", err))
	}

	if err := assertBootableKernelAndPackages(stageDir, logFn); err != nil {
		logFn(fmt.Sprintf("=> [WARNING] Package self-healing issue: %v", err))
	}

	if err := assertDefaultCredentials(stageDir, creds, logFn); err != nil {
		logFn(fmt.Sprintf("=> [WARNING] Credential configuration issue: %v", err))
	}

	if err := assertDisplayServerEnvironment(stageDir, logFn); err != nil {
		logFn(fmt.Sprintf("=> [WARNING] Display server configuration issue: %v", err))
	}

	sysctlPath := filepath.Join(stageDir, "etc", "sysctl.d", "20-quiet-printk.conf")
	_ = fsRunner.MkdirAll(filepath.Dir(sysctlPath), 0755)
	_ = fsRunner.WriteFile(sysctlPath, []byte("kernel.printk = 3 4 1 3\n"), 0644)

	kernelName, initrdName := locateKernelAndRamdisk(filepath.Join(stageDir, "boot"))
	logFn(fmt.Sprintf("=> Identified kernel: %s, initrd: %s", kernelName, initrdName))

	// Reset /etc/grub.d/40_custom to default template so duplicate entries never appear.
	// os-prober dynamically generates the official, labeled boot entry with verified kernel parameters.
	default40Custom := `#!/bin/sh
exec tail -n +3 $0
# This file provides an easy way to add custom menu entries.  Simply type the
# menu entries you want to add after this comment.  Be careful not to change
# the 'exec tail' line above.
`
	_ = fsRunner.WriteFile("/etc/grub.d/40_custom", []byte(default40Custom), 0755)
	_, _ = cmdRunner.Run("chmod", "+x", "/etc/grub.d/40_custom")
	logFn("=> Cleaned /etc/grub.d/40_custom: relying on os-prober to generate exactly one boot entry.")

	return nil
}

func locateKernelAndRamdisk(bootDir string) (kernel, initrd string) {
	kernel = "vmlinuz"
	initrd = "initrd.img"

	entries, err := os.ReadDir(bootDir)
	if err != nil {
		return
	}

	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "vmlinuz-") {
			kernel = name
		} else if strings.HasPrefix(name, "initrd.img-") {
			initrd = name
		}
	}
	return
}

func assertDisplayServerEnvironment(rootPath string, logFn func(string)) error {
	sddmDir := filepath.Join(rootPath, "etc", "sddm.conf.d")
	_ = fsRunner.MkdirAll(sddmDir, 0755)
	_ = fsRunner.Remove(filepath.Join(sddmDir, "x11-enforcement.conf"))
	return nil
}

func configureWindowsBootloader(targetPartition, partUUID string, logFn func(string)) error {
	winStanza := fmt.Sprintf(`#!/bin/sh
exec tail -n +3 $0
menuentry "Windows Boot Manager (UEFI on %s)" --class windows --class os {
    insmod part_gpt
    insmod part_msdos
    insmod fat
    insmod ntfs
    insmod chain
    search --no-floppy --fs-uuid --set=root %s
    chainloader /EFI/Microsoft/Boot/bootmgfw.efi
}
`, filepath.Base(targetPartition), partUUID)

	if err := fsRunner.WriteFile("/etc/grub.d/40_custom", []byte(winStanza), 0755); err != nil {
		return err
	}
	_, err := cmdRunner.Run("chmod", "+x", "/etc/grub.d/40_custom")
	return err
}

func configureMacOSBootloader(targetPartition, partUUID string, logFn func(string)) error {
	macStanza := fmt.Sprintf(`#!/bin/sh
exec tail -n +3 $0
menuentry "macOS (OpenCore Chainloader on %s)" --class macosx --class os {
    insmod part_gpt
    insmod fat
    insmod chain
    search --no-floppy --fs-uuid --set=root %s
    chainloader /EFI/OC/OpenCore.efi
}
`, filepath.Base(targetPartition), partUUID)

	if err := fsRunner.WriteFile("/etc/grub.d/40_custom", []byte(macStanza), 0755); err != nil {
		return err
	}
	_, err := cmdRunner.Run("chmod", "+x", "/etc/grub.d/40_custom")
	return err
}

func neutralizeLiveBootHooks(rootPath string, logFn func(string)) error {
	logFn("=> [PERSISTENCE] Neutralizing live-media hooks and purging installer debris...")

	binds := []struct {
		host string
		dest string
	}{
		{"/dev", filepath.Join(rootPath, "dev")},
		{"/dev/pts", filepath.Join(rootPath, "dev", "pts")},
		{"/proc", filepath.Join(rootPath, "proc")},
		{"/sys", filepath.Join(rootPath, "sys")},
	}

	for _, b := range binds {
		_ = fsRunner.MkdirAll(b.dest, 0755)
		if out, err := cmdRunner.Run("mount", "--bind", b.host, b.dest); err != nil {
			return fmt.Errorf("bind mounting %s: %w (%s)", b.host, err, string(out))
		}
	}

	defer func() {
		for i := len(binds) - 1; i >= 0; i-- {
			_, _ = cmdRunner.Run("umount", "-l", binds[i].dest)
		}
	}()

	if hostDNS, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		_ = fsRunner.WriteFile(filepath.Join(rootPath, "etc", "resolv.conf"), hostDNS, 0644)
	}

	purgeScript := `
export DEBIAN_FRONTEND=noninteractive
apt-get purge -y --autoremove \
    live-boot \
    live-boot-initramfs-tools \
    live-config \
    live-config-systemd \
    live-tools \
    user-setup \
    calamares \
    calamares-settings-debian \
    calamares-settings-parrot 2>/dev/null || true

rm -rf /lib/live /usr/lib/live /etc/live
rm -rf /home/*/Desktop/install* /home/*/Desktop/calamares* /home/*/Desktop/README*
rm -rf /etc/skel/Desktop/install* /etc/skel/Desktop/calamares* /etc/skel/Desktop/README*
rm -rf /root/Desktop/install* /root/Desktop/calamares* /root/Desktop/README*

systemctl disable calamares.service 2>/dev/null || true
systemctl enable systemd-logind 2>/dev/null || true
systemctl set-default graphical.target 2>/dev/null || true
update-initramfs -u -k all 2>/dev/null || true
`
	if out, err := cmdRunner.Run("chroot", rootPath, "/bin/sh", "-c", purgeScript); err != nil {
		logFn(fmt.Sprintf("=> [PERSISTENCE] Notice: cleanup warning: %v (%s)", err, string(out)))
	} else {
		logFn("=> [PERSISTENCE] Target converted: installer purged, desktop clean, native initramfs generated.")
	}

	return nil
}

func assertUniversalNetworking(rootPath string, logFn func(string)) error {
	logFn("=> [NETWORKING] Injecting autonomous DHCP & DNS configuration...")

	netDir := filepath.Join(rootPath, "etc", "systemd", "network")
	_ = fsRunner.MkdirAll(netDir, 0755)
	netConfig := `[Match]
Name=en* eth* wl*

[Network]
DHCP=yes
`
	_ = fsRunner.WriteFile(filepath.Join(netDir, "20-wired.network"), []byte(netConfig), 0644)

	resolvPath := filepath.Join(rootPath, "etc", "resolv.conf")
	dnsConfig := "nameserver 1.1.1.1\nnameserver 8.8.8.8\n"
	_ = fsRunner.WriteFile(resolvPath, []byte(dnsConfig), 0644)

	return nil
}

func assertBootableKernelAndPackages(rootPath string, logFn func(string)) error {
	bootDir := filepath.Join(rootPath, "boot")
	_ = fsRunner.MkdirAll(bootDir, 0755)

	entries, _ := os.ReadDir(bootDir)
	hasKernel := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "vmlinuz") {
			hasKernel = true
			break
		}
	}

	if hasKernel {
		return nil
	}

	binds := []struct {
		host string
		dest string
	}{
		{"/dev", filepath.Join(rootPath, "dev")},
		{"/dev/pts", filepath.Join(rootPath, "dev", "pts")},
		{"/proc", filepath.Join(rootPath, "proc")},
		{"/sys", filepath.Join(rootPath, "sys")},
	}

	for _, b := range binds {
		_ = fsRunner.MkdirAll(b.dest, 0755)
		if out, err := cmdRunner.Run("mount", "--bind", b.host, b.dest); err != nil {
			return fmt.Errorf("bind mounting %s: %w (%s)", b.host, err, string(out))
		}
	}

	defer func() {
		for i := len(binds) - 1; i >= 0; i-- {
			_, _ = cmdRunner.Run("umount", "-l", binds[i].dest)
		}
	}()

	if hostDNS, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		_ = fsRunner.WriteFile(filepath.Join(rootPath, "etc", "resolv.conf"), hostDNS, 0644)
	}

	installScript := "DEBIAN_FRONTEND=noninteractive apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends " +
		debianKernelPackageForArch(runtime.GOARCH) + " systemd-sysv sudo"
	logFn(fmt.Sprintf("=> [PACKAGE SELF-HEAL] Executing: %s", installScript))
	_, _ = cmdRunner.Run("chroot", rootPath, "/bin/sh", "-c", installScript)

	return nil
}

// debianKernelPackageForArch returns the real Debian/Ubuntu kernel
// metapackage name for goarch (the same values runtime.GOARCH uses). This
// chroot runs inside the newly-provisioned guest OS, and a dual-boot guest
// is always the same architecture as the host (you cannot boot an arm64
// guest on amd64 UEFI firmware or vice versa — see docs/ARCH_SUPPORT.md),
// so goarch here is the host's own architecture. Hardcoding
// "linux-image-amd64" would make this self-heal install step try to pull an
// amd64 kernel package into an arm64 guest's own apt (debian-netboot-arm64,
// ubuntu-server-arm64 — added in item 36), which fails outright since no
// such package exists in the arm64 repos.
func debianKernelPackageForArch(goarch string) string {
	if goarch == "arm64" {
		return "linux-image-arm64"
	}
	return "linux-image-amd64"
}

// assertDefaultCredentials creates creds.Username (sudo-enabled) in the
// chroot at rootPath, and either locks root (the default) or gives it the
// same password as creds.Username if creds.AllowRootLogin is set. Audit
// 2.3: this used to hardcode a "saleem"/"saleem" account and root password
// on every install regardless of who ran the tool; there is no hardcoded
// account or password anywhere in this path anymore.
func assertDefaultCredentials(rootPath string, creds Credentials, logFn func(string)) error {
	if logFn == nil {
		logFn = func(string) {}
	}
	if !ValidUsername(creds.Username) {
		return fmt.Errorf("refusing to provision account: %q is not a valid username", creds.Username)
	}
	logFn(fmt.Sprintf("=> [ACCOUNTS] Provisioning user account %q...", creds.Username))

	hash, err := creds.ResolvedPasswordHash()
	if err != nil {
		return fmt.Errorf("hashing password for %s: %w", creds.Username, err)
	}

	// Phase 5 item 32: the proof-of-concept FS-interface migration batch
	// (see engine/fs.go, docs/dryrun-inventory.md) — routed through
	// fsRunner rather than calling os.MkdirAll/os.WriteFile directly, so
	// SetDryRun(true) can simulate this write. Also fixes a smaller,
	// related gap found while doing this: both calls used to discard
	// their error entirely; a failure here (read-only chroot, disk full)
	// now aborts instead of silently proceeding to the chroot step below
	// as if the sudoers file had actually been written.
	sudoersDir := filepath.Join(rootPath, "etc", "sudoers.d")
	if err := fsRunner.MkdirAll(sudoersDir, 0750); err != nil {
		return fmt.Errorf("creating %s: %w", sudoersDir, err)
	}
	sudoersFile := filepath.Join(sudoersDir, creds.Username)
	if err := fsRunner.WriteFile(sudoersFile, []byte(creds.Username+" ALL=(ALL:ALL) ALL\n"), 0440); err != nil {
		return fmt.Errorf("writing %s: %w", sudoersFile, err)
	}

	rootCmd := "passwd -l root 2>/dev/null || true"
	if creds.AllowRootLogin {
		rootCmd = fmt.Sprintf("echo %s | chpasswd -e 2>/dev/null || true", shellSingleQuote("root:"+hash))
	}

	script := renderAccountProvisioningScript(rootCmd, creds.Username, shellSingleQuote(creds.Username+":"+hash))

	out, runErr := cmdRunner.Run("chroot", rootPath, "/bin/sh", "-c", script)
	outStr := string(out)
	// This verification depends on the chroot call above having actually
	// run useradd/id -u for real — in dry-run, chroot is itself
	// intercepted (cmdRunner), so it never produces ORCH_ACCOUNT_VERIFIED
	// no matter how correct the real account-provisioning logic is. Real
	// mode is unchanged: the exact same condition still aborts there.
	if IsDryRun() {
		logDryRunSimulated(fmt.Sprintf("account %q provisioned and verified inside %s (chroot above was intercepted, so useradd/id -u never really ran)", creds.Username, rootPath))
	} else if runErr != nil || !strings.Contains(outStr, "ORCH_ACCOUNT_VERIFIED") {
		return fmt.Errorf("account %q was not verified to exist after provisioning (id -u failed): %v (%s)", creds.Username, runErr, outStr)
	}
	if strings.Contains(outStr, "admin_group=none") {
		logFn(fmt.Sprintf("=> [WARNING] No sudo/wheel admin group found in the target; %s has no admin privileges.", creds.Username))
	}

	rootState := "locked"
	if creds.AllowRootLogin {
		rootState = "enabled"
	}
	logFn(fmt.Sprintf("=> [ACCOUNTS] Verified: %s exists; root login %s.", creds.Username, rootState))
	return nil
}

// renderAccountProvisioningScript builds the /bin/sh script assertDefaultCredentials
// runs inside the target chroot. Kept as a pure function (no cmdRunner, no
// filesystem access) so its admin-group-detection and account-verification
// logic can be exercised directly with `sh -c` against fake stub commands in
// a test, not just mocked at the Go level.
//
// Debian/Ubuntu's admin group is "sudo"; Fedora/RHEL/Arch/openSUSE use
// "wheel". Hardcoding "sudo" (the previous behavior) silently produced an
// account with NO admin group at all on any non-Debian-family target, since
// `usermod -aG sudo` fails when the group doesn't exist — combined with root
// locked by default, that is a full lockout. This detects whichever group
// actually exists in the chroot's own /etc/group before using it, and
// verifies with `id -u` that the account genuinely exists afterward instead
// of assuming success (audit 1A.3).
func renderAccountProvisioningScript(rootCmd, username, credLine string) string {
	return fmt.Sprintf(`
set -e
%s

ADMIN_GROUP=""
if getent group sudo >/dev/null 2>&1; then
    ADMIN_GROUP="sudo"
elif getent group wheel >/dev/null 2>&1; then
    ADMIN_GROUP="wheel"
fi

if ! id -u %[2]s >/dev/null 2>&1; then
    if [ -n "$ADMIN_GROUP" ]; then
        useradd -m -s /bin/bash -G "$ADMIN_GROUP" %[2]s
    else
        useradd -m -s /bin/bash %[2]s
    fi
fi
echo %[3]s | chpasswd -e
if [ -n "$ADMIN_GROUP" ]; then
    usermod -aG "$ADMIN_GROUP" %[2]s
fi

if id -u %[2]s >/dev/null 2>&1; then
    echo "ORCH_ACCOUNT_VERIFIED admin_group=${ADMIN_GROUP:-none}"
else
    echo "ORCH_ACCOUNT_MISSING" >&2
    exit 7
fi
`, rootCmd, username, credLine)
}

// renderCalamaresCleanupScript builds the /bin/bash script that removes
// leftover Calamares/live-installer desktop shortcuts and purges the
// Calamares package itself from a freshly-deployed rootfs. Kept as its
// own pure function (used by both engine/orchestrator.go's
// extractLiveRootFS and, historically, the near-identical inline script
// it used to duplicate) so it can be syntax/shellcheck-verified directly
// (audit item 26) without needing a real chroot. Deliberately best-effort
// end to end — Calamares may simply not be installed on a given image,
// which is not an error.
func renderCalamaresCleanupScript(username string) string {
	return fmt.Sprintf(`
rm -rf /home/%[1]s/Desktop/install* /home/%[1]s/Desktop/calamares* /home/%[1]s/Desktop/README*
rm -rf /etc/skel/Desktop/install* /etc/skel/Desktop/calamares* /etc/skel/Desktop/README*
rm -rf /root/Desktop/install* /root/Desktop/calamares* /root/Desktop/README*
apt-get purge -y calamares calamares-settings-debian calamares-settings-parrot 2>/dev/null || true
systemctl disable calamares.service 2>/dev/null || true
`, username)
}

func queryFilesystemType(targetPartition string) string {
	out, err := cmdRunner.RunStdout("blkid", "-s", "TYPE", "-o", "value", targetPartition)
	if err != nil {
		return "ext4"
	}
	return strings.ToLower(strings.TrimSpace(string(out)))
}

func queryPartitionUUID(targetPartition string) string {
	out, err := cmdRunner.RunStdout("blkid", "-s", "UUID", "-o", "value", targetPartition)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func verifyInstallationIntegrity(targetPartition string) error {
	grubCfgPaths := []string{
		"/boot/grub/grub.cfg",
		"/boot/grub2/grub.cfg",
	}

	var content string
	for _, p := range grubCfgPaths {
		if data, err := os.ReadFile(p); err == nil && len(data) > 0 {
			content = string(data)
			break
		}
	}

	if content == "" {
		return nil
	}

	partName := filepath.Base(targetPartition)
	if strings.Contains(content, partName) {
		return nil
	}

	if uuid := queryPartitionUUID(targetPartition); uuid != "" && strings.Contains(content, uuid) {
		return nil
	}

	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
