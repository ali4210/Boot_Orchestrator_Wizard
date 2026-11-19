package engine

import (
	"fmt"
	"os"
	"strings"
)

// dangerousStorageTypes maps blkid's TYPE value to a human-readable label
// for storage layouts this tool has no support for and must never write to
// blind. blkid (util-linux >= 2.33) detects BitLocker as TYPE=BitLocker
// alongside the usual filesystem types, so a single blkid probe per device
// covers LVM, LUKS, mdraid and BitLocker without needing lvm2/cryptsetup/
// mdadm to be installed on the host running this tool.
var dangerousStorageTypes = map[string]string{
	"LVM2_member":       "an LVM physical volume",
	"crypto_LUKS":       "a LUKS encrypted volume",
	"linux_raid_member": "a Linux software RAID (mdraid) member",
	"BitLocker":         "a BitLocker-encrypted volume",
}

// listDiskAndPartitions returns diskDevice itself plus every partition
// lsblk reports under it (e.g. "/dev/sdz" -> ["/dev/sdz", "/dev/sdz1",
// "/dev/sdz2", ...]), via the Runner so it is mockable.
func listDiskAndPartitions(diskDevice string) ([]string, error) {
	out, err := cmdRunner.RunStdout("lsblk", "-ln", "-o", "PATH", diskDevice)
	if err != nil {
		return nil, fmt.Errorf("listing partitions of %s: %w", diskDevice, err)
	}
	var devices []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			devices = append(devices, line)
		}
	}
	if len(devices) == 0 {
		devices = []string{diskDevice}
	}
	return devices, nil
}

// detectDangerousStorage probes each device in devices with blkid and
// reports the first LVM/LUKS/mdraid/BitLocker match found, if any.
//
// Follow-up F7: this used to treat ANY blkid error (wrong exit code, the
// binary missing entirely, a permission error, a device I/O error, ...) as
// "couldn't classify, not dangerous, keep going" — which is fail-OPEN, the
// opposite of what a safety gate should do. Real blkid's exit code 2 means
// specifically "no recognizable superblock/filesystem found" (e.g. an
// unformatted partition) — a normal, common, genuinely non-dangerous case.
// Every other non-zero exit (bad usage, ambiguous probe, I/O error) or a
// failure to even run blkid at all (binary missing: exitCodeOf reports -1
// for a non-*exec.ExitError) now aborts instead: this check cannot prove
// the device is safe, so it fails closed rather than silently continuing
// past a disk it couldn't actually inspect.
//
// Uses `blkid -p` (force a live low-level superblock probe) rather than
// plain `blkid`, which can answer from blkid's on-disk cache
// (/run/blkid/blkid.tab) and so could report a stale type for a disk that
// has since been reformatted or encrypted.
func detectDangerousStorage(devices []string) (found bool, reason string, err error) {
	for _, dev := range devices {
		out, runErr := cmdRunner.RunStdout("blkid", "-p", "-o", "value", "-s", "TYPE", dev)
		if runErr != nil {
			if exitCodeOf(runErr) == 2 {
				// No recognizable filesystem — not dangerous, not an
				// inspection failure. Keep checking the remaining devices.
				continue
			}
			return false, "", fmt.Errorf("could not safely inspect %s with blkid (exit code %d): %w", dev, exitCodeOf(runErr), runErr)
		}
		t := strings.TrimSpace(string(out))
		if label, bad := dangerousStorageTypes[t]; bad {
			return true, fmt.Sprintf("%s detected on %s (blkid TYPE=%s)", label, dev, t), nil
		}
	}
	return false, "", nil
}

// PreflightStorageSafetyCheck must be called before any partition-table or
// filesystem write touches diskDevice. It aborts (returns a non-nil error,
// having written nothing) if LVM, LUKS, mdraid, or BitLocker is detected
// anywhere on diskDevice or any of its partitions — this tool has no
// support for any of them, and partitioning or formatting blind on top of
// one risks unrecoverable data loss (Phase 1 item B.5). It also aborts
// (fails closed) if any device could not be safely inspected at all
// (follow-up F7), rather than assuming "inspection failed" means "safe".
func PreflightStorageSafetyCheck(diskDevice string) error {
	if diskDevice == "" {
		return fmt.Errorf("refusing to run the storage safety preflight: no target disk specified")
	}
	devices, err := listDiskAndPartitions(diskDevice)
	if err != nil {
		return err
	}
	found, reason, err := detectDangerousStorage(devices)
	if err != nil {
		return fmt.Errorf("refusing to modify %s: %w; nothing was written", diskDevice, err)
	}
	if found {
		return fmt.Errorf("refusing to modify %s: %s — this tool does not support LVM, LUKS, mdraid, or BitLocker; nothing was written", diskDevice, reason)
	}
	return nil
}

// DetectSecureBootAndTPM reports whether Secure Boot is enabled (via
// `mokutil --sb-state`) and whether a TPM device is present
// (/sys/class/tpm/tpm0), along with human-readable warnings for either
// condition. Neither condition aborts anything — both are advisory: this
// tool does not yet sign its own bootloader chain for Secure Boot, and does
// not interact with the TPM/BitLocker-style sealing a target OS might set up
// on its own, so the caller should surface these as warnings, not failures.
func DetectSecureBootAndTPM() (secureBootOn bool, tpmPresent bool, warnings []string) {
	if out, err := cmdRunner.RunStdout("mokutil", "--sb-state"); err == nil {
		if strings.Contains(strings.ToLower(string(out)), "enabled") {
			secureBootOn = true
		}
	}
	if _, statErr := os.Stat("/sys/class/tpm/tpm0"); statErr == nil {
		tpmPresent = true
	}

	if secureBootOn {
		warnings = append(warnings, "Secure Boot is ENABLED on this machine. This tool does not sign the bootloaders it installs; the newly installed OS may fail to boot until Secure Boot is disabled or the installed bootloader is enrolled with mokutil.")
	}
	if tpmPresent {
		warnings = append(warnings, "A TPM device was detected (/sys/class/tpm/tpm0). If the existing OS uses TPM-sealed disk encryption (e.g. BitLocker in its default mode), partition changes made by this tool can trigger recovery-key prompts or break the seal.")
	}
	return secureBootOn, tpmPresent, warnings
}
