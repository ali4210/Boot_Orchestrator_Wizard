//go:build !windows

package safety

// RestrictToAdministrators is a no-op on non-Windows platforms:
// os.MkdirAll's mode parameter (0700) is already a real, enforced
// permission on Unix, so there is nothing further to restrict here. See
// journal_acl_windows.go for the real implementation this stands in for
// on Windows, where 0700 alone does not provide an equivalent guarantee.
func RestrictToAdministrators(dir string) error {
	return nil
}
