package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/nodeagent"
)

func (s *Server) registerAgentConfig(mux *http.ServeMux) {
	configured := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if s.agentConfig == nil {
				auth.WriteError(w, 503, "config_tool_unavailable", "full configuration tool is not configured or unavailable")
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("GET /v1/agent/config", configured(func(w http.ResponseWriter, r *http.Request) {
		result, err := s.agentConfig.Inspect(r.Context())
		writeConfigResult(w, result, err)
	}))
	mux.HandleFunc("POST /v1/agent/config/render", configured(func(w http.ResponseWriter, r *http.Request) {
		request, ok := decodeConfigRequest(w, r)
		if !ok {
			return
		}
		result, err := s.agentConfig.Render(r.Context(), request)
		writeConfigResult(w, result, err)
	}))
	mux.HandleFunc("POST /v1/agent/config/validate", configured(s.agentValidateConfig))
	mux.HandleFunc("POST /v1/agent/config/apply", configured(s.agentApplyConfig))
	mux.HandleFunc("GET /v1/agent/config/backups", configured(func(w http.ResponseWriter, r *http.Request) {
		backups, err := s.agentConfig.Backups()
		if err != nil {
			writeConfigResult(w, nodeagent.ConfigResult{}, err)
			return
		}
		writeJSON(w, 200, backups)
	}))
	mux.HandleFunc("GET /v1/agent/config/backups/{key}", configured(func(w http.ResponseWriter, r *http.Request) {
		backup, err := s.agentConfig.Backup(r.PathValue("key"))
		if err != nil {
			auth.WriteError(w, 404, "config_backup_not_found", "backup not found")
			return
		}
		writeJSON(w, 200, backup)
	}))
	mux.HandleFunc("DELETE /v1/agent/config/backups/{key}", configured(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		result, err := s.agentJournal.Execute(key, "config/backup-delete/"+r.PathValue("key"), func() (int, []byte) {
			if s.agentConfig.DeleteBackup(r.PathValue("key")) != nil {
				return 503, []byte(`{"code":"config_backup_unavailable","message":"backup deletion unconfirmed"}`)
			}
			return 204, nil
		})
		writeAgentCommand(w, key, result, err)
	}))
}
func decodeConfigRequest(w http.ResponseWriter, r *http.Request) (nodeagent.ConfigRequest, bool) {
	var request nodeagent.ConfigRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, nodeagent.MaxConfigRequest))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil || d.Decode(&struct{}{}) != io.EOF || request.Action != "" || len(request.Revision) != 64 || len(request.SourceID) != 64 {
		auth.WriteError(w, 400, "bad_request", "expected revision, source_id and content")
		return request, false
	}
	return request, true
}
func (s *Server) agentValidateConfig(w http.ResponseWriter, r *http.Request) {
	request, ok := decodeConfigRequest(w, r)
	if !ok {
		return
	}
	result, err := s.agentConfig.Validate(r.Context(), request)
	writeConfigResult(w, result, err)
}
func (s *Server) agentApplyConfig(w http.ResponseWriter, r *http.Request) {
	request, ok := decodeConfigRequest(w, r)
	if !ok {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	raw, _ := json.Marshal(request)
	digest := sha256.Sum256(raw)
	result, err := s.agentJournal.Execute(key, "config/apply/"+hex.EncodeToString(digest[:]), func() (int, []byte) {
		result, err := s.agentConfig.Apply(r.Context(), key, request)
		rec := newAgentResponse()
		writeConfigResult(rec, result, err)
		return rec.status, rec.Body.Bytes()
	})
	writeAgentCommand(w, key, result, err)
}
func writeConfigResult(w http.ResponseWriter, result nodeagent.ConfigResult, err error) {
	if err != nil {
		code := "config_tool_unavailable"
		if errors.Is(err, nodeagent.ErrConfigBackup) {
			code = "config_backup_unavailable"
		}
		auth.WriteError(w, 503, code, "configuration operation unavailable; inspect command status before retrying")
		return
	}
	if !result.OK {
		auth.WriteError(w, result.Status, result.Error.Code, "configuration operation rejected")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(result.Data)
}
func writeAgentCommand(w http.ResponseWriter, key string, result nodeagent.CommandResult, err error) {
	if err != nil {
		status, code := 503, "node_agent_unavailable"
		switch {
		case errors.Is(err, nodeagent.ErrKey):
			status, code = 400, "invalid_agent_command_key"
		case errors.Is(err, nodeagent.ErrConflict):
			status, code = 409, "agent_command_conflict"
		case errors.Is(err, nodeagent.ErrUnknown):
			status, code = 409, "agent_command_unconfirmed"
		case errors.Is(err, nodeagent.ErrCapacity):
			status, code = 409, "agent_command_capacity"
		}
		auth.WriteError(w, status, code, code)
		return
	}
	w.Header().Set("Idempotency-Key", key)
	if len(result.Body) > 0 {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(result.Status)
	_, _ = w.Write(result.Body)
}

func (s *Server) handleNodeConfigFile(w http.ResponseWriter, r *http.Request) {
	if s.agentClient == nil {
		nodeOperationUnavailable(w, r)
		return
	}
	path := "/v1/agent/config" + strings.TrimPrefix(r.URL.Path, "/api/telemt/config/files")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, nodeagent.MaxConfigRequest))
	if err != nil {
		auth.WriteError(w, 413, "bad_request", "configuration request too large")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
	defer cancel()
	key := ""
	if (r.Method == http.MethodPost && strings.HasSuffix(path, "/apply")) || r.Method == http.MethodDelete {
		key = r.Header.Get("Idempotency-Key")
		if key == "" {
			key = nodeagent.NewCommandKey()
		}
		w.Header().Set("Idempotency-Key", key)
	}
	res, err := s.agentClient.RequestBody(ctx, r.Method, path, key, body)
	if err != nil {
		auth.WriteError(w, 503, "node_agent_unavailable", "configuration agent unavailable; inspect command status before retrying")
		return
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		auth.WriteError(w, 502, "node_agent_unavailable", "agent rejected authorization")
		return
	}
	response, err := nodeagent.ReadLimitedBody(res, nodeagent.MaxConfigResponse)
	if err != nil {
		auth.WriteError(w, 502, "node_agent_unavailable", "invalid configuration response")
		return
	}
	if r.Method != http.MethodGet {
		s.appendAudit(r, "agent.config", key, "")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(res.StatusCode)
	_, _ = w.Write(response)
}
