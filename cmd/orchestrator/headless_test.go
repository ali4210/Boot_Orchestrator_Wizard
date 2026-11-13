package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"boot-orchestrator/discovery"
	"boot-orchestrator/hypervisor"
)

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

// TestBuildHeadlessRequest_IsoPathUsesLocalFile proves --iso-path routes
// the local file path straight into ImageURL (the same "local path" ingest
// mechanism engine/streamer.go already supports for absolute paths), with
// no catalog entry or SHA256 pinned.
func TestBuildHeadlessRequest_IsoPathUsesLocalFile(t *testing.T) {
	isoPath := filepath.Join(t.TempDir(), "custom.iso")
	if err := os.WriteFile(isoPath, []byte("fake-iso-bytes"), 0644); err != nil {
		t.Fatalf("writing fake iso: %v", err)
	}

	cfg := validHeadlessConfig(t, "a-fine-password")
	cfg.OSID = ""
	cfg.ISOPath = isoPath

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
	cfg := validHeadlessConfig(t, "a-fine-password")
	cfg.OSID = ""
	cfg.ISOPath = filepath.Join(t.TempDir(), "does-not-exist.iso")

	_, err := buildHeadlessRequest(cfg, &discovery.Catalog{}, hypervisor.KindBareMetal, "", "", "")
	if err == nil {
		t.Fatalf("expected a missing --iso-path file to be rejected")
	}
}
