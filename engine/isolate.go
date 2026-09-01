// Package engine — isolate.go implements the "Container & Profile Isolator"
// from the blueprint: re-routes Docker's data-root (Linux) and the WSL2
// distro VHDX (Windows) off the system volume before/after a dual-boot
// resize, since an undersized root/C: volume is one of the most common
// causes of a "successful" dual-boot install running out of space days later.
package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DockerIsolationPlan describes moving Docker's data-root to targetPath.
type DockerIsolationPlan struct {
	DaemonJSONPath string
	OriginalRoot   string // empty if daemon.json had no explicit data-root (i.e. was using the default)
	TargetRoot     string
	BackupPath     string // where the original daemon.json is copied before editing
}

// dockerDaemonConfig is intentionally minimal — we only touch data-root and
// must preserve every other key untouched, so we decode into a generic map.
type dockerDaemonConfig map[string]json.RawMessage

// PlanDockerIsolation reads the existing daemon.json (if any) and prepares
// a plan to relocate data-root to targetPath. It does not write anything —
// call ApplyDockerIsolation to actually perform the move, so the caller can
// journal the step first.
func PlanDockerIsolation(daemonJSONPath, targetRoot string) (*DockerIsolationPlan, error) {
	plan := &DockerIsolationPlan{
		DaemonJSONPath: daemonJSONPath,
		TargetRoot:     targetRoot,
		BackupPath:     daemonJSONPath + ".pre-orchestrator.bak",
	}

	raw, err := os.ReadFile(daemonJSONPath)
	if err != nil {
		if os.IsNotExist(err) {
			// No existing config — Docker is using its compiled-in default
			// data-root (/var/lib/docker). That's fine, plan proceeds.
			return plan, nil
		}
		return nil, fmt.Errorf("reading %s: %w", daemonJSONPath, err)
	}

	var cfg dockerDaemonConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON, refusing to touch it: %w", daemonJSONPath, err)
	}
	if v, ok := cfg["data-root"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			plan.OriginalRoot = s
		}
	}
	return plan, nil
}

// ApplyDockerIsolation performs the actual data move + daemon.json rewrite.
// Returns rollback data (the original daemon.json bytes and original root
// path) that safety.Journal should store via Begin() before this is called.
//
// Sequencing matters here: Docker MUST be stopped before rsync'ing the data
// directory, or files will be copied mid-write and corrupt the image store.
func ApplyDockerIsolation(plan *DockerIsolationPlan) (rollbackData []byte, err error) {
	sourceRoot := plan.OriginalRoot
	if sourceRoot == "" {
		sourceRoot = "/var/lib/docker"
	}

	// 1. Snapshot rollback info before mutating anything.
	var originalDaemonJSON []byte
	if b, err := os.ReadFile(plan.DaemonJSONPath); err == nil {
		originalDaemonJSON = b
	}
	rb := struct {
		DaemonJSONPath     string `json:"daemon_json_path"`
		OriginalDaemonJSON []byte `json:"original_daemon_json"` // nil if file didn't exist
		OriginalRoot       string `json:"original_root"`
		MovedTo            string `json:"moved_to"`
	}{
		DaemonJSONPath:     plan.DaemonJSONPath,
		OriginalDaemonJSON: originalDaemonJSON,
		OriginalRoot:       sourceRoot,
		MovedTo:            plan.TargetRoot,
	}
	rollbackData, err = json.Marshal(rb)
	if err != nil {
		return nil, fmt.Errorf("marshal rollback data: %w", err)
	}

	// 2. Stop Docker. Required — do not rsync a live data-root.
	if out, err := exec.Command("systemctl", "stop", "docker").CombinedOutput(); err != nil {
		return rollbackData, fmt.Errorf("failed to stop docker service (aborting before any data move): %w\n%s", err, out)
	}

	// 3. Create target and copy data preserving ownership/perms/xattrs/hardlinks.
	if err := os.MkdirAll(plan.TargetRoot, 0711); err != nil {
		return rollbackData, fmt.Errorf("creating target root %s: %w", plan.TargetRoot, err)
	}
	rsyncArgs := []string{"-aHAX", "--info=progress2", strings.TrimRight(sourceRoot, "/") + "/", plan.TargetRoot + "/"}
	if out, err := exec.Command("rsync", rsyncArgs...).CombinedOutput(); err != nil {
		return rollbackData, fmt.Errorf("rsync of docker data-root failed (original data at %s is untouched): %w\n%s", sourceRoot, err, out)
	}

	// 4. Rewrite daemon.json with the new data-root, preserving all other keys.
	cfg := dockerDaemonConfig{}
	if len(originalDaemonJSON) > 0 {
		if err := json.Unmarshal(originalDaemonJSON, &cfg); err != nil {
			return rollbackData, fmt.Errorf("existing daemon.json became invalid unexpectedly: %w", err)
		}
	}
	newRootJSON, _ := json.Marshal(plan.TargetRoot)
	cfg["data-root"] = newRootJSON

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return rollbackData, fmt.Errorf("marshal updated daemon.json: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(plan.DaemonJSONPath), 0755); err != nil {
		return rollbackData, fmt.Errorf("creating dir for daemon.json: %w", err)
	}
	if err := os.WriteFile(plan.DaemonJSONPath, out, 0644); err != nil {
		return rollbackData, fmt.Errorf("writing updated daemon.json: %w", err)
	}

	// 5. Restart Docker on the new root.
	if out, err := exec.Command("systemctl", "start", "docker").CombinedOutput(); err != nil {
		return rollbackData, fmt.Errorf("docker failed to start on new data-root %s — old data at %s is still intact, rollback recommended: %w\n%s", plan.TargetRoot, sourceRoot, err, out)
	}

	return rollbackData, nil
}

