package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/config"
	"github.com/amirotin/telemt_panel/internal/host"
	"github.com/amirotin/telemt_panel/internal/paneltls"
)

type tlsPrepared struct {
	Receipt      string                    `json:"receipt"`
	ExpiresAt    time.Time                 `json:"expires_at"`
	Candidate    config.TLSCandidate       `json:"candidate"`
	NewURL       string                    `json:"new_url"`
	Warnings     []string                  `json:"warnings"`
	Certificate  *paneltls.CertificateInfo `json:"certificate,omitempty"`
	revision     [32]byte
	subscription bool
	managed      bool
	pem          []byte
}

type tlsCapabilities struct {
	ConfigWritable bool `json:"config_writable"`
	Restart        bool `json:"restart"`
	Prepare        bool `json:"prepare"`
	ACMEPrepare    bool `json:"acme_prepare"`
	ManagedHTTPS   bool `json:"managed_https,omitempty"`
	Upload         bool `json:"certificate_upload,omitempty"`
}

type tlsSettings struct {
	Active               config.TLSCandidate  `json:"active"`
	Configured           *config.TLSCandidate `json:"configured,omitempty"`
	Capabilities         tlsCapabilities      `json:"capabilities"`
	ManualHints          []string             `json:"manual_hints"`
	DefaultACMECacheDir  string               `json:"default_acme_cache_dir"`
	ConfigPath           string               `json:"config_path,omitempty"`
	ManualRestartCommand string               `json:"manual_restart_command"`
	State                string               `json:"state"`
	Prepared             *tlsPrepared         `json:"prepared,omitempty"`
	RestartRequired      bool                 `json:"restart_required"`
	NewURL               string               `json:"new_url,omitempty"`
	Error                string               `json:"error,omitempty"`
	CertificateStatus    paneltls.Status      `json:"certificate_status"`
}

type panelAccess struct {
	mu                sync.Mutex
	file              *config.TLSFile
	path              string
	ctx               context.Context
	cancel            context.CancelFunc
	mux               *paneltls.ChallengeMux
	run               paneltls.AcquisitionRunner
	state             string
	pending           *tlsPrepared
	pendingTimer      *time.Timer
	savedManaged      bool
	saved             bool
	savedRevision     [32]byte
	newURL            string
	err               string
	savedSubscription bool
}

func newPanelAccess() *panelAccess {
	ctx, cancel := context.WithCancel(context.Background())
	return &panelAccess{ctx: ctx, cancel: cancel, state: "idle"}
}

func (a *panelAccess) clearPendingLocked() {
	if a.pendingTimer != nil {
		a.pendingTimer.Stop()
		a.pendingTimer = nil
	}
	if a.pending != nil {
		clear(a.pending.pem)
		a.pending = nil
	}
}

func (a *panelAccess) close() {
	a.cancel()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.clearPendingLocked()
	if a.file != nil {
		_ = a.file.Close()
		a.file = nil
	}
}

// SetTLSConfigPath pins the startup configuration before Run/Handler.
func (s *Server) SetTLSConfigPath(path string) {
	a := s.access
	a.path = path
	a.file, _ = config.OpenTLSFile(path)
}

func (s *Server) tlsRestartAvailable() bool {
	name, _ := resolveLogicalService("panel", s.svcMgr.Kind(), s.cfg.Host)
	caps := s.svcMgr.Caps()
	if name == "" || s.runner == nil || !caps.CanRestart || s.privilegesMode == host.PrivilegesModeManual {
		return false
	}
	// A service manager installed on the host does not make a manually started
	// panel restartable. Verify the configured service before allowing a handoff.
	if caps.CanStatus {
		ctx, cancel := context.WithTimeout(s.access.ctx, 2*time.Second)
		defer cancel()
		status, err := s.svcMgr.Status(ctx, name)
		return err == nil && status == host.StatusRunning
	}
	return true
}

