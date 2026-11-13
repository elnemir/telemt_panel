package httpapi

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/nodes"
	"github.com/amirotin/telemt_panel/internal/platform"
)

func (s *Server) registerPlatformRoutes(mux *http.ServeMux, protect func(http.HandlerFunc) http.Handler) {
	mux.Handle("GET /api/platform", protect(s.handlePlatformStatus))
	mux.Handle("GET /api/clusters", protect(s.handleListClusters))
	mux.Handle("POST /api/clusters", protect(s.handleCreateCluster))
	mux.Handle("GET /api/clusters/{cluster_id}/routing", protect(s.handleGetClusterRouting))
	mux.Handle("PUT /api/clusters/{cluster_id}/routing", protect(s.handlePutClusterRouting))
	mux.Handle("GET /api/clusters/{cluster_id}/routing/export", protect(s.handleExportClusterRouting))
	mux.Handle("GET /api/clusters/{cluster_id}/routing/suggestions", protect(s.handleClusterRoutingSuggestions))
	mux.Handle("GET /api/clusters/{cluster_id}/routing/install", protect(s.handleGetClusterIngressInstall))
	mux.Handle("POST /api/clusters/{cluster_id}/routing/install", protect(s.handleStartClusterIngressInstall))
	mux.Handle("GET /api/clusters/{cluster_id}", protect(s.handleGetCluster))
	mux.Handle("PUT /api/clusters/{cluster_id}", protect(s.handleRenameCluster))
	mux.Handle("DELETE /api/clusters/{cluster_id}", protect(s.handleDeleteCluster))
	mux.Handle("GET /api/clusters/{cluster_id}/configuration", protect(s.handleGetClusterConfig))
	mux.Handle("POST /api/clusters/{cluster_id}/configuration/preview", protect(s.handlePreviewClusterConfig))
	mux.Handle("PUT /api/clusters/{cluster_id}/configuration", protect(s.handlePutClusterConfig))
	mux.Handle("DELETE /api/clusters/{cluster_id}/configuration", protect(s.handleDeleteClusterConfig))
	mux.Handle("POST /api/clusters/{cluster_id}/members", protect(s.handleAddClusterMember))
	mux.Handle("DELETE /api/clusters/{cluster_id}/members/{member_id}", protect(s.handleRemoveClusterMember))
	mux.Handle("GET /api/clusters/{cluster_id}/identities", protect(s.handleListIdentities))
	mux.Handle("POST /api/clusters/{cluster_id}/identities", protect(s.handleCreateIdentity))
	mux.Handle("GET /api/identities/{identity_id}", protect(s.handleGetIdentity))
	mux.Handle("PUT /api/identities/{identity_id}", protect(s.handleUpdateIdentity))
	mux.Handle("POST /api/identities/{identity_id}/retry", protect(s.handleRetryIdentity))
}
func writePlatformError(w http.ResponseWriter, e error) {
	status, code := 503, "platform_unavailable"
	switch {
	case errors.Is(e, platform.ErrNotFound):
		status, code = 404, "platform_not_found"
	case errors.Is(e, platform.ErrInvalid):
		status, code = 400, "platform_invalid"
	case errors.Is(e, platform.ErrConflict):
		status, code = 409, "platform_conflict"
	}
	auth.WriteError(w, status, code, code)
}
func (s *Server) requirePlatform(w http.ResponseWriter) bool {
	w.Header().Set("Cache-Control", "no-store")
	if s.platform == nil {
		writePlatformError(w, platform.ErrUnavailable)
		return false
	}
	return true
}
func platformRevision(w http.ResponseWriter, r *http.Request) (int64, bool) {
	n, ok := nodeMatch(w, r)
	if !ok {
		return 0, false
	}
	if n > math.MaxInt64 {
		writePlatformError(w, platform.ErrInvalid)
		return 0, false
	}
	return int64(n), true
}
func platformETag(w http.ResponseWriter, revision int64) {
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(revision, 10)))
	w.Header().Set("Cache-Control", "no-store")
}
func (s *Server) handlePlatformStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]bool{"available": platform.Available, "enabled": s.platform != nil})
}
func (s *Server) handleListClusters(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	list, e := s.platform.ListClusters(r.Context())
	if e != nil {
		writePlatformError(w, e)
		return
	}
	writeJSON(w, 200, list)
}
func (s *Server) handleCreateCluster(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if decodeJSONBody(w, r, &body, jsonBodyOptions{MaxBytes: 1024, RejectUnknown: true}) != nil {
		writePlatformError(w, platform.ErrInvalid)
		return
	}
	c, e := s.platform.CreateCluster(r.Context(), body.Name)
	if e != nil {
		writePlatformError(w, e)
		return
	}
	platformETag(w, c.Revision)
	writeJSON(w, 201, c)
}

