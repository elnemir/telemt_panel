//go:build linux && !lite

package nodeinstall

import (
	"os"
	"path/filepath"
	"syscall"
)

// systemd can issue credentials as root:root 0440 with a service-specific
// ACL. Limit the exception to the private directory supplied by the manager.
func systemdCredentialKey(path string, opened os.FileInfo) bool {
	directory := os.Getenv("CREDENTIALS_DIRECTORY")
	if !filepath.IsAbs(directory) || filepath.Dir(path) != filepath.Clean(directory) ||
		opened.Mode().Perm() != 0440 || !ownedByRoot(opened) {
		return false
	}
	dir, err := os.Lstat(directory)
	if err != nil || !dir.IsDir() || dir.Mode().Perm()&0027 != 0 || !ownedByRoot(dir) {
		return false
	}
	current, err := os.Lstat(path)
	return err == nil && current.Mode().IsRegular() && os.SameFile(opened, current)
}

func ownedByRoot(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0 && stat.Gid == 0
}