func (s *Server) tlsCapabilitiesLocked(subscription bool) (tlsCapabilities, []string) {
	a := s.access
	caps := tlsCapabilities{Restart: s.tlsRestartAvailable()}
	hints := []string{}
	if a.file != nil {
		caps.ConfigWritable = a.file.Writable() == nil
	}
	caps.Upload = !subscription && s.certificateDirectory() != ""
	if caps.Upload {
		_, err := os.Lstat(filepath.Join(s.cfg.DataDir, config.PanelHTTPSName))
		caps.ManagedHTTPS = (a.file != nil || s.panelSettingsBase != nil) && (!caps.ConfigWritable || !errors.Is(err, os.ErrNotExist))
	}
	if !caps.ConfigWritable && !caps.ManagedHTTPS {
		hints = append(hints, "edit_startup_config_manually")
	}
	if !caps.Restart {
		hints = append(hints, "restart_panel_manually")
	}
	caps.Prepare = caps.ConfigWritable || caps.ManagedHTTPS
	// Bind permission and port ownership are checked before contacting the CA.
	// This capability only promises the helper is installed, not public reachability.
	_, err := os.Executable()
	caps.ACMEPrepare = caps.Prepare && err == nil
	return caps, hints
}

func (s *Server) defaultTLSCacheLocked() string {
	if s.cfg.TLS.AcmeCacheDir != "" {
		return s.cfg.TLS.AcmeCacheDir
	}
	if s.access.path == "" {
		return ""
	}
	abs, err := filepath.Abs(s.access.path)
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(abs), "certs")
}

func (s *Server) handleGetTLSConfig(w http.ResponseWriter, r *http.Request) {
	subscription, ok := accessTarget(w, r)
	if !ok {
		return
	}
	a := s.access
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending != nil && time.Now().After(a.pending.ExpiresAt) {
		a.clearPendingLocked()
		a.state = "idle"
	}
	caps, hints := s.tlsCapabilitiesLocked(subscription)
	state := tlsSettings{Active: accessCandidate(s.cfg, subscription), Capabilities: caps, ManualHints: hints, DefaultACMECacheDir: s.accessCache(subscription), State: a.state, RestartRequired: a.saved, NewURL: a.newURL, Error: a.err}
	state.CertificateStatus = s.tlsManager.Status()
	if a.saved && a.savedSubscription != subscription {
		state.NewURL = ""
	}
	if subscription {
		state.CertificateStatus = s.subTLSManager.Status()
	}
	if a.pending != nil && a.pending.subscription == subscription {
		state.Prepared = a.pending
	}
	state.ConfigPath = a.path
	service, _ := resolveLogicalService("panel", s.svcMgr.Kind(), s.cfg.Host)
	state.ManualRestartCommand = serviceCommandHint(s.svcMgr, service, "restart")
	if state.ManualRestartCommand == "" {
		state.ManualRestartCommand = manualRestartCommand(s.svcMgr.Kind(), service)
	}
	if state.Active.TLS.Mode == "" {
		state.Active.TLS.Mode = "http"
	}
	if snapshot, err := s.readTLSSnapshot(caps.ManagedHTTPS); err == nil {
		configured := accessCandidate(snapshot.Config, subscription)
		state.Configured = &configured
	}
	if caps.ManagedHTTPS {
		state.ConfigPath = filepath.Join(s.cfg.DataDir, config.PanelHTTPSName)
		state.DefaultACMECacheDir = s.managedACMECache()
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, state)
}

func decodeTLSBody(w http.ResponseWriter, r *http.Request, value any, maxBytes int64) bool {
	if err := decodeJSONBody(w, r, value, jsonBodyOptions{MaxBytes: maxBytes, RejectUnknown: true, ClassifyReadErrors: true}); err != nil {
		message := "invalid transport request body"
		if errors.Is(err, errJSONBodyTrailingData) {
			message = "one JSON object is required"
		}
		auth.WriteError(w, http.StatusBadRequest, "bad_request", message)
		return false
	}
	return true
}

