package httpapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/nodeagent"
)

func (s *Server) agentHandler() http.Handler {
	mux := http.NewServeMux()
	s.registerAgentConfig(mux)
	mux.HandleFunc("GET /v1/agent/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"agent_id": s.cfg.Agent.ID, "version": s.version})
	})
	mux.HandleFunc("GET /v1/agent/host", s.agentHost)
	mux.HandleFunc("GET /v1/agent/service", s.handleTelemtService)
	mux.HandleFunc("POST /v1/agent/service/{action}", s.agentServiceAction)
	mux.HandleFunc("GET /v1/agent/commands/{key}", func(w http.ResponseWriter, r *http.Request) {
		result, ok := s.agentJournal.Get(r.PathValue("key"))
		if !ok {
			auth.WriteError(w, 404, "not_found", "command not found")
			return
		}
		writeJSON(w, 200, result)
	})
	logs := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("service") != "telemt" {
				auth.WriteError(w, 400, "bad_request", "agent exposes Telemt logs only")
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("GET /v1/agent/logs/tail", logs(s.handleLogsTail))
	mux.HandleFunc("HEAD /v1/agent/events/logs", logs(s.handleProbeLogStream))
	mux.HandleFunc("GET /v1/agent/events/logs", logs(s.handleEventsLogs))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set(nodeagent.IdentityHeader, s.cfg.Agent.ID)
		expected := os.Getenv(s.cfg.Agent.AuthEnv)
		if s.agentJournal == nil || len(expected) < 32 || strings.ContainsAny(expected, "\r\n") {
			auth.WriteError(w, 503, "node_agent_unavailable", "agent configuration unavailable")
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(expected)) != 1 {
			auth.WriteError(w, 401, "unauthorized", "agent authorization required")
			return
		}
		if r.Header.Get(nodeagent.IdentityHeader) != s.cfg.Agent.ID {
			auth.WriteError(w, 409, "node_agent_identity_mismatch", "agent identity mismatch")
			return
		}
		// No endpoint accepts caller-defined action arguments.
		limit := int64(4096)
		isConfig := strings.HasPrefix(r.URL.Path, "/v1/agent/config/")
		if isConfig {
			limit = nodeagent.MaxConfigRequest
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
		if err != nil {
			auth.WriteError(w, 413, "bad_request", "request too large")
			return
		}
		if !isConfig && len(bytes.TrimSpace(body)) != 0 && string(bytes.TrimSpace(body)) != "{}" {
			auth.WriteError(w, 400, "bad_request", "agent actions accept no arguments")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) agentHost(w http.ResponseWriter, r *http.Request) {
	rec := newAgentResponse()
	s.handleHost(rec, r)
	var result hostInfo
	if json.Unmarshal(rec.Body.Bytes(), &result) != nil {
		auth.WriteError(w, 500, "internal_error", "invalid host result")
		return
	}
	result.Caps.ConfigFile = s.agentConfig != nil
	result.Caps.RestartPanel = false
	result.Caps.SelfUpdate = false
	delete(result.ManualCommands, "restart_panel")
	delete(result.ManualCommands, "self_update")
	writeJSON(w, 200, result)
}

func (s *Server) agentServiceAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if action != "start" && action != "stop" && action != "restart" {
		auth.WriteError(w, 404, "not_found", "unknown service action")
		return
	}
	panelName, _ := resolveLogicalService("panel", s.svcMgr.Kind(), s.cfg.Host)
	if !serviceBindingsDiffer(s.svcMgr.Kind(), s.telemtServiceName, panelName) {
		auth.WriteError(w, 409, "agent_binding_conflict", "Telemt and agent service bindings must differ")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	result, err := s.agentJournal.Execute(key, "service/"+action, func() (int, []byte) {
		rec := newAgentResponse()
		s.handleServiceAction(rec, r, action)
		return rec.status, rec.Body.Bytes()
	})
	writeAgentCommand(w, key, result, err)
}

// remoteAgent never falls back to the central host, and never forwards an
// agent's auth failure as a panel-session 401 (which would log out the admin).
func (s *Server) remoteAgent(w http.ResponseWriter, r *http.Request, path string, stream bool) {
	if s.agentClient == nil {
		nodeOperationUnavailable(w, r)
		return
	}
	base := r.Context()
	var tracked *sessionStream
	if stream {
		var release func()
		var ok bool
		tracked, release, ok = s.trackSessionStream(w, r)
		if !ok {
			return
		}
		defer release()
		base = tracked.ctx
	}
	ctx, cancel := context.WithCancel(base)
	defer cancel()
	if stream {
		hash, _ := auth.SessionIDHashFromContext(r.Context())
		deregister, err := s.logStreams.TryRegister(hash, cancel)
		if err != nil {
			writeLogAdmissionError(w, err)
			return
		}
		defer deregister()
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			tick := time.NewTicker(sessionStreamCheckInterval)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					if tracked.check() != nil {
						cancel()
						return
					}
				}
			}
		}()
		defer func() { cancel(); <-finished }()
	}
	if !stream {
		var bounded context.CancelFunc
		ctx, bounded = context.WithTimeout(ctx, 35*time.Second)
		defer bounded()
	}
	key := ""
	if r.Method == "POST" {
		key = r.Header.Get("Idempotency-Key")
		if key == "" {
			key = nodeagent.NewCommandKey()
		}
		w.Header().Set("Idempotency-Key", key)
	}
	res, err := s.agentClient.Request(ctx, r.Method, path, key)
	if err != nil {
		auth.WriteError(w, 503, "node_agent_unavailable", "node agent is unavailable; inspect operation status before retrying mutations")
		return
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		auth.WriteError(w, 502, "node_agent_unavailable", "agent rejected authorization")
		return
	}
	if stream && res.StatusCode == 200 {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(200)
		rc := http.NewResponseController(w)
		_ = rc.Flush()
		buf := make([]byte, 16*1024)
		for {
			n, err := res.Body.Read(buf)
			if n > 0 {
				if tracked.check() != nil {
					return
				}
				extendSSEWriteDeadline(rc)
				if _, e := w.Write(buf[:n]); e != nil {
					return
				}
				if rc.Flush() != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	body, err := nodeagent.ReadBody(res)
	if err != nil {
		auth.WriteError(w, 502, "node_agent_unavailable", "invalid agent response")
		return
	}
	if r.Method == "POST" {
		s.appendAudit(r, "agent.command", key, "")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(res.StatusCode)
	_, _ = w.Write(body)
}

func (s *Server) handleNodeService(w http.ResponseWriter, r *http.Request) {
	s.remoteAgent(w, r, "/v1/agent/service", false)
}
func (s *Server) handleNodeServiceAction(w http.ResponseWriter, r *http.Request) {
	action := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	s.remoteAgent(w, r, "/v1/agent/service/"+action, false)
}
func (s *Server) handleNodeAgentLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("service") != "telemt" {
		auth.WriteError(w, 400, "bad_request", "remote scope exposes Telemt logs only")
		return
	}
	path := "/v1/agent/logs/tail"
	stream := false
	if strings.HasSuffix(r.URL.Path, "/events/logs") {
		path = "/v1/agent/events/logs"
		stream = r.Method == http.MethodGet
	}
	s.remoteAgent(w, r, path+"?"+url.Values(q).Encode(), stream)
}

// Captures the bounded JSON result of existing typed handlers without exposing
// their headers or introducing a second implementation of host operations.
type agentResponse struct {
	header http.Header
	status int
	Body   bytes.Buffer
}

func newAgentResponse() *agentResponse          { return &agentResponse{header: make(http.Header), status: 200} }
func (r *agentResponse) Header() http.Header    { return r.header }
func (r *agentResponse) WriteHeader(status int) { r.status = status }
func (r *agentResponse) Write(body []byte) (int, error) {
	if r.Body.Len()+len(body) > 2*1024*1024 {
		return 0, nodeagent.ErrResponse
	}
	return r.Body.Write(body)
}

func (s *Server) handleNodeAgentCommand(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !nodeagent.ValidCommandKeySyntax(key) {
		auth.WriteError(w, 400, "invalid_agent_command_key", "invalid command key")
		return
	}
	s.remoteAgent(w, r, "/v1/agent/commands/"+key, false)
}