// RollbackDockerIsolation reverses ApplyDockerIsolation using the data
// captured in rollbackData. Registered against safety.Journal under the
// step name "docker-isolation".
func RollbackDockerIsolation(rollbackData json.RawMessage) error {
	var rb struct {
		DaemonJSONPath     string `json:"daemon_json_path"`
		OriginalDaemonJSON []byte `json:"original_daemon_json"`
		OriginalRoot       string `json:"original_root"`
		MovedTo            string `json:"moved_to"`
	}
	if err := json.Unmarshal(rollbackData, &rb); err != nil {
		return fmt.Errorf("corrupt rollback data: %w", err)
	}

	exec.Command("systemctl", "stop", "docker").CombinedOutput()

	if len(rb.OriginalDaemonJSON) > 0 {
		if err := os.WriteFile(rb.DaemonJSONPath, rb.OriginalDaemonJSON, 0644); err != nil {
			return fmt.Errorf("restoring original daemon.json: %w", err)
		}
	} else {
		os.Remove(rb.DaemonJSONPath)
	}

	// Data at MovedTo is intentionally left in place rather than deleted —
	// deleting user data during a rollback is a bigger risk than leaving an
	// orphaned directory behind for the user to clean up manually.
	if out, err := exec.Command("systemctl", "start", "docker").CombinedOutput(); err != nil {
		return fmt.Errorf("docker failed to restart after rollback to %s: %w\n%s", rb.OriginalRoot, err, out)
	}
	return nil
}

// WSL2 VHDX relocation ------------------------------------------------------

// WSL2IsolationPlan describes moving a distro's ext4.vhdx to a new location
// and re-registering it, since `wsl --export` / `--import` is the only
// supported way to relocate a WSL2 distro (there is no in-place move).
type WSL2IsolationPlan struct {
	DistroName   string
	CurrentVHDX  string
	TargetDir    string
	ExportTarPath string
}

// PlanWSL2Isolation locates the distro's current VHDX via `wsl --manage` /
// registry lookup (left to the caller to supply, since reading the Windows
// registry from a cross-compiled Linux build isn't meaningful) and prepares
// export/import commands.
func PlanWSL2Isolation(distroName, currentVHDX, targetDir string) *WSL2IsolationPlan {
	return &WSL2IsolationPlan{
		DistroName:    distroName,
		CurrentVHDX:   currentVHDX,
		TargetDir:     targetDir,
		ExportTarPath: filepath.Join(os.TempDir(), distroName+"-orchestrator-export.tar"),
	}
}

// BuildWSL2IsolationCommands returns the exact PowerShell command sequence
// needed to relocate the distro. Returned as data (not executed) — this
// package is built without Windows-only APIs so it can be unit-tested on
// any platform; cmd/orchestrator wires this into safety.Journal and actually
// runs it only on a windows GOOS build.
func (p *WSL2IsolationPlan) BuildWSL2IsolationCommands() []string {
	return []string{
		fmt.Sprintf("wsl --shutdown"),
		fmt.Sprintf("wsl --export %s \"%s\"", p.DistroName, p.ExportTarPath),
		fmt.Sprintf("wsl --unregister %s", p.DistroName),
		fmt.Sprintf("wsl --import %s \"%s\" \"%s\"", p.DistroName, p.TargetDir, p.ExportTarPath),
	}
}

// RollbackCommands re-imports the distro back at its original VHDX location
// using the same export tarball — assumes the tarball has not been deleted
// yet (callers should only clean it up after Finalize(), not after Commit()).
func (p *WSL2IsolationPlan) RollbackCommands(originalDir string) []string {
	return []string{
		fmt.Sprintf("wsl --unregister %s", p.DistroName),
		fmt.Sprintf("wsl --import %s \"%s\" \"%s\"", p.DistroName, originalDir, p.ExportTarPath),
	}
}