func (s *Server) handlePrepareTLS(w http.ResponseWriter, r *http.Request) {
	subscription, ok := accessTarget(w, r)
	if !ok {
		return
	}
	var request prepareTLSRequest
	if !decodeTLSBody(w, r, &request, 2*6*certificatePEMLimit+(16<<10)) {
		return
	}
	a := s.access
	a.mu.Lock()
	if a.state == "preparing" || a.saved {
		a.mu.Unlock()
		auth.WriteError(w, http.StatusConflict, "tls_prepare_busy", "a preparation or saved restart is already pending")
		return
	}
	caps, _ := s.tlsCapabilitiesLocked(subscription)
	if !caps.Prepare {
		a.mu.Unlock()
		auth.WriteError(w, http.StatusServiceUnavailable, "tls_manual_required", "HTTPS configuration storage is unavailable; apply transport manually")
		return
	}
	candidate := request.TLSCandidate
	snapshot, err := s.readTLSSnapshot(caps.ManagedHTTPS)
	if err != nil {
		a.mu.Unlock()
		auth.WriteError(w, http.StatusConflict, "tls_config_changed", "HTTPS configuration is no longer readable and valid")
		return
	}
	upload := request.CertificateUpload
	if upload != nil {
		if !caps.Upload || !s.secureCertificateUpload(r) {
			a.mu.Unlock()
			auth.WriteError(w, http.StatusForbidden, "tls_upload_secure_required", "certificate upload requires an authenticated HTTPS or local connection and persistent panel storage")
			return
		}
		if candidate.TLS.Mode != "certificate" || candidate.TLS.CertFile != "" || candidate.TLS.KeyFile != "" || candidate.PublicURL == "" ||
			len(upload.CertificatePEM) == 0 || len(upload.PrivateKeyPEM) == 0 || len(upload.CertificatePEM) > certificatePEMLimit || len(upload.PrivateKeyPEM) > certificatePEMLimit {
			a.mu.Unlock()
			auth.WriteError(w, http.StatusBadRequest, "tls_certificate_invalid", "provide an HTTPS address and two PEM files of at most 128 KiB each")
			return
		}
		var id [16]byte
		_, _ = rand.Read(id[:])
		candidate.TLS.CertFile = filepath.Join(s.certificateDirectory(), hex.EncodeToString(id[:])+".pem")
		candidate.TLS.KeyFile = candidate.TLS.CertFile
	}
	cache := s.accessCache(subscription)
	if caps.ManagedHTTPS {
		if candidate.Listen != snapshot.Config.Listen || (candidate.TLS.Mode != "certificate" && candidate.TLS.Mode != "acme") ||
			(candidate.TLS.Mode == "certificate" && upload == nil && (candidate.TLS.CertFile != snapshot.Config.TLS.CertFile || candidate.TLS.KeyFile != snapshot.Config.TLS.KeyFile)) {
			a.mu.Unlock()
			auth.WriteError(w, http.StatusBadRequest, "tls_invalid_candidate", "managed HTTPS requires the existing listener and either PEM upload or ACME")
			return
		}
		cache = s.managedACMECache()
		if candidate.TLS.Mode == "acme" {
			candidate.TLS.AcmeCacheDir = cache
		}
	}
	peerTLS := s.cfg.TLS
	if !subscription {
		peerTLS = s.cfg.Subpage.TLS
	}
	if candidate.TLS.Mode == "acme" && peerTLS.Mode == "acme" && strings.EqualFold(strings.TrimSuffix(candidate.TLS.AcmeDomain, "."), peerTLS.AcmeDomain) && (candidate.TLS.AcmeCacheDir == "" || candidate.TLS.AcmeCacheDir == cache) {
		candidate.TLS.AcmeCacheDir = peerTLS.AcmeCacheDir
	}
	if err := candidate.Normalize(cache); err != nil {
		a.mu.Unlock()
		auth.WriteError(w, http.StatusBadRequest, "tls_invalid_candidate", err.Error())
		return
	}
	if (!subscription || candidate.Enabled) && candidate.TLS.Mode == "http" && (!loopbackListen(candidate.Listen) || strings.HasPrefix(candidate.PublicURL, "http://")) && !request.ConfirmHTTP {
		a.mu.Unlock()
		auth.WriteError(w, http.StatusBadRequest, "confirmation_required", "confirm unencrypted HTTP before preparation")
		return
	}
	if caps.ManagedHTTPS && candidate.BasePath != snapshot.Config.BasePath {
		a.mu.Unlock()
		auth.WriteError(w, http.StatusBadRequest, "tls_invalid_candidate", "managed HTTPS cannot change the base path")
		return
	}
	if err := validateAccessCandidate(snapshot.Config, candidate, subscription); err != nil {
		a.mu.Unlock()
		auth.WriteError(w, http.StatusBadRequest, "tls_invalid_candidate", err.Error())
		return
	}
	a.state = "preparing"
	a.clearPendingLocked()
	a.err = ""
	rootCtx, mux, run := a.ctx, a.mux, a.run
	a.mu.Unlock()
	// Override the ordinary 60s write timeout for this route only. Deadline stays
	// bounded even if the browser has a longer-lived HTTP/2 connection.
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(115 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		a.mu.Lock()
		a.state = "idle"
		a.mu.Unlock()
		auth.WriteError(w, http.StatusServiceUnavailable, "tls_prepare_unavailable", "unable to extend response deadline for certificate preparation")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), paneltls.PrepareTimeout)
	defer cancel()
	stop := context.AfterFunc(rootCtx, cancel)
	defer stop()
	var certificate *paneltls.CertificateInfo
	var pem []byte
	defer func() { clear(pem) }()
	if !subscription || candidate.Enabled {
		activeListen := s.cfg.Listen
		if subscription {
			activeListen = ""
			if s.cfg.Subpage.Enabled {
				activeListen = s.cfg.Subpage.Listen
			}
		}
		if upload != nil {
			key := []byte(upload.PrivateKeyPEM)
			certificate, pem, err = paneltls.PrepareUploadedCertificate(ctx, candidate, activeListen, []byte(upload.CertificatePEM), key)
			clear(key)
			request.CertificateUpload = nil
			upload = nil
		} else {
			certificate, err = paneltls.PrepareCandidate(ctx, candidate, activeListen, mux, run)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state = "idle"
	if err != nil {
		var preparation *paneltls.PrepareError
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			auth.WriteError(w, 504, "tls_prepare_timeout", "preparation canceled or timed out; current access is unchanged")
		case errors.As(err, &preparation):
			auth.WriteError(w, 422, preparation.Code, preparation.Message)
		default:
			auth.WriteError(w, 422, "tls_prepare_failed", "preparation failed; current access is unchanged")
		}
		return
	}
	if ctx.Err() != nil {
		auth.WriteError(w, 504, "tls_prepare_timeout", "preparation canceled; current access is unchanged")
		return
	}
	latest, err := s.readTLSSnapshot(caps.ManagedHTTPS)
	if err != nil || latest.Revision != snapshot.Revision {
		auth.WriteError(w, http.StatusConflict, "tls_config_changed", "configuration changed during preparation; prepare again")
		return
	}
	var nonce [32]byte
	_, _ = rand.Read(nonce[:])
	prepared := &tlsPrepared{Receipt: hex.EncodeToString(nonce[:]), ExpiresAt: time.Now().Add(5 * time.Minute), Candidate: candidate, Certificate: certificate, NewURL: s.tlsNewURL(r, candidate, certificate), Warnings: s.tlsWarnings(candidate, certificate, subscription, caps.ManagedHTTPS), revision: snapshot.Revision, subscription: subscription, managed: caps.ManagedHTTPS, pem: pem}
	pem = nil // Ownership transferred to the expiring receipt.
	a.pending = prepared
	a.pendingTimer = time.AfterFunc(time.Until(prepared.ExpiresAt), func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.pending == prepared {
			a.clearPendingLocked()
			a.state = "idle"
		}
	})
	a.state = "prepared"
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, prepared)
}

