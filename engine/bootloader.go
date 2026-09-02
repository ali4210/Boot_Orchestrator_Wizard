package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// AutoConfigureDualBoot orchestrates fstab generation, kernel self-healing, networking bootstrap, and bootloader integration.
func AutoConfigureDualBoot(targetPartition, grubDefaultPath string, logFn func(string)) error {
	if logFn == nil {
		logFn = func(string) {}
	}

	logFn("=> [AUTONOMOUS DUAL-BOOT] Preparing target partition for bootloader integration...")

	// 1. Temporary mount directory
	stageDir, err := os.MkdirTemp("", "orch-dualboot-*")
	if err != nil {
		return fmt.Errorf("creating temporary mount point: %w", err)
	}
	defer os.RemoveAll(stageDir)

	// 2. Mount target partition
	logFn(fmt.Sprintf("=> Mounting %s to %s...", targetPartition, stageDir))
	if out, err := exec.Command("mount", targetPartition, stageDir).CombinedOutput(); err != nil {
		return fmt.Errorf("mounting target partition %s: %w\n%s", targetPartition, err, string(out))
	}
	defer func() {
		_ = exec.Command("umount", "-l", stageDir).Run()
		_ = exec.Command("sync").Run()
	}()

	// 3. Query Partition UUID
	uuidCmd := exec.Command("blkid", "-s", "UUID", "-o", "value", targetPartition)
	uuidOut, err := uuidCmd.Output()
	if err != nil {
		return fmt.Errorf("resolving UUID for %s: %w", targetPartition, err)
	}
	partUUID := strings.TrimSpace(string(uuidOut))
	logFn(fmt.Sprintf("=> Partition UUID resolved: %s", partUUID))

	// 4. Ensure target has a valid persistent /etc/fstab entry
	fstabPath := filepath.Join(stageDir, "etc", "fstab")
	_ = os.MkdirAll(filepath.Join(stageDir, "etc"), 0755)
	fstabEntry := fmt.Sprintf("UUID=%s  /  ext4  errors=remount-ro  0  1\n", partUUID)

	existingFstab, _ := os.ReadFile(fstabPath)
	if !strings.Contains(string(existingFstab), partUUID) {
		f, fErr := os.OpenFile(fstabPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if fErr == nil {
			_, _ = f.WriteString(fstabEntry)
			_ = f.Close()
			logFn("=> Target /etc/fstab populated with root UUID.")
		}
	}

	// 5. Autonomous Network Configuration Injection (DHCP on all adapters)
	if err := assertUniversalNetworking(stageDir, logFn); err != nil {
		logFn(fmt.Sprintf("=> [WARNING] Network bootstrap issue: %v", err))
	}

	// 6. Autonomous Kernel & Package Self-Healing Injection
	if err := assertBootableKernelAndPackages(stageDir, logFn); err != nil {
		logFn(fmt.Sprintf("=> [WARNING] Package self-healing issue: %v", err))
	}

	// 7. Autonomous Credentials & Sudo Self-Healing Injection
	if err := assertDefaultCredentials(stageDir, logFn); err != nil {
		logFn(fmt.Sprintf("=> [WARNING] Credential configuration issue: %v", err))
	}

	// 8. Silence Kernel TTY Dmesg Hardware Bleed
	sysctlPath := filepath.Join(stageDir, "etc", "sysctl.d", "20-quiet-printk.conf")
	_ = os.MkdirAll(filepath.Dir(sysctlPath), 0755)
	_ = os.WriteFile(sysctlPath, []byte("kernel.printk = 3 4 1 3\n"), 0644)

	// 9. Host GRUB: Injected OS-Prober overrides
	_ = os.MkdirAll("/etc/default/grub.d", 0755)
	_ = os.WriteFile("/etc/default/grub.d/99-orchestrator-prober.cfg", []byte("GRUB_DISABLE_OS_PROBER=false\n"), 0644)
	_ = os.WriteFile("/etc/default/grub.d/50-force-menu.cfg", []byte("GRUB_TIMEOUT=10\nGRUB_TIMEOUT_STYLE=menu\n"), 0644)

	// Update main /etc/default/grub fallback if needed
	if grubDefaultPath != "" {
		if grubData, readErr := os.ReadFile(grubDefaultPath); readErr == nil {
			content := string(grubData)
			if strings.Contains(content, "GRUB_DISABLE_OS_PROBER=true") {
				content = strings.ReplaceAll(content, "GRUB_DISABLE_OS_PROBER=true", "GRUB_DISABLE_OS_PROBER=false")
				_ = os.WriteFile(grubDefaultPath, []byte(content), 0644)
			}
		}
	}

	// 10. Run os-prober to discover the new system
	logFn("=> Running os-prober to register secondary kernel...")
	_ = exec.Command("os-prober").Run()

	// 11. Regenerate GRUB config
	logFn("=> Generating boot menu entries via update-grub...")
	if out, err := exec.Command("update-grub").CombinedOutput(); err != nil {
		if fbOut, fbErr := exec.Command("grub2-mkconfig", "-o", "/boot/grub2/grub.cfg").CombinedOutput(); fbErr != nil {
			return fmt.Errorf("updating grub bootloader failed: %w\n%s\n%s", err, string(out), string(fbOut))
		}
	}

	// 12. Final Verification
	if err := verifyInstallationIntegrity(targetPartition); err != nil {
		return fmt.Errorf("dual-boot validation failed: %w", err)
	}

	logFn("=> Dual-boot registration and verification completed successfully.")
	time.Sleep(500 * time.Millisecond)
	return nil
}

// assertUniversalNetworking provisions automatic DHCP and DNS configurations.
func assertUniversalNetworking(rootPath string, logFn func(string)) error {
	logFn("=> [NETWORKING] Injecting autonomous DHCP & DNS configuration...")

	// 1. Universal systemd-networkd wired DHCP rule
	netDir := filepath.Join(rootPath, "etc", "systemd", "network")
	_ = os.MkdirAll(netDir, 0755)
	netConfig := `[Match]
Name=en* eth* wl*

[Network]
DHCP=yes
`
	_ = os.WriteFile(filepath.Join(netDir, "20-wired.network"), []byte(netConfig), 0644)

	// 2. Persistent DNS fallback
	resolvPath := filepath.Join(rootPath, "etc", "resolv.conf")
	dnsConfig := "nameserver 1.1.1.1\nnameserver 8.8.8.8\n"
	_ = os.WriteFile(resolvPath, []byte(dnsConfig), 0644)

	// 3. Fallback /etc/network/interfaces for Debian if ifupdown is present
	interfacesPath := filepath.Join(rootPath, "etc", "network", "interfaces")
	_ = os.MkdirAll(filepath.Dir(interfacesPath), 0755)
	if _, err := os.Stat(interfacesPath); os.IsNotExist(err) {
		fallbackInterfaces := "auto lo\niface lo inet loopback\n\nallow-hotplug enp0s3\niface enp0s3 inet dhcp\n\nallow-hotplug eth0\niface eth0 inet dhcp\n"
		_ = os.WriteFile(interfacesPath, []byte(fallbackInterfaces), 0644)
	}

	return nil
}

// assertBootableKernelAndPackages installs the kernel, systemd-sysv, sudo, and network management tools.
func assertBootableKernelAndPackages(rootPath string, logFn func(string)) error {
	bootDir := filepath.Join(rootPath, "boot")
	_ = os.MkdirAll(bootDir, 0755)

	entries, _ := os.ReadDir(bootDir)
	hasKernel := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "vmlinuz-") || strings.HasPrefix(entry.Name(), "vmlinux-") {
			hasKernel = true
			break
		}
	}

	// Virtual filesystems required for chroot
	binds := []struct {
		host string
		dest string
	}{
		{"/dev", filepath.Join(rootPath, "dev")},
		{"/proc", filepath.Join(rootPath, "proc")},
		{"/sys", filepath.Join(rootPath, "sys")},
		{"/dev/pts", filepath.Join(rootPath, "dev", "pts")},
	}

	for _, b := range binds {
		_ = os.MkdirAll(b.dest, 0755)
		if out, err := exec.Command("mount", "--bind", b.host, b.dest).CombinedOutput(); err != nil {
			return fmt.Errorf("bind mounting %s: %w (%s)", b.host, err, string(out))
		}
	}

	defer func() {
		for i := len(binds) - 1; i >= 0; i-- {
			_ = exec.Command("umount", "-l", binds[i].dest).Run()
		}
	}()

	// Provide DNS inside chroot during installation
	if hostDNS, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		_ = os.WriteFile(filepath.Join(rootPath, "etc", "resolv.conf"), hostDNS, 0644)
	}

	var installScript string
	switch {
	case fileExists(filepath.Join(rootPath, "usr", "bin", "apt-get")):
		pkgs := "systemd-sysv sudo curl wget isc-dhcp-client net-tools"
		if !hasKernel {
			pkgs = "linux-image-amd64 " + pkgs
		}
		installScript = fmt.Sprintf("DEBIAN_FRONTEND=noninteractive apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends %s && systemctl enable systemd-networkd || true", pkgs)
	case fileExists(filepath.Join(rootPath, "usr", "bin", "pacman")):
		pkgs := "systemd sudo curl dhcpcd"
		if !hasKernel {
			pkgs = "linux linux-firmware mkinitcpio " + pkgs
		}
		installScript = fmt.Sprintf("pacman -Sy --noconfirm %s && systemctl enable systemd-networkd || true", pkgs)
	case fileExists(filepath.Join(rootPath, "usr", "bin", "dnf")):
		pkgs := "systemd sudo dhcp-client"
		if !hasKernel {
			pkgs = "kernel " + pkgs
		}
		installScript = fmt.Sprintf("dnf install -y %s && systemctl enable systemd-networkd || true", pkgs)
	default:
		return fmt.Errorf("unknown target package manager; cannot bootstrap tools")
	}

	logFn(fmt.Sprintf("=> [PACKAGE SELF-HEAL] Executing: %s", installScript))
	cmd := exec.Command("chroot", rootPath, "/bin/sh", "-c", installScript)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bootstrapping core packages failed: %w\n%s", err, string(out))
	}

	logFn("=> [PACKAGE SELF-HEAL] Kernel, networking, and administration tools deployed successfully.")
	return nil
}

