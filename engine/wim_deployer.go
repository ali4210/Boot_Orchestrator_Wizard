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
	xmlOut, err := exec.Command("wimlib-imagex", "info", wimPath, "--extract-xml").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("wimlib-imagex info XML %s failed: %w\n%s", wimPath, err, string(xmlOut))
	}
	return parseWimXML(xmlOut)
}

func listWimIndexesDism(wimPath string) ([]WimIndex, error) {
	out, err := exec.Command("dism.exe", "/Get-WimInfo", "/WimFile:"+wimPath).CombinedOutput()
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
		cmd := exec.Command("dism.exe",
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
		if out, err := exec.Command("mkfs.ntfs", "-Q", "-F", plan.TargetPartition).CombinedOutput(); err != nil {
			return fmt.Errorf("formatting %s as NTFS failed: %w\n%s", plan.TargetPartition, err, string(out))
		}

		// 2. Create isolated mount point
		mountPoint, err := os.MkdirTemp("", "orchestrator-ntfs-*")
		if err != nil {
			return fmt.Errorf("creating temporary mountpoint: %w", err)
		}
		defer os.RemoveAll(mountPoint)

		// 3. Mount NTFS target partition
		if out, err := exec.Command("mount", "-t", "ntfs-3g", plan.TargetPartition, mountPoint).CombinedOutput(); err != nil {
			// Fallback to standard mount if ntfs-3g alias is standard
			if fbOut, fbErr := exec.Command("mount", plan.TargetPartition, mountPoint).CombinedOutput(); fbErr != nil {
				return fmt.Errorf("mounting NTFS partition failed: %s (fallback: %s: %w)", string(out), string(fbOut), fbErr)
			}
		}

		defer func() {
			_ = exec.Command("sync").Run()
			if out, err := exec.Command("umount", mountPoint).CombinedOutput(); err != nil {
				_ = exec.Command("umount", "-l", mountPoint).Run()
				_ = out
			}
		}()

		// 4. Apply chosen WIM index onto mounted NTFS target
		applyCmd := exec.Command("wimlib-imagex", "apply", plan.WimPath, strconv.Itoa(plan.Index), mountPoint)
		if out, err := applyCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("wimlib-imagex extraction to %s failed: %w\n%s", mountPoint, err, string(out))
		}

		plan.MountPoint = mountPoint
	}

	// 5. Inject Unattended Answer File if configured
	if plan.AutounattendSrc != "" && plan.MountPoint != "" {
		if err := injectAutounattend(plan.AutounattendSrc, plan.MountPoint); err != nil {
			return fmt.Errorf("autounattend injection failed: %w", err)
		}
	}

	return nil
}

func injectAutounattend(srcPath, targetRoot string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("reading answer file %s: %w", srcPath, err)
	}
	if err := xml.Unmarshal(data, new(struct{ XMLName xml.Name })); err != nil {
		return fmt.Errorf("malformed XML answer file %s: %w", srcPath, err)
	}
	destPath := filepath.Join(targetRoot, "autounattend.xml")
	return os.WriteFile(destPath, data, 0644)
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
