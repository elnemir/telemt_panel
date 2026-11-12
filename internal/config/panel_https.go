package config

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/amirotin/telemt_panel/internal/atomicfile"
)

const PanelHTTPSName = "panel-https.toml"

type panelHTTPS struct {
	TLS       TLSConfig `toml:"tls"`
	PublicURL string    `toml:"public_url"`
}

// ReadPanelHTTPS overlays the panel listener's TLS and origin, inheriting the
// bind address, URL prefix and all other configuration from the startup file.
func ReadPanelHTTPS(base TLSSnapshot, dataDir string) (TLSSnapshot, error) {
	raw, err := readPanelOverrideFile(dataDir, PanelHTTPSName)
	if err != nil {
		return TLSSnapshot{}, err
	}
	if raw == nil {
		return base, nil
	}
	var overlay panelHTTPS
	md, err := toml.NewDecoder(bytes.NewReader(raw)).Decode(&overlay)
	if err != nil {
		return TLSSnapshot{}, sourceParseError(err)
	}
	if len(md.Undecoded()) != 0 {
		return TLSSnapshot{}, errors.New("unknown panel HTTPS setting")
	}
	next, err := applyPanelHTTPS(base.Config, overlay)
	if err != nil {
		return TLSSnapshot{}, err
	}
	hash := sha256.New()
	_, _ = hash.Write(base.Revision[:])
	_, _ = hash.Write(raw)
	return TLSSnapshot{Config: next, Revision: [32]byte(hash.Sum(nil))}, nil
}

func applyPanelHTTPS(base *Config, overlay panelHTTPS) (*Config, error) {
	switch overlay.TLS.Mode {
	case "certificate", "acme":
	default:
		return nil, errors.New("panel HTTPS mode must be certificate or acme")
	}
	candidate := TLSCandidate{Listen: base.Listen, BasePath: base.BasePath, PublicURL: overlay.PublicURL, TLS: overlay.TLS}
	if err := candidate.Normalize(""); err != nil {
		return nil, err
	}
	if candidate.BasePath != base.BasePath {
		return nil, errors.New("panel HTTPS cannot change the base path")
	}
	next := *base
	next.TLS, next.PublicURL = candidate.TLS, candidate.PublicURL
	if err := next.NormalizeAccess(); err != nil {
		return nil, err
	}
	return &next, nil
}

// SavePanelHTTPS publishes a validated, allowlisted override. The API serializes
// writes and compares the startup and override revisions before publishing.
func SavePanelHTTPS(base *Config, dataDir string, candidate TLSCandidate) (bool, error) {
	overlay := panelHTTPS{TLS: candidate.TLS, PublicURL: candidate.PublicURL}
	if _, err := applyPanelHTTPS(base, overlay); err != nil {
		return false, err
	}
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(overlay); err != nil {
		return false, err
	}
	if encoded.Len() > PanelOverridesMaxBytes {
		return false, errors.New("panel HTTPS override is too large")
	}
	return atomicfile.Write(filepath.Join(dataDir, PanelHTTPSName), 0600, func(w io.Writer) error {
		_, err := w.Write(encoded.Bytes())
		return err
	})
}
