package host

import (
	"fmt"
	"path/filepath"
)

// Shared by the portable sudo transport and Linux policy installation.
const maxPolicySize = 64 << 10

func privilegedPath(path string) error {
	if path == "/" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("privileged path %q must be normalized and absolute", path)
	}
	return nil
}
