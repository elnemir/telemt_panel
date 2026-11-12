package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/nodes"
	"github.com/amirotin/telemt_panel/internal/platform"
)

func (s *Server) requireNodes(w http.ResponseWriter) bool {
	if s.nodes != nil {
		return true
	}
	auth.WriteError(w, http.StatusServiceUnavailable, "node_registry_unavailable", "node registry is unavailable")
	return false
}

func writeNodeError(w http.ResponseWriter, err error) {
	status, code := http.StatusServiceUnavailable, "node_registry_unavailable"
	switch {
	case errors.Is(err, nodes.ErrNotFound):
		status, code = http.StatusNotFound, "node_not_found"
	case errors.Is(err, nodes.ErrDisabled):
		status, code = http.StatusConflict, "node_disabled"
	case errors.Is(err, nodes.ErrConflict):
		status, code = http.StatusConflict, "node_revision_conflict"
	case errors.Is(err, nodes.ErrProtected):
		status, code = http.StatusConflict, "node_configuration_managed"
	case errors.Is(err, nodes.ErrCredentials):
		status, code = http.StatusServiceUnavailable, "node_credentials_unavailable"
	case errors.Is(err, nodes.ErrInvalid):
		status, code = http.StatusBadRequest, "invalid_node"
	case errors.Is(err, nodes.ErrLimit):
		status, code = http.StatusConflict, "node_registry_full"
	case errors.Is(err, nodes.ErrStorage):
		status, code = http.StatusServiceUnavailable, "node_persistence_failed"
	}
	// Never serialize raw persistence, URL, environment or upstream errors.
	auth.WriteError(w, status, code, code)
}

func nodeETag(w http.ResponseWriter, n nodes.Node) {
	w.Header().Set("ETag", strconv.Quote(strconv.FormatUint(n.Revision, 10)))
	w.Header().Set("Cache-Control", "no-store")
}

func nodeMatch(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	values := r.Header.Values("If-Match")
	if len(values) == 0 {
		auth.WriteError(w, http.StatusPreconditionRequired, "node_revision_required", "If-Match is required")
		return 0, false
	}
	value := strings.TrimSpace(values[0])
	if len(value) >= 2 && strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"") {
		value = value[1 : len(value)-1]
	}
	revision, err := strconv.ParseUint(value, 10, 64)
	if len(values) != 1 || err != nil || revision == 0 {
		auth.WriteError(w, http.StatusBadRequest, "invalid_node_revision", "If-Match must contain one positive revision")
		return 0, false
	}
	return revision, true
}

func decodeNodeInput(w http.ResponseWriter, r *http.Request) (nodes.Input, bool) {
	var body struct {
		nodes.Input
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSONBody(w, r, &body, jsonBodyOptions{MaxBytes: 32 * 1024, RejectUnknown: true}); err != nil || body.Enabled == nil {
		auth.WriteError(w, http.StatusBadRequest, "invalid_node", "name, api_url and enabled are required; unknown fields are not allowed")
		return nodes.Input{}, false
	}
	body.Input.Enabled = *body.Enabled
	return body.Input, true
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	if !s.requireNodes(w) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, struct {
		Nodes        []nodes.Node `json:"nodes"`
		StateDurable bool         `json:"state_durable"`
	}{s.nodes.List(), s.nodes.Durable()})
}

func (s *Server) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	if !s.requireNodes(w) {
		return
	}
	input, ok := decodeNodeInput(w, r)
	if !ok {
		return
	}
	n, err := s.nodes.Create(input)
	if err != nil {
		writeNodeError(w, err)
		return
	}
	s.appendAudit(r, "node.create", n.ID, "")
	nodeETag(w, n)
	writeJSON(w, http.StatusCreated, n)
}

func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	if !s.requireNodes(w) {
		return
	}
	n, err := s.nodes.Get(r.PathValue("node_id"))
	if err != nil {
		writeNodeError(w, err)
		return
	}
	nodeETag(w, n)
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) handleReplaceNode(w http.ResponseWriter, r *http.Request) {
	if !s.requireNodes(w) {
		return
	}
	revision, ok := nodeMatch(w, r)
	if !ok {
		return
	}
	input, ok := decodeNodeInput(w, r)
	if !ok {
		return
	}
	n, err := s.nodes.Replace(r.PathValue("node_id"), revision, input)
	if err != nil {
		writeNodeError(w, err)
		return
	}
	s.refreshNode(n.ID)
	s.appendAudit(r, "node.replace", n.ID, "")
	nodeETag(w, n)
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	if !s.requireNodes(w) {
		return
	}
	revision, ok := nodeMatch(w, r)
	if !ok {
		return
	}
	id := r.PathValue("node_id")
	if err := s.deleteRegisteredNode(r.Context(), id, revision); err != nil {
		if errors.Is(err, platform.ErrConflict) {
			auth.WriteError(w, 409, "node_cluster_owned", "remove cluster membership before deleting the node")
		} else if errors.Is(err, platform.ErrUnavailable) {
			writePlatformError(w, err)
		} else {
			writeNodeError(w, err)
		}
		return
	}

	s.refreshNode(id)
	s.appendAudit(r, "node.delete", id, "")
	w.WriteHeader(http.StatusNoContent)
}

// handleNodeRead deliberately does not bind the legacy Store/Hub, subpage,
// quota scheduler or host adapters to a selected node.
func (s *Server) handleNodeRead(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.requireNodes(w) {
			return
		}
		n, client, err := s.nodes.Resolve(r.PathValue("node_id"))
		if err != nil {
			writeNodeError(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		var data any
		var revision string
		switch kind {
		case "health":
			data, err = client.Health(ctx)
		case "users":
			data, revision, err = client.UsersWithRevision(ctx)
		case "config":
			data, revision, err = client.GetConfig(ctx)
		default:
			auth.WriteError(w, http.StatusNotFound, "not_found", "unknown node operation")
			return
		}
		if !s.nodes.Current(n) {
			writeNodeError(w, nodes.ErrConflict)
			return
		}
		if err != nil {
			status, code := http.StatusBadGateway, "node_upstream_unavailable"
			if errors.Is(err, context.DeadlineExceeded) {
				status, code = http.StatusGatewayTimeout, "node_upstream_timeout"
			}
			auth.WriteError(w, status, code, "Telemt did not return a successful response")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, struct {
			NodeID       string `json:"node_id"`
			NodeRevision uint64 `json:"node_revision"`
			Revision     string `json:"revision,omitempty"`
			Data         any    `json:"data"`
		}{n.ID, n.Revision, revision, data})
	}
}

// Release the topology lock before refreshNode waits for runtime leases. A
// leased request can itself be waiting to read cluster ownership.
func (s *Server) deleteRegisteredNode(ctx context.Context, id string, revision uint64) error {
	if s.platform != nil {
		s.platformMu.Lock()
		defer s.platformMu.Unlock()
		cluster, e := s.platform.NodeCluster(ctx, id)
		if e != nil {
			return platform.ErrUnavailable
		}
		if cluster != "" {
			return platform.ErrConflict
		}
	}
	return s.nodes.Delete(id, revision)
}
