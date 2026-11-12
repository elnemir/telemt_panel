//go:build !linux && !lite

package nodeinstall

import "os"

func systemdCredentialKey(string, os.FileInfo) bool { return false }
