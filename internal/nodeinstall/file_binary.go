//go:build !lite

package nodeinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// FileBinary is a panel-host artifact intended for file-capable installations.
// Its source, digest and version come from startup configuration.
type FileBinary struct {
	Path    string
	SHA256  string
	Version string
}

func (b FileBinary) configure(plan Plan) Plan {
	if b.Path != "" && plan.WithAgent {
		plan.Release, plan.ConfigFiles = b.Version, true
	}
	return plan
}

func (b FileBinary) stage(dir string) (string, error) {
	info, err := os.Stat(b.Path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxBundleBinarySize {
		return "", ErrArtifacts
	}
	input, err := os.Open(b.Path)
	if err != nil {
		return "", ErrArtifacts
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", ErrArtifacts
	}
	path := filepath.Join(dir, "telemt")
	output, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", ErrArtifacts
	}
	digest := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(output, digest), io.LimitReader(input, maxBundleBinarySize+1))
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil || size != info.Size() || !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), b.SHA256) {
		return "", ErrArtifacts
	}
	return path, nil
}