func loopbackListen(listen string) bool {
	h, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	return h == "localhost" || net.ParseIP(h) != nil && net.ParseIP(h).IsLoopback()
}

func (s *Server) tlsWarnings(c config.TLSCandidate, cert *paneltls.CertificateInfo, subscription, managed bool) []string {
	warnings := []string{}
	if !managed {
		warnings = append(warnings, "toml_formatting")
	}
	if subscription && !c.Enabled {
		return warnings
	}
	warnings = append(warnings, "public_reachability_unverified")
	activeListen := s.cfg.Listen
	if subscription {
		activeListen = s.cfg.Subpage.Listen
	} else {
		warnings = append(warnings, "password_login")
	}
	_, oldPort, _ := net.SplitHostPort(activeListen)
	_, newPort, _ := net.SplitHostPort(c.Listen)
	if oldPort != newPort {
		warnings = append(warnings, "firewall_port")
	}
	if c.TLS.Mode == "acme" {
		warnings = append(warnings, "acme_port_80")
	}
	if c.TLS.Mode == "http" {
		warnings = append(warnings, "http_unencrypted")
	}
	if cert != nil && !cert.PubliclyTrusted {
		warnings = append(warnings, "certificate_private")
	}
	return warnings
}

func (s *Server) tlsNewURL(r *http.Request, c config.TLSCandidate, cert *paneltls.CertificateInfo) string {
	if c.PublicURL != "" {
		return c.PublicURL + c.BasePath + "/"
	}
	h, port, _ := net.SplitHostPort(c.Listen)
	scheme := "http"
	if c.TLS.Mode != "http" {
		scheme = "https"
	}
	if c.TLS.AcmeDomain != "" {
		h = c.TLS.AcmeDomain
	} else if cert != nil && cert.Domain != "" && !strings.Contains(cert.Domain, "*") {
		h = cert.Domain
	}
	if h == "" || net.ParseIP(h) != nil && net.ParseIP(h).IsUnspecified() {
		h = r.Host
		if host, _, err := net.SplitHostPort(h); err == nil {
			h = host
		}
		probe := config.TLSCandidate{Listen: net.JoinHostPort(h, port), TLS: config.TLSConfig{Mode: "http"}}
		if probe.Normalize("") != nil {
			h = "localhost"
		}
	}
	u := url.URL{Scheme: scheme, Host: net.JoinHostPort(h, port), Path: c.BasePath + "/"}
	return u.String()
}

