package engine

import (
	"os"
	"strconv"
)

// FS abstracts the filesystem-write operations a dry-run needs to
// intercept, mirroring how Runner already abstracts command execution.
// Phase 5 item 32: currently only a proof-of-concept batch of call sites
// are migrated onto this — see docs/dryrun-inventory.md and
// engine/dryrun.go's own doc comment for exactly what is and isn't
// covered yet. Read-only filesystem operations (os.Stat, os.ReadFile,
// os.ReadDir, ...) are deliberately NOT part of this interface: they
// never need interception, dry-run or not.
type FS interface {
	WriteFile(name string, data []byte, perm os.FileMode) error
	MkdirAll(path string, perm os.FileMode) error
	Remove(name string) error
	RemoveAll(path string) error
	Symlink(oldname, newname string) error
	Rename(oldpath, newpath string) error
}

// DefaultFS is the real, production FS backed by the os package.
type DefaultFS struct{}

func (DefaultFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	return os.WriteFile(name, data, perm)
}

func (DefaultFS) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (DefaultFS) Remove(name string) error {
	return os.Remove(name)
}

func (DefaultFS) RemoveAll(path string) error {
	return os.RemoveAll(path)
}

func (DefaultFS) Symlink(oldname, newname string) error {
	return os.Symlink(oldname, newname)
}

func (DefaultFS) Rename(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}

// DryRunFS logs what it would have written/removed and returns success
// without touching the real filesystem.
type DryRunFS struct{}

func (DryRunFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	logDryRun("write", name, perm.String(), fmtByteCount(len(data)))
	return nil
}

func (DryRunFS) MkdirAll(path string, perm os.FileMode) error {
	logDryRun("mkdir -p", path, perm.String())
	return nil
}

func (DryRunFS) Remove(name string) error {
	logDryRun("remove", name)
	return nil
}

func (DryRunFS) RemoveAll(path string) error {
	logDryRun("remove -r", path)
	return nil
}

func (DryRunFS) Symlink(oldname, newname string) error {
	logDryRun("symlink", newname, "->", oldname)
	return nil
}

func (DryRunFS) Rename(oldpath, newpath string) error {
	logDryRun("rename", oldpath, "->", newpath)
	return nil
}

func fmtByteCount(n int) string {
	return "(" + strconv.Itoa(n) + " bytes)"
}

// fsRunner is the active FS used by migrated engine package call sites.
// Tests may replace it with a mock and must restore it when done, since
// it is shared process-wide state — same contract as cmdRunner.
var fsRunner FS = DefaultFS{}
