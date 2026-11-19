// Package engine — wim_deployer.go implements the Windows WIM/ESD
// deployment, NTFS preparation, and autounattend injector with full Linux/Windows cross-support.
package engine

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"boot-orchestrator/safety"
)

type WimIndex struct {
	Index       int
	Name        string
	Description string
	SizeBytes   uint64
}

func ListWimIndexes(wimPath string) ([]WimIndex, error) {
	if _, err := exec.LookPath("wimlib-imagex"); err == nil {
		return listWimIndexesWimlib(wimPath)
	}
	if _, err := exec.LookPath("dism.exe"); err == nil {
		return listWimIndexesDism(wimPath)
	}
	return nil, fmt.Errorf("neither wimlib-imagex nor dism.exe found on PATH")
}

func listWimIndexesWimlib(wimPath string) ([]WimIndex, error) {
	xmlOut, err := execCommand("wimlib-imagex", "info", wimPath, "--extract-xml").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("wimlib-imagex info XML %s failed: %w\n%s", wimPath, err, string(xmlOut))
	}
	return parseWimXML(xmlOut)
}

func listWimIndexesDism(wimPath string) ([]WimIndex, error) {
	out, err := execCommand("dism.exe", "/Get-WimInfo", "/WimFile:"+wimPath).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("dism /Get-WimInfo %s: %w\n%s", wimPath, err, string(out))
	}
	return parseDismWimInfoText(string(out))
}

type wimXMLImage struct {
	Index int    `xml:"INDEX,attr"`
	Name  string `xml:"NAME"`
	Desc  string `xml:"DESCRIPTION"`
	Size  uint64 `xml:"TOTALBYTES"`
}