func (s *Server) handlePutTLSConfig(w http.ResponseWriter, r *http.Request) {
	subscription, ok := accessTarget(w, r)
	if !ok {
		return
	}
	var request struct {
		Receipt   string              `json:"receipt"`
		Candidate config.TLSCandidate `json:"candidate"`
	}
	if !decodeTLSBody(w, r, &request, 16<<10) {
		return
	}
	a := s.access
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.pending
	if p == nil || p.subscription != subscription || p.Receipt != request.Receipt || time.Now().After(p.ExpiresAt) || request.Candidate != p.Candidate {
		auth.WriteError(w, http.StatusConflict, "tls_receipt_invalid", "prepare this exact candidate again; receipt is stale, changed or already used")
		return
	}
	caps, _ := s.tlsCapabilitiesLocked(subscription)
	if !caps.Prepare {
		auth.WriteError(w, http.StatusServiceUnavailable, "tls_manual_required", "HTTPS configuration storage is unavailable")
		return
	}
	if caps.ManagedHTTPS != p.managed {
		auth.WriteError(w, http.StatusConflict, "tls_config_changed", "configuration storage changed after preparation; prepare again")
		return
	}
	snapshot, err := s.readTLSSnapshot(p.managed)
	if err != nil || snapshot.Revision != p.revision {
		auth.WriteError(w, http.StatusConflict, "tls_config_changed", "configuration changed after preparation; prepare again")
		return
	}
	if len(p.pem) > 0 && !s.secureCertificateUpload(r) {
		auth.WriteError(w, http.StatusForbidden, "tls_upload_secure_required", "certificate upload requires a secure connection")
		return
	}
	if len(p.pem) == 0 && (!subscription || p.Candidate.Enabled) && p.Candidate.TLS.Mode == "certificate" {
		certificate, err := paneltls.PrepareCandidate(r.Context(), p.Candidate, p.Candidate.Listen, nil, nil)
		if err != nil || certificate == nil || p.Certificate == nil || *certificate != *p.Certificate {
			auth.WriteError(w, 422, "tls_certificate_invalid", "prepared certificate/key changed or became unavailable; prepare again")
			return
		}
	}
	if (!subscription || p.Candidate.Enabled) && p.Candidate.TLS.Mode == "acme" {
		if err := paneltls.VerifyPreparedCache(p.Candidate, p.Certificate); err != nil {
			var preparation *paneltls.PrepareError
			if errors.As(err, &preparation) {
				auth.WriteError(w, 422, preparation.Code, preparation.Message)
			} else {
				auth.WriteError(w, 422, "tls_certificate_invalid", "prepared ACME cache is no longer valid; prepare again")
			}
			return
		}
	}
	uploaded := len(p.pem) > 0
	if uploaded {
		if !time.Now().Before(p.Certificate.ExpiresAt) {
			auth.WriteError(w, 422, "tls_certificate_invalid", "prepared certificate expired; prepare again")
			return
		}
		if published, err := saveUploadedCertificate(p.Candidate.TLS.CertFile, p.pem); err != nil {
			if published {
				_ = os.Remove(p.Candidate.TLS.CertFile)
			}
			auth.WriteError(w, http.StatusServiceUnavailable, "tls_save_failed", "unable to write private certificate storage")
			return
		}
	}
	published := false
	if p.managed {
		published, err = config.SavePanelHTTPS(snapshot.Config, s.cfg.DataDir, p.Candidate)
	} else {
		err = a.file.SaveAccess(p.revision, p.Candidate, subscription, true)
		published = err == nil
	}
	if err != nil && !published {
		if uploaded {
			_ = os.Remove(p.Candidate.TLS.CertFile)
		}
		if errors.Is(err, config.ErrTLSRevision) {
			auth.WriteError(w, http.StatusConflict, "tls_config_changed", "configuration changed after preparation; prepare again")
		} else {
			auth.WriteError(w, http.StatusServiceUnavailable, "tls_save_failed", "unable to save HTTPS configuration; check file and directory permissions")
		}
		return
	}
	durable := err == nil
	snapshot, err = s.readTLSSnapshot(p.managed)
	if err == nil {
		a.savedRevision = snapshot.Revision
	}
	a.savedManaged = p.managed
	a.saved = true
	a.savedSubscription = subscription
	a.newURL = p.NewURL
	a.clearPendingLocked()
	a.state = "saved"
	if p.managed && !durable {
		auth.WriteError(w, http.StatusServiceUnavailable, "tls_save_failed", "HTTPS settings are visible but disk durability could not be confirmed; inspect saved settings before restarting")
		return
	}
	auditTarget := "panel"
	if subscription {
		auditTarget = "subscription"
	}
	s.appendAudit(r, "panel.tls.save", "", auditTarget+":"+p.Candidate.TLS.Mode)
	writeJSON(w, http.StatusOK, map[string]any{"new_url": a.newURL, "restart_required": true})
}

