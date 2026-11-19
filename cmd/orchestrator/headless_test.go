package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"boot-orchestrator/discovery"
	"boot-orchestrator/hypervisor"
)

// withFakeBlockDevice overrides isBlockDevice so tests can treat an
// arbitrary path as a recognized block device without needing a real one
// on the test machine.
func withFakeBlockDevice(t *testing.T, recognizedPaths ...string) {
	t.Helper()
	orig := isBlockDevice
	t.Cleanup(func() { isBlockDevice = orig })
	isBlockDevice = func(path string) bool {
		for _, p := range recognizedPaths {
			if p == path {
				return true
			}
		}
		return false
	}
}

func validHeadlessConfig(t *testing.T, passwordFileContent string) headlessConfig {
	t.Helper()
	pwFile := filepath.Join(t.TempDir(), "pw.txt")
	if err := os.WriteFile(pwFile, []byte(passwordFileContent), 0600); err != nil {
		t.Fatalf("writing password file: %v", err)
	}
	return headlessConfig{
		OSID:         "ubuntu-24.04-amd64",
		TargetDisk:   "/dev/sdz",
		Username:     "janedoe",
		PasswordFile: pwFile,
		Yes:          true,
	}
}

// TestHeadlessConfig_Validate is the regression test for Phase 1 item C.7's
// "refuse without --yes" requirement, plus every other required flag.
func TestHeadlessConfig_Validate(t *testing.T) {
	base := validHeadlessConfig(t, "a-fine-password")

	cases := []struct {
		name    string
		mutate  func(c *headlessConfig)
		wantErr string
	}{
		{"complete config passes", func(c *headlessConfig) {}, ""},
		{"missing target disk", func(c *headlessConfig) { c.TargetDisk = "" }, "target-disk"},
		{"missing username", func(c *headlessConfig) { c.Username = "" }, "username"},
		{"missing password file", func(c *headlessConfig) { c.PasswordFile = "" }, "password-file"},
		{"missing os selection", func(c *headlessConfig) { c.OSID = ""; c.ISOPath = "" }, "os-id"},
		{"both os-id and iso-path", func(c *headlessConfig) { c.ISOPath = "/tmp/x.iso" }, "mutually exclusive"},
		{"missing --yes", func(c *headlessConfig) { c.Yes = false }, "--yes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			err := cfg.validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected a valid config to pass, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error to contain %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

// TestReadPasswordFile_StripsExactlyOneTrailingNewline proves a password
// file saved by a text editor (which appends a trailing newline) doesn't
// silently make the newline part of the password, while internal
// whitespace is preserved verbatim.
func TestReadPasswordFile_StripsExactlyOneTrailingNewline(t *testing.T) {
	cases := map[string]string{
		"hunter2\n":        "hunter2",
		"hunter2\r\n":      "hunter2",
		"hunter2":          "hunter2",
		"  spaced out  \n": "  spaced out  ",
		"multi\nline\n":    "multi\nline",
	}
	for input, want := range cases {
		path := filepath.Join(t.TempDir(), "pw.txt")
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatalf("writing fixture: %v", err)
		}
		got, err := readPasswordFile(path)
		if err != nil {
			t.Fatalf("readPasswordFile: %v", err)
		}
		if got != want {
			t.Errorf("readPasswordFile(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestReadPasswordFile_MissingFile(t *testing.T) {
	if _, err := readPasswordFile(filepath.Join(t.TempDir(), "nope.txt")); err == nil {
		t.Fatalf("expected an error for a missing password file")
	}
}

// TestResolveHeadlessTarget_DualBootWhenTargetIsActiveDisk proves headless
// mode picks up the host partition/target partition from
// engine.DetectActiveRootDisk when --target-disk matches the live root
// disk, exactly like the TUI's startProvisioning does.
func TestResolveHeadlessTarget_DualBootWhenTargetIsActiveDisk(t *testing.T) {
	mode, hostNum, targetPart := resolveHeadlessTarget("/dev/sda", "/dev/sda", "1", "/dev/sda2")
	if mode != "dual-boot" {
		t.Errorf("expected dual-boot mode, got %q", mode)
	}
	if hostNum != "1" || targetPart != "/dev/sda2" {
		t.Errorf("expected host/target partitions to be carried over, got hostNum=%q targetPart=%q", hostNum, targetPart)
	}
}

func TestResolveHeadlessTarget_SingleBootWhenTargetIsNotActiveDisk(t *testing.T) {
	mode, hostNum, targetPart := resolveHeadlessTarget("/dev/sdb", "/dev/sda", "1", "/dev/sda2")
	if mode != "single-boot" {
		t.Errorf("expected single-boot mode, got %q", mode)
	}
	if hostNum != "" || targetPart != "" {
		t.Errorf("expected no host/target partition carryover for a non-active disk, got hostNum=%q targetPart=%q", hostNum, targetPart)
	}
}

// TestBuildHeadlessRequest_RejectsInvalidUsername proves buildHeadlessRequest
// enforces the same engine.ValidUsername rule the TUI's credentials screen
// does (Phase 1 items A.4/C.7: same engine path, same safety gates).
func TestBuildHeadlessRequest_RejectsInvalidUsername(t *testing.T) {
	cfg := validHeadlessConfig(t, "a-fine-password")
	cfg.Username = "root"

	_, err := buildHeadlessRequest(cfg, &discovery.Catalog{}, hypervisor.KindBareMetal, "", "", "")
	if err == nil {
		t.Fatalf("expected a reserved username to be rejected")
	}
}

// TestBuildHeadlessRequest_RejectsShortPassword proves the same for
// engine.ValidPassword.
func TestBuildHeadlessRequest_RejectsShortPassword(t *testing.T) {
	cfg := validHeadlessConfig(t, "short")

	_, err := buildHeadlessRequest(cfg, &discovery.Catalog{}, hypervisor.KindBareMetal, "", "", "")
	if err == nil {
		t.Fatalf("expected a too-short password to be rejected")
	}
}

// TestBuildHeadlessRequest_UnknownOSID proves a typo'd --os-id fails loudly
// instead of silently provisioning with an empty image URL.
func TestBuildHeadlessRequest_UnknownOSID(t *testing.T) {
	withFakeBlockDevice(t, "/dev/sdz")
	cfg := validHeadlessConfig(t, "a-fine-password")
	cfg.OSID = "does-not-exist"

	_, err := buildHeadlessRequest(cfg, &discovery.Catalog{}, hypervisor.KindBareMetal, "", "", "")
	if err == nil {
		t.Fatalf("expected an unknown --os-id to be rejected")
	}
}

// TestBuildHeadlessRequest_ResolvesCatalogEntry proves a matching --os-id
// produces a ProvisionRequest with the entry's mirrors, SHA256, and distro
// name, root locked (no AllowRootLogin flag exists in headless mode), and
// the account's password hashed rather than carried in plaintext.
func TestBuildHeadlessRequest_ResolvesCatalogEntry(t *testing.T) {
	withFakeBlockDevice(t, "/dev/sdz")
	cfg := validHeadlessConfig(t, "a-fine-password")
	catalog := &discovery.Catalog{Entries: []discovery.Entry{
		{
			ID:          "ubuntu-24.04-amd64",
			Distro:      "Ubuntu 24.04",
			Family:      "linux",
			DownloadURL: "https://example.test/ubuntu-24.04.iso",
			SHA256:      "deadbeef",
		},
	}}

	req, err := buildHeadlessRequest(cfg, catalog, hypervisor.KindBareMetal, "", "", "")
	if err != nil {
		t.Fatalf("buildHeadlessRequest: %v", err)
	}
	if req.ImageURL != "https://example.test/ubuntu-24.04.iso" {
		t.Errorf("unexpected ImageURL: %q", req.ImageURL)
	}
	if req.ImageSHA256 != "deadbeef" {
		t.Errorf("unexpected ImageSHA256: %q", req.ImageSHA256)
	}
	if req.Credentials.Username != "janedoe" {
		t.Errorf("unexpected username: %q", req.Credentials.Username)
	}
	if req.Credentials.Password != "" {
		t.Errorf("expected the plaintext password to never be carried in the request, got it set")
	}
	if req.Credentials.PasswordHash == "" || !strings.HasPrefix(req.Credentials.PasswordHash, "$6$") {
		t.Errorf("expected a SHA-512 crypt hash, got %q", req.Credentials.PasswordHash)
	}
	if req.Credentials.AllowRootLogin {
		t.Errorf("expected root to stay locked by default in headless mode")
	}
	if req.TargetDiskPath != "/dev/sdz" {
		t.Errorf("unexpected TargetDiskPath: %q", req.TargetDiskPath)
	}
}

// TestBuildHeadlessRequest_RefusesUnavailableCatalogEntry is the
// regression test for audit follow-up G1: gentoo-stage3-amd64 and both
// macOS recovery entries have confirmed-dead download URLs (see
// OS_STATUS_MATRIX.md) and are now marked Unavailable in the manifest.
// Headless mode must refuse outright, naming the reason, rather than
// attempting a download that's already known to 404/403.
func TestBuildHeadlessRequest_RefusesUnavailableCatalogEntry(t *testing.T) {
	withFakeBlockDevice(t, "/dev/sdz")
	cfg := validHeadlessConfig(t, "a-fine-password")
	cfg.OSID = "gentoo-stage3-amd64"
	catalog := &discovery.Catalog{Entries: []discovery.Entry{
		{
			ID:                "gentoo-stage3-amd64",
			Distro:            "Gentoo Linux",
			Family:            "linux",
			DownloadURL:       "https://distfiles.gentoo.org/dead.iso",
			Unavailable:       true,
			UnavailableReason: "both mirrors return HTTP 404",
		},
	}}

	_, err := buildHeadlessRequest(cfg, catalog, hypervisor.KindBareMetal, "", "", "")
	if err == nil {
		t.Fatalf("expected buildHeadlessRequest to refuse an Unavailable catalog entry")
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("expected the error to say the entry is unavailable, got: %v", err)
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("expected the error to carry the real UnavailableReason, got: %v", err)
	}
}

// TestBuildHeadlessRequest_RefusesRealAlpineEntry is
// TestBuildHeadlessRequest_RefusesUnavailableCatalogEntry's counterpart
// against the REAL embedded catalog (discovery.LoadEmbedded, the exact
// loader runHeadless itself uses — see headless.go) rather than a
// synthetic fixture: proves the actual alpine-linux-amd64 manifest entry,
// not just the general Unavailable mechanism, is refused in headless
// mode. alpine-linux-amd64 was marked Unavailable because
// OS_STATUS_MATRIX.md's real Tier 2/3 runs found its ISO incompatible
// with the autonomous installer (no squashfs, no /live directory).
func TestBuildHeadlessRequest_RefusesRealAlpineEntry(t *testing.T) {
	withFakeBlockDevice(t, "/dev/sdz")
	cfg := validHeadlessConfig(t, "a-fine-password")
	cfg.OSID = "alpine-linux-amd64"

	catalog, err := discovery.LoadEmbedded()
	if err != nil {
		t.Fatalf("discovery.LoadEmbedded: %v", err)
	}

	_, err = buildHeadlessRequest(cfg, catalog, hypervisor.KindBareMetal, "", "", "")
	if err == nil {
		t.Fatalf("expected buildHeadlessRequest to refuse the real alpine-linux-amd64 entry")
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("expected the error to say the entry is unavailable, got: %v", err)
	}
	if !strings.Contains(err.Error(), "incompatible with the autonomous installer") {
		t.Errorf("expected the error to carry the real UnavailableReason, got: %v", err)
	}
}

// TestBuildHeadlessRequest_IsoPathUsesLocalFile proves --iso-path routes
// the local file path straight into ImageURL (the same "local path" ingest
// mechanism engine/streamer.go already supports for absolute paths), with
// no catalog entry or SHA256 pinned.
func TestBuildHeadlessRequest_IsoPathUsesLocalFile(t *testing.T) {
	withFakeBlockDevice(t, "/dev/sdz")
	isoPath := filepath.Join(t.TempDir(), "custom.iso")
	if err := os.WriteFile(isoPath, []byte("fake-iso-bytes"), 0644); err != nil {
		t.Fatalf("writing fake iso: %v", err)
	}

	cfg := validHeadlessConfig(t, "a-fine-password")
	cfg.OSID = ""
	cfg.ISOPath = isoPath
	cfg.AllowUnverified = true // a direct --iso-path is always unverified; not what this test is about

	req, err := buildHeadlessRequest(cfg, &discovery.Catalog{}, hypervisor.KindBareMetal, "", "", "")
	if err != nil {
		t.Fatalf("buildHeadlessRequest: %v", err)
	}
	if req.ImageURL != isoPath {
		t.Errorf("expected ImageURL to be the local iso path, got %q", req.ImageURL)
	}
	if req.ImageSHA256 != "" {
		t.Errorf("expected no pinned checksum for an unverified local iso, got %q", req.ImageSHA256)
	}
}

// TestBuildHeadlessRequest_IsoPathMissingFile proves a nonexistent
// --iso-path fails loudly before any provisioning starts.
func TestBuildHeadlessRequest_IsoPathMissingFile(t *testing.T) {
	withFakeBlockDevice(t, "/dev/sdz")
	cfg := validHeadlessConfig(t, "a-fine-password")
	cfg.OSID = ""
	cfg.ISOPath = filepath.Join(t.TempDir(), "does-not-exist.iso")

	_, err := buildHeadlessRequest(cfg, &discovery.Catalog{}, hypervisor.KindBareMetal, "", "", "")
	if err == nil {
		t.Fatalf("expected a missing --iso-path file to be rejected")
	}
}

// TestBuildHeadlessRequest_RejectsNonBlockDeviceTarget is the regression
// test for follow-up F3: --target-disk must name a real block device, not
// an arbitrary path (a typo, a regular file, a symlink to nowhere).
func TestBuildHeadlessRequest_RejectsNonBlockDeviceTarget(t *testing.T) {
	// isBlockDevice is NOT stubbed here — /dev/sdz has no real
	// /sys/class/block entry on the test machine, which is exactly the
	// case this test wants to exercise.
	cfg := validHeadlessConfig(t, "a-fine-password")

	_, err := buildHeadlessRequest(cfg, &discovery.Catalog{}, hypervisor.KindBareMetal, "", "", "")
	if err == nil {
		t.Fatalf("expected a non-block-device --target-disk to be rejected")
	}
	if !strings.Contains(err.Error(), "block device") {
		t.Fatalf("expected the error to mention 'block device', got: %v", err)
	}
}

// TestBuildHeadlessRequest_RefusesLiveRootDiskInSingleBootMode is the
// regression test for follow-up F3's other safety gate: even if
// resolveHeadlessTarget somehow resolved single-boot mode for a disk that
// isLikelyLiveRootDisk (reading /proc/mounts directly) independently
// confirms is the live root disk, buildHeadlessRequest must refuse rather
// than proceed to wipe the system it's running on.
func TestBuildHeadlessRequest_RefusesLiveRootDiskInSingleBootMode(t *testing.T) {
	withFakeBlockDevice(t, "/dev/sdz")

	fakeMounts := filepath.Join(t.TempDir(), "mounts")
	if err := os.WriteFile(fakeMounts, []byte("/dev/sdz2 / ext4 rw,relatime 0 0\n"), 0644); err != nil {
		t.Fatalf("writing fake /proc/mounts: %v", err)
	}
	origMounts := procMountsPath
	t.Cleanup(func() { procMountsPath = origMounts })
	procMountsPath = fakeMounts

	isoPath := filepath.Join(t.TempDir(), "custom.iso")
	if err := os.WriteFile(isoPath, []byte("fake-iso-bytes"), 0644); err != nil {
		t.Fatalf("writing fake iso: %v", err)
	}
	cfg := validHeadlessConfig(t, "a-fine-password")
	cfg.OSID = ""
	cfg.ISOPath = isoPath

	// activeRootDisk deliberately left empty, simulating
	// engine.DetectActiveRootDisk failing to detect anything even though
	// /proc/mounts (read independently here) shows /dev/sdz2 mounted at
	// "/" — i.e. /dev/sdz genuinely is the live root disk.
	_, err := buildHeadlessRequest(cfg, &discovery.Catalog{}, hypervisor.KindBareMetal, "", "", "")
	if err == nil {
		t.Fatalf("expected provisioning the live root disk in single-boot mode to be refused")
	}
}

// TestIsLikelyLiveRootDisk covers the /proc/mounts cross-check directly.
func TestIsLikelyLiveRootDisk(t *testing.T) {
	fakeMounts := filepath.Join(t.TempDir(), "mounts")
	content := "/dev/sdz2 / ext4 rw,relatime 0 0\n/dev/sdz1 /boot/efi vfat rw 0 0\n"
	if err := os.WriteFile(fakeMounts, []byte(content), 0644); err != nil {
		t.Fatalf("writing fake /proc/mounts: %v", err)
	}
	origMounts := procMountsPath
	t.Cleanup(func() { procMountsPath = origMounts })
	procMountsPath = fakeMounts

	if !isLikelyLiveRootDisk("/dev/sdz") {
		t.Errorf("expected /dev/sdz to be detected as the live root disk (its partition /dev/sdz2 is mounted at /)")
	}
	if isLikelyLiveRootDisk("/dev/sdy") {
		t.Errorf("expected /dev/sdy (unrelated to any mount) to not be detected as the live root disk")
	}
	if isLikelyLiveRootDisk("") {
		t.Errorf("expected an empty disk path to never match")
	}
}

// TestIsLikelyLiveRootDisk_DoesNotMatchOnTextualPrefixAlone is the
// regression test for follow-up F8: the previous implementation matched
// any rootSrc with diskDevice as a string prefix, so diskDevice="/dev/sda"
// would wrongly match a root mounted on the entirely different disk
// "/dev/sdaa" (and diskDevice="/dev/sda1" would wrongly match the
// different partition "/dev/sda12"). Matching must now be exact, or via a
// genuine parent-disk relationship.
func TestIsLikelyLiveRootDisk_DoesNotMatchOnTextualPrefixAlone(t *testing.T) {
	fakeMounts := filepath.Join(t.TempDir(), "mounts")
	content := "/dev/sdaa1 / ext4 rw,relatime 0 0\n"
	if err := os.WriteFile(fakeMounts, []byte(content), 0644); err != nil {
		t.Fatalf("writing fake /proc/mounts: %v", err)
	}
	origMounts := procMountsPath
	t.Cleanup(func() { procMountsPath = origMounts })
	procMountsPath = fakeMounts

	if isLikelyLiveRootDisk("/dev/sda") {
		t.Fatalf("expected /dev/sda to NOT match a root mounted on the unrelated disk /dev/sdaa (sdaa1's parent disk is sdaa, not sda)")
	}

	// Sanity: the same fixture genuinely does match its real parent disk.
	if !isLikelyLiveRootDisk("/dev/sdaa") {
		t.Fatalf("expected /dev/sdaa to match its own partition /dev/sdaa1")
	}
}

// TestIsBlockDevice_RegularFileNamedLikeADiskIsRejected is the regression
// test for follow-up F8: a plain regular file (not a device node at all),
// even if named exactly like a disk ("sda"), must never be treated as a
// block device.
func TestIsBlockDevice_RegularFileNamedLikeADiskIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sda")
	if err := os.WriteFile(path, []byte("not a disk"), 0644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	if isBlockDevice(path) {
		t.Fatalf("expected a regular file named 'sda' to be rejected")
	}
}

// TestIsBlockDevice_ByIDSymlinkToRealBlockDeviceIsAccepted proves
// isBlockDevice resolves symlinks (as /dev/disk/by-id/... entries are)
// before checking the device-type bits, using /dev/loop0 — a real block
// device already present on any Linux system with the loop module loaded
// (this project's own prescribed safe-testing mechanism) — as the
// resolution target. Read-only: never opens, mounts, or writes to it.
func TestIsBlockDevice_ByIDSymlinkToRealBlockDeviceIsAccepted(t *testing.T) {
	const realBlockDevice = "/dev/loop0"
	if fi, err := os.Stat(realBlockDevice); err != nil || fi.Mode()&os.ModeDevice == 0 {
		t.Skipf("%s not available as a block device on this machine, skipping", realBlockDevice)
	}

	byIDSymlink := filepath.Join(t.TempDir(), "by-id-fake-disk")
	if err := os.Symlink(realBlockDevice, byIDSymlink); err != nil {
		t.Fatalf("creating fake by-id symlink: %v", err)
	}

	if !isBlockDevice(byIDSymlink) {
		t.Fatalf("expected a symlink resolving to a real block device (%s) to be accepted", realBlockDevice)
	}
}

// TestIsBlockDevice_CharDeviceIsRejected proves the ModeCharDevice
// exclusion works: /dev/null is a real device node (ModeDevice is set)
// but a character device, not a block device, and must be rejected.
func TestIsBlockDevice_CharDeviceIsRejected(t *testing.T) {
	if fi, err := os.Stat("/dev/null"); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		t.Skip("/dev/null is not available as a character device on this machine, skipping")
	}
	if isBlockDevice("/dev/null") {
		t.Fatalf("expected /dev/null (a character device) to be rejected")
	}
}

// TestFindAutounattendNear_PrefersAlongsideThenParent proves the lookup
// checks next to the executable first, then the executable's parent
// directory — matching this project's own bin/ + discovery/ layout — and
// fails with a clear error when neither has the file.
func TestFindAutounattendNear_PrefersAlongsideThenParent(t *testing.T) {
	t.Run("alongside the executable", func(t *testing.T) {
		dir := t.TempDir()
		manifestDir := filepath.Join(dir, "discovery", "manifests")
		if err := os.MkdirAll(manifestDir, 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(manifestDir, "autounattend.xml"), []byte("<unattend/>"), 0644); err != nil {
			t.Fatalf("writing fixture: %v", err)
		}
		got, err := findAutounattendNear(filepath.Join(dir, "boot-orchestrator"))
		if err != nil {
			t.Fatalf("findAutounattendNear: %v", err)
		}
		if got != filepath.Join(manifestDir, "autounattend.xml") {
			t.Errorf("unexpected path: %q", got)
		}
	})

	t.Run("in the parent directory (bin/ layout)", func(t *testing.T) {
		root := t.TempDir()
		manifestDir := filepath.Join(root, "discovery", "manifests")
		if err := os.MkdirAll(manifestDir, 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(manifestDir, "autounattend.xml"), []byte("<unattend/>"), 0644); err != nil {
			t.Fatalf("writing fixture: %v", err)
		}
		binDir := filepath.Join(root, "bin")
		if err := os.MkdirAll(binDir, 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		got, err := findAutounattendNear(filepath.Join(binDir, "boot-orchestrator"))
		if err != nil {
			t.Fatalf("findAutounattendNear: %v", err)
		}
		if got != filepath.Join(manifestDir, "autounattend.xml") {
			t.Errorf("unexpected path: %q", got)
		}
	})

	t.Run("not found anywhere", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := findAutounattendNear(filepath.Join(dir, "boot-orchestrator")); err == nil {
			t.Fatalf("expected an error when autounattend.xml exists nowhere near the executable")
		}
	})
}

// TestBuildHeadlessRequest_WindowsTargetWithoutUnattendFailsLoudly is the
// regression test for follow-up F3's last requirement: a Windows catalog
// entry must never silently proceed with an empty AutounattendPath just
// because the file wasn't found relative to the current working directory
// (or anywhere else) — it must fail loudly instead.
func TestBuildHeadlessRequest_WindowsTargetWithoutUnattendFailsLoudly(t *testing.T) {
	withFakeBlockDevice(t, "/dev/sdz")
	cfg := validHeadlessConfig(t, "a-fine-password")
	cfg.OSID = "windows-11-amd64"
	catalog := &discovery.Catalog{Entries: []discovery.Entry{
		{
			ID:          "windows-11-amd64",
			Distro:      "Windows 11",
			Family:      "windows",
			DownloadURL: "https://example.test/win11.iso",
			SHA256:      "deadbeef", // not what this test is about; see the unverified-checksum gate tests instead
		},
	}}

	// Run from a scratch working directory with no discovery/manifests
	// anywhere nearby, so resolveAutounattendPath (which this test does not
	// stub) is guaranteed not to find one next to the test binary either —
	// this only proves the function fails loudly, not that it will on a
	// real install where the manifest ships alongside the binary.
	_, err := buildHeadlessRequest(cfg, catalog, hypervisor.KindBareMetal, "", "", "")
	if err == nil {
		t.Skip("autounattend.xml happened to be found near the test binary; the no-unattend-found path is covered directly by TestFindAutounattendNear_PrefersAlongsideThenParent instead")
	}
	if !strings.Contains(err.Error(), "autounattend") && !strings.Contains(err.Error(), "unattend") {
		t.Fatalf("expected the error to mention the missing unattend file, got: %v", err)
	}
}

// TestBuildHeadlessRequest_RefusesUnverifiedChecksumWithoutFlag is the
// regression test for audit follow-up F13: a catalog entry with no SHA256
// (the current state of every real manifest entry — see
// docs/MANIFEST_AUDIT.md) must be refused in headless mode unless
// --allow-unverified is explicitly passed.
func TestBuildHeadlessRequest_RefusesUnverifiedChecksumWithoutFlag(t *testing.T) {
	withFakeBlockDevice(t, "/dev/sdz")
	cfg := validHeadlessConfig(t, "a-fine-password")
	catalog := &discovery.Catalog{Entries: []discovery.Entry{
		{ID: "ubuntu-24.04-amd64", Distro: "Ubuntu 24.04", DownloadURL: "https://example.test/ubuntu.iso"},
	}}

	_, err := buildHeadlessRequest(cfg, catalog, hypervisor.KindBareMetal, "", "", "")
	if err == nil {
		t.Fatalf("expected a checksum-less entry to be refused without --allow-unverified")
	}
	if !strings.Contains(err.Error(), "checksum") || !strings.Contains(err.Error(), "allow-unverified") {
		t.Fatalf("expected the error to mention the checksum and --allow-unverified, got: %v", err)
	}
}

// TestBuildHeadlessRequest_AllowsUnverifiedChecksumWithFlag proves the
// opt-in actually works: the same checksum-less entry succeeds once
// --allow-unverified is set.
func TestBuildHeadlessRequest_AllowsUnverifiedChecksumWithFlag(t *testing.T) {
	withFakeBlockDevice(t, "/dev/sdz")
	cfg := validHeadlessConfig(t, "a-fine-password")
	cfg.AllowUnverified = true
	catalog := &discovery.Catalog{Entries: []discovery.Entry{
		{ID: "ubuntu-24.04-amd64", Distro: "Ubuntu 24.04", DownloadURL: "https://example.test/ubuntu.iso"},
	}}

	_, err := buildHeadlessRequest(cfg, catalog, hypervisor.KindBareMetal, "", "", "")
	if err != nil {
		t.Fatalf("expected --allow-unverified to permit a checksum-less entry, got: %v", err)
	}
}

// TestBuildHeadlessRequest_IsoPathRefusedWithoutAllowUnverified proves the
// gate also covers --iso-path (which can never have a catalog checksum at
// all), not just --os-id.
func TestBuildHeadlessRequest_IsoPathRefusedWithoutAllowUnverified(t *testing.T) {
	withFakeBlockDevice(t, "/dev/sdz")
	isoPath := filepath.Join(t.TempDir(), "custom.iso")
	if err := os.WriteFile(isoPath, []byte("fake-iso-bytes"), 0644); err != nil {
		t.Fatalf("writing fake iso: %v", err)
	}
	cfg := validHeadlessConfig(t, "a-fine-password")
	cfg.OSID = ""
	cfg.ISOPath = isoPath

	_, err := buildHeadlessRequest(cfg, &discovery.Catalog{}, hypervisor.KindBareMetal, "", "", "")
	if err == nil {
		t.Fatalf("expected a direct --iso-path (always unverified) to be refused without --allow-unverified")
	}
}
