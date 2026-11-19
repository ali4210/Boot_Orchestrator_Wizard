//go:build !windows

package engine

import "golang.org/x/sys/unix"

// freeDiskSpaceBytes reports the free space available to an unprivileged
// user on the filesystem containing path (audit item 12/E's free-disk-space
// pre-check). Uses Statfs's Bavail (blocks available to non-superusers),
// not Bfree (blocks free including the superuser-reserved margin), so this
// matches what a normal download actually has room to use.
func freeDiskSpaceBytes(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