func (s *Server) handleRestartTLS(w http.ResponseWriter, r *http.Request) {
	a := s.access
	a.mu.Lock()
	if !a.saved || a.state == "restarting" {
		a.mu.Unlock()
		auth.WriteError(w, http.StatusConflict, "tls_restart_not_pending", "save a prepared transport change before restarting")
		return
	}
	if !s.tlsRestartAvailable() {
		a.mu.Unlock()
		auth.WriteError(w, http.StatusServiceUnavailable, "tls_manual_required", "restart the panel service manually")
		return
	}
	snapshot, err := s.readTLSSnapshot(a.savedManaged)
	if err != nil || snapshot.Revision != a.savedRevision {
		a.mu.Unlock()
		auth.WriteError(w, http.StatusConflict, "tls_config_changed", "saved configuration changed; inspect it and restart manually")
		return
	}
	name, _ := resolveLogicalService("panel", s.svcMgr.Kind(), s.cfg.Host)
	a.state = "restarting"
	a.err = ""
	newURL := a.newURL
	a.mu.Unlock()
	s.appendAudit(r, "panel.tls.restart", "", "")
	writeJSON(w, http.StatusAccepted, map[string]any{"new_url": newURL, "restart_required": true})
	if err := http.NewResponseController(w).Flush(); err != nil {
		a.mu.Lock()
		a.state = "restart_failed"
		a.err = "tls_restart_failed"
		a.mu.Unlock()
		return
	}
	// Use the existing allowlisted Runner only after delivering the response.
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
		defer cancel()
		_, err := s.runner.Run(ctx, host.Op{Kind: host.OpRestartService, Args: map[string]string{host.ArgService: name}})
		a.mu.Lock()
		defer a.mu.Unlock()
		if err != nil {
			a.state = "restart_failed"
			a.err = "tls_restart_failed"
		}
	}()
}
