// Package engine — wim_deployer.go implements the "Windows WIM/ESD
// deployment & autounattend injector" from the blueprint. Prefers DISM on
// Windows hosts (native, most reliable) and falls back to wimlib-imagex
// (works cross-platform, useful when preparing a Windows install from a
// Linux host) — the caller picks based on runtime.GOOS or tool availability.
package engine

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// WimIndex describes one selectable image inside an install.wim/install.esd
// (e.g. "Windows 11 Pro", "Windows 11 Home") as reported by DISM/wimlib.
type WimIndex struct {
	Index       int
	Name        string
	Description string
	SizeBytes   uint64
}

// ListWimIndexes enumerates the editions available inside a WIM/ESD so the
// TUI can present a picker rather than guessing index 1.
func ListWimIndexes(wimPath string) ([]WimIndex, error) {
	if _, err := exec.LookPath("wimlib-imagex"); err == nil {
		return listWimIndexesWimlib(wimPath)
	}
	if _, err := exec.LookPath("dism.exe"); err == nil {
		return listWimIndexesDism(wimPath)
	}
	return nil, fmt.Errorf("neither wimlib-imagex nor dism.exe found on PATH — install wimlib-imagex (cross-platform) or run on Windows with DISM available")
}

func listWimIndexesWimlib(wimPath string) ([]WimIndex, error) {
	out, err := exec.Command("wimlib-imagex", "info", wimPath).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("wimlib-imagex info %s: %w\n%s", wimPath, err, out)
	}
	// wimlib's plain-text `info` output isn't machine-friendly; prefer the
	// XML variant for reliable parsing.
	xmlOut, err := exec.Command("wimlib-imagex", "info", wimPath, "--extract-xml", "/dev/stdout").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("wimlib-imagex info --extract-xml %s: %w\n%s", wimPath, err, xmlOut)
	}
	return parseWimXML(xmlOut)
}

func listWimIndexesDism(wimPath string) ([]WimIndex, error) {
	out, err := exec.Command("dism.exe", "/Get-WimInfo", "/WimFile:"+wimPath).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("dism /Get-WimInfo %s: %w\n%s", wimPath, err, out)
	}
	return parseDismWimInfoText(string(out))
}

// wimXMLDoc mirrors the small subset of WIM.xml we actually need.
type wimXMLDoc struct {
	Images []wimXMLImage `xml:"IMAGE"`
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

// parseDismWimInfoText is a best-effort parser for DISM's human-oriented
// text output (DISM has no clean machine-readable mode for this command).
// If DISM's output format ever changes across Windows builds, this is the
// first place to check when index parsing breaks.
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
	if len(results) == 0 {
		return nil, fmt.Errorf("could not parse any image index from DISM output — DISM output format may have changed")
	}
	return results, nil
}

// ApplyWimPlan describes applying one WIM index onto a target partition.
type ApplyWimPlan struct {
	WimPath         string
	Index           int
	TargetPartition string
	MountPoint      string // Windows drive letter (e.g. "D:") or Linux mountpoint
	AutounattendSrc string // path to an autounattend.xml to inject; empty = skip
}

// ApplyWim applies the chosen WIM index to the target and, if provided,
// copies autounattend.xml to the root of the applied volume so Windows
// Setup picks it up unattended on first boot.
//
// This must run against an already-formatted NTFS partition — formatting
// is a separate, explicit step (not folded in here) because NTFS format
// options (cluster size, GPT vs MBR alignment) are UEFI-boot-critical and
// deserve their own reviewable step in the journal.
func ApplyWim(plan *ApplyWimPlan) error {
	if _, err := os.Stat(plan.WimPath); err != nil {
		return fmt.Errorf("WIM file not found: %w", err)
	}

	if _, err := exec.LookPath("wimlib-imagex"); err == nil {
		out, err := exec.Command("wimlib-imagex", "apply", plan.WimPath,
			strconv.Itoa(plan.Index), plan.TargetPartition).CombinedOutput()
		if err != nil {
			return fmt.Errorf("wimlib-imagex apply failed: %w\n%s", err, out)
		}
	} else if _, err := exec.LookPath("dism.exe"); err == nil {
		out, err := exec.Command("dism.exe",
			"/Apply-Image",
			"/ImageFile:"+plan.WimPath,
			"/Index:"+strconv.Itoa(plan.Index),
			"/ApplyDir:"+plan.MountPoint,
		).CombinedOutput()
		if err != nil {
			return fmt.Errorf("dism /Apply-Image failed: %w\n%s", err, out)
		}
	} else {
		return fmt.Errorf("neither wimlib-imagex nor dism.exe available to apply the image")
	}

	if plan.AutounattendSrc != "" {
		if err := injectAutounattend(plan.AutounattendSrc, plan.MountPoint); err != nil {
			return fmt.Errorf("image applied successfully but autounattend injection failed (Setup will run interactively instead): %w", err)
		}
	}

	return nil
}

// injectAutounattend copies the answer file to the root of the applied
// volume. Windows Setup automatically discovers \autounattend.xml at the
// root of any attached fixed drive, so no registry/BCD editing is needed
// for this — just the file placement.
func injectAutounattend(srcPath, targetRoot string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("reading autounattend source %s: %w", srcPath, err)
	}
	// Sanity-check it's at least well-formed XML before writing it —
	// a malformed answer file can cause Setup to fail confusingly deep
	// into an unattended run with no way to intervene.
	if err := xml.Unmarshal(data, new(struct{ XMLName xml.Name })); err != nil {
		return fmt.Errorf("%s is not well-formed XML, refusing to inject it: %w", srcPath, err)
	}
	destPath := filepath.Join(targetRoot, "autounattend.xml")
	if err := os.WriteFile(destPath, data, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", destPath, err)
	}
	return nil
}

// --- tiny string helpers (kept local to avoid adding a dependency for a
// few one-off text ops) ------------------------------------------------

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i, r := range s {
		if r == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func hasPrefixFold(s, prefix string) bool {
	trimmed := trimLeftSpace(s)
	if len(trimmed) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		a, b := trimmed[i], prefix[i]
		if a >= 'A' && a <= 'Z' {
			a += 'a' - 'A'
		}
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}

func trimLeftSpace(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[i:]
}

func trimAfterColon(s string) string {
	idx := -1
	for i, r := range s {
		if r == ':' {
			idx = i
			break
		}
	}
	if idx == -1 {
		return ""
	}
	return trimLeftSpace(trimLeftSpace(s[idx+1:]))
}