func parseWimXML(data []byte) ([]WimIndex, error) {
	var doc struct {
		XMLName xml.Name      `xml:"WIM"`
		Images  []wimXMLImage `xml:"IMAGE"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing WIM XML metadata: %w", err)
	}
	result := make([]WimIndex, 0, len(doc.Images))
	for _, img := range doc.Images {
		result = append(result, WimIndex{Index: img.Index, Name: img.Name, Description: img.Desc, SizeBytes: img.Size})
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no <IMAGE> entries found in WIM metadata")
	}
	return result, nil
}

func parseDismWimInfoText(text string) ([]WimIndex, error) {
	var results []WimIndex
	lines := splitLines(text)
	var cur WimIndex
	haveIndex := false
	for _, line := range lines {
		switch {
		case hasPrefixFold(line, "Index :"):
			if haveIndex {
				results = append(results, cur)
			}
			cur = WimIndex{}
			if v, err := strconv.Atoi(trimAfterColon(line)); err == nil {
				cur.Index = v
			}
			haveIndex = true
		case hasPrefixFold(line, "Name :"):
			cur.Name = trimAfterColon(line)
		case hasPrefixFold(line, "Description :"):
			cur.Description = trimAfterColon(line)
		}
	}
	if haveIndex {
		results = append(results, cur)
	}
	return results, nil
}

type ApplyWimPlan struct {
	WimPath         string
	Index           int
	TargetPartition string
	MountPoint      string
	AutounattendSrc string
	Credentials     Credentials
}

// ApplyWim autonomously prepares NTFS, mounts target, unpacks WIM/ESD, and injects answer file.
func ApplyWim(plan *ApplyWimPlan) error {
	if _, err := os.Stat(plan.WimPath); err != nil {
		return fmt.Errorf("WIM payload missing: %w", err)
	}

	if plan.Index <= 0 {
		plan.Index = 1
	}

	if runtime.GOOS == "windows" {
		// Native Windows DISM execution
		cmd := execCommand("dism.exe",
			"/Apply-Image",
			"/ImageFile:"+plan.WimPath,
			"/Index:"+strconv.Itoa(plan.Index),
			"/ApplyDir:"+plan.MountPoint,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("dism /Apply-Image failed: %w\n%s", err, string(out))
		}
	} else {
		// Linux/POSIX cross-platform execution via mkfs.ntfs & wimlib-imagex
		if _, err := exec.LookPath("mkfs.ntfs"); err != nil {
			return fmt.Errorf("required tool mkfs.ntfs (ntfs-3g) not found on PATH")
		}
		if _, err := exec.LookPath("wimlib-imagex"); err != nil {
			return fmt.Errorf("required tool wimlib-imagex not found on PATH")
		}

		// 1. Format target partition as quick NTFS
		if out, err := execCommand("mkfs.ntfs", "-Q", "-F", plan.TargetPartition).CombinedOutput(); err != nil {
			return fmt.Errorf("formatting %s as NTFS failed: %w\n%s", plan.TargetPartition, err, string(out))
		}

		// 2. Create isolated mount point
		mountPoint, err := os.MkdirTemp("", "orchestrator-ntfs-*")
		if err != nil {
			return fmt.Errorf("creating temporary mountpoint: %w", err)
		}
		defer fsRunner.RemoveAll(mountPoint)

		// 3. Mount NTFS target partition
		if out, err := execCommand("mount", "-t", "ntfs-3g", plan.TargetPartition, mountPoint).CombinedOutput(); err != nil {
			// Fallback to standard mount if ntfs-3g alias is standard
			if fbOut, fbErr := execCommand("mount", plan.TargetPartition, mountPoint).CombinedOutput(); fbErr != nil {
				return fmt.Errorf("mounting NTFS partition failed: %s (fallback: %s: %w)", string(out), string(fbOut), fbErr)
			}
		}

		defer func() {
			_ = execCommand("sync").Run()
			if out, err := execCommand("umount", mountPoint).CombinedOutput(); err != nil {
				_ = execCommand("umount", "-l", mountPoint).Run()
				_ = out
			}
		}()

		// 4. Apply chosen WIM index onto mounted NTFS target
		applyCmd := execCommand("wimlib-imagex", "apply", plan.WimPath, strconv.Itoa(plan.Index), mountPoint)
		if out, err := applyCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("wimlib-imagex extraction to %s failed: %w\n%s", mountPoint, err, string(out))
		}

		plan.MountPoint = mountPoint
	}

	// 5. Inject Unattended Answer File if configured
	if plan.AutounattendSrc != "" && plan.MountPoint != "" {
		if err := injectAutounattend(plan.AutounattendSrc, plan.MountPoint, plan.Credentials); err != nil {
			return fmt.Errorf("autounattend injection failed: %w", err)
		}
	}

	return nil
}

// injectAutounattend copies the answer file at srcPath onto the target,
// substituting the __ORCH_USERNAME__/__ORCH_PASSWORD__ placeholders (see
// discovery/manifests/autounattend.xml) with creds' actual values.
// Windows's unattend mechanism has no hashed-password option for local
// account creation, so creds.Password reaches this file as plaintext — that
// is inherent to the format (see Credentials' doc comment), not a choice
// made here; what audit 2.3 fixes is that the account is no longer a
// hardcoded "saleem" regardless of who runs the tool.
func injectAutounattend(srcPath, targetRoot string, creds Credentials) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("reading answer file %s: %w", srcPath, err)
	}
	content := string(data)
	content = strings.ReplaceAll(content, "__ORCH_USERNAME__", xmlEscape(creds.Username))
	content = strings.ReplaceAll(content, "__ORCH_PASSWORD__", xmlEscape(creds.Password))
	data = []byte(content)
	if err := xml.Unmarshal(data, new(struct{ XMLName xml.Name })); err != nil {
		return fmt.Errorf("malformed XML answer file %s: %w", srcPath, err)
	}
	pantherDir := filepath.Join(targetRoot, "Windows", "Panther")
	if err := fsRunner.MkdirAll(pantherDir, 0755); err != nil {
		return fmt.Errorf("creating Panther dir: %w", err)
	}
	destPath := filepath.Join(pantherDir, "unattend.xml")
	if err := fsRunner.WriteFile(destPath, data, 0644); err != nil {
		return err
	}

	// Audit follow-up (Windows CI, writeScriptSecurely investigation):
	// this file carries creds.Password in plaintext (inherent to the
	// unattend format, see above) — the real-world "equivalent" of
	// writeScriptSecurely's secret-bearing script, for the ApplyWim
	// native-Windows path (runtime.GOOS == "windows" above), which is
	// genuinely reachable and functional there (unlike the chroot/lsblk/
	// ext4 code elsewhere in this engine). Lock it down the same way
	// DefaultJournalPath's directory is locked down
	// (safety.RestrictToAdministrators, via icacls) for the real window
	// between writing it here and the guest's own first boot deleting it
	// (item 27's FirstLogonCommand) — targetRoot is a live-mounted drive
	// letter on the host during that window, not an inert archive.
	// No-op on non-Windows (ntfs-3g-mounted targetRoot there has no
	// equivalent host-side exposure in the same way, and POSIX file
	// modes already apply for whatever does exist).
	if runtime.GOOS == "windows" {
		if err := safety.RestrictToAdministrators(destPath); err != nil {
			return fmt.Errorf("restricting %s to Administrators/SYSTEM: %w", destPath, err)
		}
	}
	return nil
}

func splitLines(s string) []string {
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}

func hasPrefixFold(s, prefix string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), strings.ToLower(prefix))
}

func trimAfterColon(s string) string {
	idx := strings.Index(s, ":")
	if idx == -1 {
		return ""
	}
	return strings.TrimSpace(s[idx+1:])
}