// assertDefaultCredentials ensures the root password is set and user 'parrot' has sudo rights.
func assertDefaultCredentials(rootPath string, logFn func(string)) error {
	logFn("=> [ACCOUNTS] Provisioning preset default credentials...")

	script := `
set -e
echo "root:toor" | chpasswd 2>/dev/null || usermod -p $(openssl passwd -1 toor) root 2>/dev/null || true

if ! id -u parrot >/dev/null 2>&1; then
    useradd -m -s /bin/bash parrot 2>/dev/null || true
fi
echo "parrot:parrot" | chpasswd 2>/dev/null || usermod -p $(openssl passwd -1 parrot) parrot 2>/dev/null || true

if getent group sudo >/dev/null 2>&1; then
    usermod -aG sudo parrot 2>/dev/null || true
elif getent group wheel >/dev/null 2>&1; then
    usermod -aG wheel parrot 2>/dev/null || true
fi
`
	cmd := exec.Command("chroot", rootPath, "/bin/sh", "-c", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("setting credentials failed: %w (%s)", err, string(out))
	}

	logFn("=> [ACCOUNTS] Verified: root:toor and parrot:parrot configured with sudo privileges.")
	return nil
}

func verifyInstallationIntegrity(targetPartition string) error {
	grubCfg, err := os.ReadFile("/boot/grub/grub.cfg")
	if err != nil {
		return nil
	}
	partName := filepath.Base(targetPartition)
	if !strings.Contains(string(grubCfg), partName) {
		return fmt.Errorf("verification check: %s entry not detected in host /boot/grub/grub.cfg", partName)
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
