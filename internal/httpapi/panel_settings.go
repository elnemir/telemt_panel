package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"

	"github.com/amirotin/telemt_panel/internal/atomicfile"
	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/config"
	"github.com/amirotin/telemt_panel/internal/host"
)

type panelSettingsView struct {
	Revision        string               `json:"revision"`
	Active          config.PanelSettings `json:"active"`
	Saved           config.PanelSettings `json:"saved"`
	Startup         config.PanelSettings `json:"startup"`
	Writable        bool                 `json:"writable"`
	SavedValid      bool                 `json:"saved_valid"`
	RestartRequired bool                 `json:"restart_required"`
	RestartCommand  string               `json:"restart_command,omitempty"`
}

type panelSettingsRequest struct {
	Revision string                     `json:"revision"`
	Settings *config.PanelSettingsPatch `json:"settings"`
}

// SetPanelSettingsBase records the validated startup configuration before
// applying overrides. Call once before starting the HTTP server.
func (s *Server) SetPanelSettingsBase(base *config.Config) {
	s.panelSettingsBase = base
	s.panelSettingsEpoch = rand.Text()
}

func (s *Server) panelSettingsView(raw []byte) panelSettingsView {
	base := s.panelSettingsBase
	digest := sha256.Sum256(append([]byte(s.panelSettingsEpoch), raw...))
	v := panelSettingsView{
		Revision: hex.EncodeToString(digest[:]),
		Active:   config.EditablePanelSettings(s.cfg),
		Startup:  config.EditablePanelSettings(base),
		Writable: base.DataDir != "" && s.st.StateDurable(),
	}
	next, err := config.ApplyPanelOverrides(base, raw)
	v.SavedValid = err == nil
	v.Saved = v.Startup
	if v.SavedValid {
		v.Saved = config.EditablePanelSettings(next)
	}
	v.RestartRequired = v.SavedValid && v.Active != v.Saved
	kind := s.svcMgr.Kind()
	switch kind {
	case host.KindSystemd, host.KindOpenRC, host.KindProcd, host.KindSysvinit, host.KindDocker:
		service, _ := resolveLogicalService("panel", kind, s.cfg.Host)
		if service != "" {
			v.RestartCommand = manualRestartCommand(kind, service)
		}
	}
	return v
}

func (s *Server) readPanelSettings(w http.ResponseWriter) (panelSettingsView, bool) {
	w.Header().Set("Cache-Control", "no-store")
	if s.panelSettingsBase == nil {
		auth.WriteError(w, http.StatusForbidden, "panel_config_unavailable", "panel settings editing is not available")
		return panelSettingsView{}, false
	}
	raw, err := config.ReadPanelOverrides(s.panelSettingsBase.DataDir)
	if err != nil {
		slog.Error("read panel settings", "err", err)
		auth.WriteError(w, http.StatusInternalServerError, "panel_config_unavailable", "could not read panel settings")
		return panelSettingsView{}, false
	}
	return s.panelSettingsView(raw), true
}

func (s *Server) handleGetPanelSettings(w http.ResponseWriter, _ *http.Request) {
	s.panelSettingsMu.Lock()
	defer s.panelSettingsMu.Unlock()
	if view, ok := s.readPanelSettings(w); ok {
		writeJSON(w, http.StatusOK, view)
	}
}

func (s *Server) handlePreviewPanelSettings(w http.ResponseWriter, r *http.Request) {
	s.editPanelSettings(w, r, false)
}

func (s *Server) handlePutPanelSettings(w http.ResponseWriter, r *http.Request) {
	s.editPanelSettings(w, r, true)
}

func (s *Server) editPanelSettings(w http.ResponseWriter, r *http.Request, save bool) {
	var body panelSettingsRequest
	if err := decodeJSONBody(w, r, &body, jsonBodyOptions{MaxBytes: 4 << 10, RejectUnknown: true}); err != nil || body.Settings == nil {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "invalid panel settings request")
		return
	}
	s.panelSettingsMu.Lock()
	defer s.panelSettingsMu.Unlock()
	view, ok := s.readPanelSettings(w)
	if !ok {
		return
	}
	if !view.Writable {
		auth.WriteError(w, http.StatusForbidden, "panel_config_unavailable", "durable panel data is required")
		return
	}
	if body.Revision != view.Revision {
		auth.WriteError(w, http.StatusConflict, "panel_config_conflict", "saved panel settings changed")
		return
	}
	base := s.panelSettingsBase
	next, err := body.Settings.Apply(base)
	if err != nil {
		var fieldErr *config.PanelSettingsFieldError
		if errors.As(err, &fieldErr) {
			auth.WriteError(w, http.StatusUnprocessableEntity, "panel_config_"+fieldErr.Field, fieldErr.Error())
			return
		}
		auth.WriteError(w, http.StatusUnprocessableEntity, "panel_config_invalid", "could not validate panel settings")
		return
	}
	if !save {
		writeJSON(w, http.StatusOK, config.EditablePanelSettings(next))
		return
	}

	encoded, err := config.EncodePanelOverrides(base, next)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "internal_error", "could not encode panel settings")
		return
	}
	path := filepath.Join(base.DataDir, config.PanelOverridesName)
	published, err := atomicfile.Write(path, 0600, func(w io.Writer) error {
		_, err := w.Write(encoded)
		return err
	})
	if err != nil {
		slog.Error("save panel settings", "err", err)
		if published {
			auth.WriteError(w, http.StatusInternalServerError, "panel_config_write_unconfirmed", "settings publication durability could not be confirmed")
		} else {
			auth.WriteError(w, http.StatusInternalServerError, "internal_error", "could not save panel settings")
		}
		return
	}
	s.appendAudit(r, "panel.config.save", "panel", "")
	writeJSON(w, http.StatusOK, s.panelSettingsView(encoded))
}
