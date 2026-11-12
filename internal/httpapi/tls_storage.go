package httpapi

import (
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/amirotin/telemt_panel/internal/atomicfile"
	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/config"
)

const certificatePEMLimit = 128 << 10

// prepareTLSRequest carries upload strings as input only. They never become
// mutation receipts, audit entries or persisted configuration fields.
type prepareTLSRequest struct {
	config.TLSCandidate
	ConfirmHTTP       bool `json:"confirm_http"`
	CertificateUpload *struct {
		CertificatePEM string `json:"certificate_pem"`
		PrivateKeyPEM  string `json:"private_key_pem"`
	} `json:"certificate_upload"`
}

func (s *Server) certificateDirectory() string {
	dataDir := s.cfg.DataDir
	if dataDir == "" || s.cfg.Auth.Disabled || s.cfg.Agent.Enabled {
		return ""
	}
	dir, err := filepath.Abs(filepath.Join(dataDir, "panel-certificates"))
	if err != nil {
		return ""
	}
	return dir
}

func (s *Server) secureCertificateUpload(r *http.Request) bool {
	if auth.RequestIsSecure(r, s.cfg.TrustedProxyPrefixes) {
		return true
	}
	peer, _, _ := net.SplitHostPort(r.RemoteAddr)
	host := r.Host
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	return net.ParseIP(peer).IsLoopback() && (host == "localhost" || net.ParseIP(strings.Trim(host, "[]")).IsLoopback())
}

func (s *Server) readTLSSnapshot(managed bool) (config.TLSSnapshot, error) {
	var snapshot config.TLSSnapshot
	if s.access.file != nil {
		var err error
		snapshot, err = s.access.file.Read()
		if err != nil {
			return config.TLSSnapshot{}, err
		}
	} else if managed && s.panelSettingsBase != nil {
		// Startup already validated this configuration. A source supplied via
		// a symlink or legacy credentials may not be eligible for TLSFile writes.
		snapshot.Config = s.panelSettingsBase
	} else {
		return config.TLSSnapshot{}, errors.New("startup configuration is unavailable")
	}
	if managed {
		return config.ReadPanelHTTPS(snapshot, s.cfg.DataDir)
	}
	return snapshot, nil
}

func saveUploadedCertificate(path string, pem []byte) (bool, error) {
	dir := filepath.Dir(path)
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return false, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return false, errors.New("certificate storage must be a private directory")
	}
	return atomicfile.Write(path, 0600, func(w io.Writer) error {
		_, err := w.Write(pem)
		return err
	})
}

func (s *Server) managedACMECache() string {
	return filepath.Join(filepath.Dir(s.certificateDirectory()), "certs", "panel")
}
