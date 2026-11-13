package engine

import (
	"fmt"
	"os"
)

// autonomousInstallerScriptParams holds everything renderAutonomousInstallerScript
// needs to produce the transient_resize.sh body. Kept as a struct (rather than
// positional Sprintf args) so the two are easy to keep in sync and the
// render step is unit-testable without any real disk/USB side effects.
type autonomousInstallerScriptParams struct {
	Disk          string
	HostPartDev   string
	TargetPartDev string
	HostESP       string
	HostPartNum   string
	TargetPartNum string
	DistroLabel   string
	Username      string
	PasswordHash  string
	RootCmd       string
}

// renderAutonomousInstallerScript builds the body of transient_resize.sh: the
// autonomous installer that runs unattended after the machine reboots into
// the USB staging environment. It embeds the user's password hash (never the
// plaintext) via a quoted heredoc so the hash's own "$" characters survive
// byte-for-byte, and installs a trap that removes both the on-disk hash file
// and the script's own path on every exit path — success, early `set -e`
// failure, or signal — so neither secret-bearing artifact outlives the run
// (Phase 1 item A.2).
func renderAutonomousInstallerScript(p autonomousInstallerScriptParams) string {
	return fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
exec > >(tee -a /tmp/orchestrator_execution.log) 2>&1

# Resolve our own on-disk path once, up front, so the cleanup trap below can
# remove this script (which embeds the password hash) on every exit path —
# success, an early "set -e" failure, or a signal — not just the happy path.
SELF_PATH="$(readlink -f "$0" 2>/dev/null || echo "$0")"
cleanup() {
    rm -f /mnt/target/tmp/.orch_pwhash 2>/dev/null || true
    rm -f "$SELF_PATH" 2>/dev/null || true
}
trap cleanup EXIT

DISK="%[1]s"
HOST_PART="%[2]s"
TARGET_PART="%[3]s"
HOST_ESP="%[4]s"
HOST_PART_NUM="%[5]s"
TARGET_PART_NUM="%[6]s"
DISTRO_LABEL="%[7]s"
ORCH_USER="%[8]s"

swapoff -a || true
e2fsck -fy "$HOST_PART" || true

sgdisk -e "$DISK" || true
if [ ! -b "$TARGET_PART" ]; then
    parted -s -a optimal "$DISK" mkpart primary ext4 50%% 100%% || true
    sync
    partprobe "$DISK" || true
    udevadm settle --timeout=10 || true
fi

mkfs.ext4 -F -q -L "${DISTRO_LABEL}_ROOT" "$TARGET_PART"
mkdir -p /mnt/target /mnt/source /mnt/vault
mount "$TARGET_PART" /mnt/target

PAYLOAD_PATH=$(find / -name "os_image.payload" 2>/dev/null | head -n 1)
if [ -z "$PAYLOAD_PATH" ] || [ ! -f "$PAYLOAD_PATH" ]; then
    mkdir -p /mnt/vault
    mount -L ORCH_STORAGE /mnt/vault 2>/dev/null || true
    PAYLOAD_PATH="/mnt/vault/os_image.payload"
fi
mount -o loop,ro "$PAYLOAD_PATH" /mnt/source

SQUASH_FILE=""
for cand in /mnt/source/casper/filesystem.squashfs /mnt/source/live/filesystem.squashfs /mnt/source/install/filesystem.squashfs; do
    if [ -f "$cand" ]; then
        SQUASH_FILE="$cand"
        break
    fi
done
if [ -z "$SQUASH_FILE" ]; then
    SQUASH_FILE=$(find /mnt/source -name "*.squashfs" 2>/dev/null | head -n 1)
fi

if [ -n "$SQUASH_FILE" ] && [ -f "$SQUASH_FILE" ]; then
    unsquashfs -f -d /mnt/target "$SQUASH_FILE"
elif [ -d /mnt/source/live ]; then
    cp -a /mnt/source/live/* /mnt/target/
else
    rsync -aHAX --exclude="/proc" --exclude="/sys" --exclude="/dev" /mnt/source/ /mnt/target/
fi

tune2fs -m 1 "$TARGET_PART"
ln -sf /lib/systemd/systemd /mnt/target/sbin/init
UUID=$(blkid -s UUID -o value "$TARGET_PART" || true)
if [ -n "$UUID" ]; then
    echo "UUID=$UUID / ext4 defaults,noatime,errors=remount-ro 0 1" > /mnt/target/etc/fstab
fi

mkdir -p /mnt/target/etc/sudoers.d /mnt/target/tmp
echo "$ORCH_USER ALL=(ALL:ALL) ALL" > "/mnt/target/etc/sudoers.d/$ORCH_USER"
chmod 0440 "/mnt/target/etc/sudoers.d/$ORCH_USER"

# Written via a quoted heredoc (no expansion at all) so the hash's own "$"
# characters reach the file byte-for-byte regardless of shell quoting. Owner
# restricted to root (umask on this environment is 022 by default on most
# live images, so set the mode explicitly rather than assume it) and removed
# by the cleanup trap above on every exit path.
cat > /mnt/target/tmp/.orch_pwhash <<'ORCH_HASH_EOF'
%[9]s
ORCH_HASH_EOF
chmod 0600 /mnt/target/tmp/.orch_pwhash

mount --bind /dev /mnt/target/dev
mount --bind /proc /mnt/target/proc
mount --bind /sys /mnt/target/sys

chroot /mnt/target bash -c '
    HASH=$(cat /tmp/.orch_pwhash)
    rm -f /tmp/.orch_pwhash
    %[10]s
    id -u %[8]s >/dev/null 2>&1 || useradd -m -s /bin/bash -G sudo %[8]s
    echo "%[8]s:$HASH" | chpasswd -e
    rm -rf /home/%[8]s/Desktop/install* /home/%[8]s/Desktop/calamares* /home/%[8]s/Desktop/README*
    rm -rf /etc/skel/Desktop/install* /etc/skel/Desktop/calamares* /etc/skel/Desktop/README*
    rm -rf /root/Desktop/install* /root/Desktop/calamares* /root/Desktop/README*
    apt-get purge -y calamares calamares-settings-debian calamares-settings-parrot 2>/dev/null || true
    systemctl disable calamares.service 2>/dev/null || true
'
umount -l /mnt/target/dev /mnt/target/proc /mnt/target/sys 2>/dev/null || true

mkdir -p /mnt/target/boot/efi
mount "$HOST_ESP" /mnt/target/boot/efi 2>/dev/null || true
chroot /mnt/target bash -c "grub-install $DISK" || true
chroot /mnt/target bash -c "update-grub" || true
umount /mnt/target/boot/efi 2>/dev/null || true

mkdir -p /mnt/host
mount "$HOST_PART" /mnt/host
mkdir -p /mnt/host/boot/efi
mount "$HOST_ESP" /mnt/host/boot/efi 2>/dev/null || true
mount --bind /dev /mnt/host/dev
mount --bind /proc /mnt/host/proc
mount --bind /sys /mnt/host/sys

chroot /mnt/host bash -c "update-initramfs -u -k all" || true
chroot /mnt/host bash -c "update-grub" || true

umount /mnt/host/boot/efi 2>/dev/null || true
umount -l /mnt/host/dev /mnt/host/proc /mnt/host/sys /mnt/host 2>/dev/null || true

BOOTNUM=$(efibootmgr | grep ORCH_AUTONOMOUS_INSTALLER | head -n1 | sed 's/Boot\([0-9A-Fa-f]*\).*/\1/' | tr -d '*')
[ -n "$BOOTNUM" ] && efibootmgr -b "$BOOTNUM" -B || true

sync
umount /mnt/source 2>/dev/null || true
umount /mnt/target 2>/dev/null || true
sleep 2
reboot -f
`, p.Disk, p.HostPartDev, p.TargetPartDev, p.HostESP, p.HostPartNum, p.TargetPartNum, p.DistroLabel, p.Username, p.PasswordHash, p.RootCmd)
}

// transientScriptMode is the permission transient_resize.sh is written with.
// It embeds the user's password hash in its own text (see
// renderAutonomousInstallerScript), so it must never be group/world
// readable (Phase 1 item A.2) — owner read/write/execute only.
const transientScriptMode os.FileMode = 0700

// writeScriptSecurely writes content to path with transientScriptMode,
// syncing before close so the bytes are durable before the caller verifies
// or arms a bootloader against them. Separated from its caller so the
// permission bits are unit-testable without any of the real USB-mount
// fallback logic around it.
func writeScriptSecurely(path, content string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_SYNC, transientScriptMode)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
