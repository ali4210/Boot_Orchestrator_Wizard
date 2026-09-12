package engine

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

// GetRealUserHome resolves the genuine home path of the logged-in user rather than root.
func GetRealUserHome() string {
	sudoUser := os.Getenv("SUDO_USER")
	if sudoUser != "" && sudoUser != "root" {
		userHome := filepath.Join("/home", sudoUser)
		if _, err := os.Stat(userHome); err == nil {
			return userHome
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "/root" {
		return home
	}
	if _, err := os.Stat("/home/kali"); err == nil {
		return "/home/kali"
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
	_ = exec.Command("xhost", "+local:").Run()

	cmdArgs := []string{"-u", sudoUser, "env", fmt.Sprintf("DISPLAY=%s", disp), fmt.Sprintf("XAUTHORITY=%s", xauth), "zenity", "--file-selection", "--title=" + title}
	if isDirectory {
		cmdArgs = append(cmdArgs, "--directory")
	} else {
		cmdArgs = append(cmdArgs, "--file-filter=All Bootable & Compressed Archives | *.iso *.img *.raw *.bin *.zip *.xz *.gz *.zst *.7z *.bz2 *.ISO *.IMG *.RAW *.BIN *.ZIP *.XZ *.GZ *.ZST *.7Z *.BZ2")
	}

	cmd := exec.Command("sudo", cmdArgs...)
	out, err := cmd.Output()
	if err == nil && len(bytes.TrimSpace(out)) > 0 {
		return strings.TrimSpace(string(out)), nil
	}

	return "", errors.New("gui dialog unavailable")
}
