package httpapi

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amirotin/telemt_panel/internal/config"
	"github.com/amirotin/telemt_panel/internal/paneltls"
)

func makeUploadPair(t *testing.T) (cert, key string) {
	t.Helper()
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"panel.example.test"},
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, &private.PublicKey, private)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
}

func TestManagedPanelHTTPSUpload(t *testing.T) {
	s, cookie, startupPath := accessServer(t)
	original, err := os.ReadFile(startupPath)
	if err != nil {
		t.Fatal(err)
	}
	base, err := config.Load(startupPath)
	if err != nil {
		t.Fatal(err)
	}
	base.DataDir = t.TempDir()
	s.cfg.DataDir = base.DataDir
	s.SetPanelSettingsBase(base)
	// Exercise a startup source that can be read normally but cannot be
	// opened by the pinned, no-symlinks startup editor.
	alias := filepath.Join(t.TempDir(), "credential.toml")
	if err := os.Symlink(startupPath, alias); err != nil {
		t.Fatal(err)
	}
	if s.access.file != nil {
		if err := s.access.file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	s.SetTLSConfigPath(alias)
	const configURL = "/api/settings/tls/config"
	const prepareURL = "/api/settings/tls/prepare"
	w := accessRequest(t, s, cookie, "GET", configURL, nil)
	var view tlsSettings
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || view.Capabilities.ConfigWritable || !view.Capabilities.ManagedHTTPS || !view.Capabilities.Upload || !view.Capabilities.Prepare {
		t.Fatalf("managed HTTPS capabilities: %d %+v", w.Code, view.Capabilities)
	}

	cert, key := makeUploadPair(t)
	_, otherKey := makeUploadPair(t)
	request := func(target, host, certificate, privateKey string) *httptest.ResponseRecorder {
		return accessRequest(t, s, cookie, "POST", target+prepareURL, map[string]any{
			"listen": s.cfg.Listen, "tls": config.TLSConfig{Mode: "certificate"},
			"public_url":         "https://" + host,
			"certificate_upload": map[string]string{"certificate_pem": certificate, "private_key_pem": privateKey},
		})
	}
	for _, tc := range []struct {
		name, url, host, certificate, privateKey string
		want                                     int
	}{
		{"insecure", "", "panel.example.test", cert, key, 403},
		{"mismatched key", "https://test", "panel.example.test", cert, otherKey, 422},
		{"wrong hostname", "https://test", "other.example.test", cert, key, 422},
		{"too large", "https://test", "panel.example.test", cert, strings.Repeat("a", certificatePEMLimit+1), 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(tc.url, tc.host, tc.certificate, tc.privateKey)
			if w.Code != tc.want || strings.Contains(w.Body.String(), "PRIVATE KEY") || strings.Contains(w.Body.String(), "BEGIN CERTIFICATE") {
				t.Fatalf("status=%d, want %d, or sensitive response", w.Code, tc.want)
			}
		})
	}

	w = request("https://test", "panel.example.test", cert, key)
	var prepared tlsPrepared
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &prepared) != nil || prepared.Certificate == nil || prepared.Certificate.Domain != "panel.example.test" ||
		strings.Contains(w.Body.String(), "PRIVATE KEY") || strings.Contains(w.Body.String(), "BEGIN CERTIFICATE") {
		t.Fatalf("prepare status=%d or invalid certificate preview: %s", w.Code, w.Body.String())
	}
	managedPath := filepath.Join(base.DataDir, config.PanelHTTPSName)
	for _, path := range []string{managedPath, prepared.Candidate.TLS.CertFile} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("prepare must not create %s: %v", path, err)
		}
	}

	save := map[string]any{"receipt": prepared.Receipt, "candidate": prepared.Candidate}
	if w := accessRequest(t, s, cookie, "PUT", configURL, save); w.Code != http.StatusForbidden {
		t.Fatalf("insecure save accepted: %d", w.Code)
	}
	w = accessRequest(t, s, cookie, "PUT", "https://test"+configURL, save)
	if w.Code != 200 {
		t.Fatalf("save status=%d: %s", w.Code, w.Body.String())
	}
	if w := accessRequest(t, s, cookie, "PUT", "https://test"+configURL, save); w.Code != http.StatusConflict {
		t.Fatalf("save reused receipt: %d", w.Code)
	}
	for _, path := range []string{managedPath, prepared.Candidate.TLS.CertFile} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("expected private file %s: %v", path, err)
		}
	}
	if after, err := os.ReadFile(startupPath); err != nil || string(after) != string(original) {
		t.Fatal("saving managed HTTPS changed the startup configuration")
	}
	next, err := config.ReadPanelHTTPS(config.TLSSnapshot{Config: base}, base.DataDir)
	if err != nil || next.Config.PublicURL != "https://panel.example.test" || next.Config.Listen != base.Listen || next.Config.TLS.Mode != "certificate" {
		t.Fatalf("cannot load HTTPS after restart: %v", err)
	}
	manager := paneltls.New(next.Config.TLS, next.Config.Listen)
	defer manager.Close()
	serverTLS, _, err := manager.Prepare()
	if err != nil || len(serverTLS.Certificates) != 1 {
		t.Fatalf("runtime could not load saved pair: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(cert))
	listener := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	listener.TLS = serverTLS
	listener.StartTLS()
	defer listener.Close()
	client := listener.Client()
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: "panel.example.test"}
	resp, err := client.Get(listener.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("HTTPS request returned %d", resp.StatusCode)
	}
}