type clusterDetail struct {
	Cluster platform.Cluster  `json:"cluster"`
	Members []platform.Member `json:"members"`
}

func (s *Server) handleGetCluster(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	s.platformMu.RLock()
	defer s.platformMu.RUnlock()
	c, e := s.platform.GetCluster(r.Context(), r.PathValue("cluster_id"))
	if e != nil {
		writePlatformError(w, e)
		return
	}
	members, e := s.platform.ListMembers(r.Context(), c.ID)
	if e != nil {
		writePlatformError(w, e)
		return
	}
	platformETag(w, c.Revision)
	writeJSON(w, 200, clusterDetail{c, members})
}
func (s *Server) handleRenameCluster(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	rev, ok := platformRevision(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if decodeJSONBody(w, r, &body, jsonBodyOptions{MaxBytes: 1024, RejectUnknown: true}) != nil {
		writePlatformError(w, platform.ErrInvalid)
		return
	}
	s.platformMu.Lock()
	defer s.platformMu.Unlock()
	if e := s.platform.RenameCluster(r.Context(), r.PathValue("cluster_id"), rev, body.Name); e != nil {
		writePlatformError(w, e)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) handleDeleteCluster(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	rev, ok := platformRevision(w, r)
	if !ok {
		return
	}
	s.platformMu.Lock()
	defer s.platformMu.Unlock()
	if e := s.platform.DeleteEmptyCluster(r.Context(), r.PathValue("cluster_id"), rev); e != nil {
		writePlatformError(w, e)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) handleAddClusterMember(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) || !s.requireNodes(w) {
		return
	}
	rev, ok := platformRevision(w, r)
	if !ok {
		return
	}
	var body struct {
		NodeID string `json:"node_id"`
	}
	if decodeJSONBody(w, r, &body, jsonBodyOptions{MaxBytes: 1024, RejectUnknown: true}) != nil {
		writePlatformError(w, platform.ErrInvalid)
		return
	}
	s.platformMu.Lock()
	defer s.platformMu.Unlock()
	if _, _, e := s.nodes.Resolve(body.NodeID); e != nil {
		writeNodeError(w, e)
		return
	}
	if e := s.platform.AddMember(r.Context(), r.PathValue("cluster_id"), rev, body.NodeID); e != nil {
		writePlatformError(w, e)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) handleRemoveClusterMember(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	rev, ok := platformRevision(w, r)
	if !ok {
		return
	}
	s.platformMu.Lock()
	defer s.platformMu.Unlock()
	if e := s.platform.RemoveMember(r.Context(), r.PathValue("cluster_id"), rev, r.PathValue("member_id")); e != nil {
		writePlatformError(w, e)
		return
	}
	w.WriteHeader(202)
}
func (s *Server) handleListIdentities(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 0 {
			writePlatformError(w, platform.ErrInvalid)
			return
		}
		offset = n
	}
	if _, e := s.platform.GetCluster(r.Context(), r.PathValue("cluster_id")); e != nil {
		writePlatformError(w, e)
		return
	}
	list, e := s.platform.ListIdentities(r.Context(), r.PathValue("cluster_id"), offset, 100)
	if e != nil {
		writePlatformError(w, e)
		return
	}
	writeJSON(w, 200, list)
}
func (s *Server) handleCreateIdentity(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	rev, ok := platformRevision(w, r)
	if !ok {
		return
	}
	var body struct {
		Username string               `json:"username"`
		Secret   string               `json:"secret"`
		Policy   *identityPolicyInput `json:"policy"`
	}
	if decodeJSONBody(w, r, &body, jsonBodyOptions{MaxBytes: 8192, RejectUnknown: true}) != nil || body.Policy == nil || body.Policy.Enabled == nil {
		writePlatformError(w, platform.ErrInvalid)
		return
	}
	s.platformMu.Lock()
	defer s.platformMu.Unlock()
	i, e := s.platform.CreateIdentity(r.Context(), r.PathValue("cluster_id"), rev, platform.IdentityInput{Username: body.Username, Secret: body.Secret, Policy: body.Policy.value()})
	if e != nil {
		writePlatformError(w, e)
		return
	}
	platformETag(w, i.Revision)
	writeJSON(w, 201, i)
}

type identityDetail struct {
	Identity platform.Identity `json:"identity"`
	Jobs     []platform.Job    `json:"jobs"`
}

func (s *Server) handleGetIdentity(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	s.platformMu.RLock()
	defer s.platformMu.RUnlock()
	i, e := s.platform.GetIdentity(r.Context(), r.PathValue("identity_id"))
	if e != nil {
		writePlatformError(w, e)
		return
	}
	jobs, e := s.platform.IdentityJobs(r.Context(), i.ID)
	if e != nil {
		writePlatformError(w, e)
		return
	}
	platformETag(w, i.Revision)
	writeJSON(w, 200, identityDetail{i, jobs})
}
func (s *Server) handleUpdateIdentity(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	rev, ok := platformRevision(w, r)
	if !ok {
		return
	}
	var body struct {
		Policy *identityPolicyInput `json:"policy"`
		Rotate bool                 `json:"rotate_secret"`
		Revoke bool                 `json:"revoke"`
	}
	if decodeJSONBody(w, r, &body, jsonBodyOptions{MaxBytes: 8192, RejectUnknown: true}) != nil || body.Policy == nil || body.Policy.Enabled == nil {
		writePlatformError(w, platform.ErrInvalid)
		return
	}
	s.platformMu.Lock()
	defer s.platformMu.Unlock()
	if e := s.platform.UpdateIdentity(r.Context(), r.PathValue("identity_id"), rev, body.Policy.value(), body.Rotate, body.Revoke); e != nil {
		writePlatformError(w, e)
		return
	}
	w.WriteHeader(202)
}
func (s *Server) handleRetryIdentity(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	rev, ok := platformRevision(w, r)
	if !ok {
		return
	}
	if e := s.platform.RetryIdentity(r.Context(), r.PathValue("identity_id"), rev); e != nil {
		writePlatformError(w, e)
		return
	}
	w.WriteHeader(202)
}

func (s *Server) guardManagedUser(w http.ResponseWriter, r *http.Request, name string) (func(), bool) {
	if s.platform == nil {
		return func() {}, true
	}
	s.platformMu.RLock()
	owned, e := s.platform.ManagedUser(r.Context(), s.platformNodeID, name)
	if e != nil || owned {
		s.platformMu.RUnlock()
		if e != nil {
			writePlatformError(w, e)
		} else {
			auth.WriteError(w, 409, "user_cluster_owned", "manage this user through its cluster identity")
		}
		return func() {}, false
	}
	return s.platformMu.RUnlock, true
}
func (s *Server) withPlatformNode(ctx context.Context, id string, run func(context.Context, platform.NodeClient) error) error {
	if s.nodes == nil {
		return nodes.ErrNotFound
	}
	if id == nodes.DefaultID {
		_, client, e := s.nodes.Resolve(id)
		if e != nil {
			return e
		}
		defer s.pokeUsersAfterMutation()
		return run(ctx, client)
	}
	rt, release, e := s.acquireNodeRuntime(id)
	if e != nil {
		return e
	}
	defer release()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(rt.ctx, cancel)
	defer stop()
	if rt.ctx.Err() != nil {
		return nodes.ErrDisabled
	}
	defer rt.server.pokeUsersAfterMutation()
	return run(ctx, rt.client)
}

type identityPolicyInput struct {
	platform.Policy
	Enabled *bool `json:"enabled"`
}

func (p identityPolicyInput) value() platform.Policy { v := p.Policy; v.Enabled = *p.Enabled; return v }
