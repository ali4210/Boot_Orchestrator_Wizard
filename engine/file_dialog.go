package engine

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

type FileItem struct {
	Name  string
	Path  string
	IsDir bool
	Size  int64
	Ext   string
}

// IsSupportedPayload verifies whether a filename matches standard or compressed bootable archives.
func IsSupportedPayload(name string) bool {
	lower := strings.ToLower(name)

	// Dual-extension compressed tarballs
	if strings.HasSuffix(lower, ".tar.gz") ||
		strings.HasSuffix(lower, ".tar.xz") ||
		strings.HasSuffix(lower, ".tar.zst") ||
		strings.HasSuffix(lower, ".tar.bz2") {
		return true
	}

	// Single extensions
	ext := filepath.Ext(lower)
	switch ext {
	case ".iso", ".img", ".raw", ".bin",
		".zip", ".xz", ".gz", ".zst", ".7z", ".bz2",
		".qcow2", ".vmdk", ".vdi", ".vhdx":
		return true
	}
	return false
}

// userLookupFunc is user.Lookup, as a var so a test can substitute a fake
// lookup (e.g. an account with a custom, non-/home/<name> home directory)
// without creating or deleting a real system user account.
var userLookupFunc = user.Lookup

// GetRealUserHome resolves the genuine home path of the logged-in user
// rather than root.
//
// Bug H's investigation: this used to guess the home directory as
// filepath.Join("/home", sudoUser) — correct for the common default
// layout, but wrong for any account whose home isn't literally at
// /home/<username> (a custom useradd -d, an NSS/LDAP-provided home
// elsewhere, ...). When that guess missed, this fell through to
// os.UserHomeDir() — which, running under sudo, resolves to root's own
// home and was explicitly excluded — and then to a hardcoded "/home/kali"
// fallback that was clearly a dev-testing leftover, not something
// meaningful for any other user, and finally to "/" (the filesystem
// root) if even that didn't exist: a surprising, unhelpful place for an
// ISO/image picker to land. Now resolves via os/user.Lookup, which reads
// the real passwd/NSS database entry for SUDO_USER — authoritative,
// not a guess, regardless of where that account's home actually lives.
func GetRealUserHome() string {
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" && sudoUser != "root" {
		if u, err := userLookupFunc(sudoUser); err == nil && u.HomeDir != "" {
			if _, statErr := os.Stat(u.HomeDir); statErr == nil {
				return u.HomeDir
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "/root" {
		return home
	}
	return "/"
}

// ListDirectoryContents returns directories and supported payload archives.
func ListDirectoryContents(dirPath string, showAll bool) ([]FileItem, error) {
	if dirPath == "" || dirPath == "/root" {
		dirPath = GetRealUserHome()
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}

	var items []FileItem

	// Parent directory entry
	parent := filepath.Dir(dirPath)
	if parent != dirPath {
		items = append(items, FileItem{
			Name:  "..",
			Path:  parent,
			IsDir: true,
		})
	}

	// 1. Collect Directories First
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if entry.IsDir() {
			items = append(items, FileItem{
				Name:  name + "/",
				Path:  filepath.Join(dirPath, name),
				IsDir: true,
			})
		}
	}

	// 2. Collect Bootable & Compressed Archive Payloads
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || entry.IsDir() {
			continue
		}

		if showAll || IsSupportedPayload(name) {
			info, _ := entry.Info()
			size := int64(0)
			if info != nil {
				size = info.Size()
			}
			items = append(items, FileItem{
				Name:  name,
				Path:  filepath.Join(dirPath, name),
				IsDir: false,
				Size:  size,
				Ext:   strings.ToUpper(strings.TrimPrefix(filepath.Ext(name), ".")),
			})
		}
	}
	return items, nil
}

// PickFileOrDirectory attempts GUI dialog; returns error if GUI unavailable.
func PickFileOrDirectory(isDirectory bool, title string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", errors.New("unsupported platform")
	}

	sudoUser := os.Getenv("SUDO_USER")
	if sudoUser == "" {
		sudoUser = "kali"
	}

	disp := os.Getenv("DISPLAY")
	if disp == "" {
		disp = ":0"
	}

	xauth := fmt.Sprintf("/home/%s/.Xauthority", sudoUser)
	_ = execCommand("xhost", "+local:").Run()

	cmdArgs := []string{"-u", sudoUser, "env", fmt.Sprintf("DISPLAY=%s", disp), fmt.Sprintf("XAUTHORITY=%s", xauth), "zenity", "--file-selection", "--title=" + title}
	if isDirectory {
		cmdArgs = append(cmdArgs, "--directory")
	} else {
		cmdArgs = append(cmdArgs, "--file-filter=All Bootable & Compressed Archives | *.iso *.img *.raw *.bin *.zip *.xz *.gz *.zst *.7z *.bz2 *.ISO *.IMG *.RAW *.BIN *.ZIP *.XZ *.GZ *.ZST *.7Z *.BZ2")
	}

	cmd := execCommand("sudo", cmdArgs...)
	out, err := cmd.Output()
	if err == nil && len(bytes.TrimSpace(out)) > 0 {
		return strings.TrimSpace(string(out)), nil
	}

	return "", errors.New("gui dialog unavailable")
}
