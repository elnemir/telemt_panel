package httpapi

import (
	"github.com/amirotin/telemt_panel/internal/ingress"
	"github.com/amirotin/telemt_panel/internal/platform"
	"net/http"
)

func (s *Server) handleGetClusterRouting(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	out, e := s.platform.GetRouting(r.Context(), r.PathValue("cluster_id"))
	if e != nil {
		writePlatformError(w, e)
		return
	}
	platformETag(w, out.Revision)
	writeJSON(w, 200, out)
}
func (s *Server) handlePutClusterRouting(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) || !s.requireNodes(w) {
		return
	}
	revision, ok := platformRevision(w, r)
	if !ok {
		return
	}
	var cfg ingress.Config
	if decodeJSONBody(w, r, &cfg, jsonBodyOptions{MaxBytes: 32768, RejectUnknown: true}) != nil || cfg.Validate() != nil {
		writePlatformError(w, platform.ErrInvalid)
		return
	}
	s.platformMu.Lock()
	defer s.platformMu.Unlock()
	for _, t := range cfg.Targets {
		if _, _, e := s.nodes.Resolve(t.NodeID); e != nil {
			writePlatformError(w, platform.ErrConflict)
			return
		}
	}
	if e := s.platform.PutRouting(r.Context(), r.PathValue("cluster_id"), revision, cfg); e != nil {
		writePlatformError(w, e)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) handleExportClusterRouting(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	revision, ok := platformRevision(w, r)
	if !ok {
		return
	}
	s.platformMu.RLock()
	defer s.platformMu.RUnlock()
	raw, e := s.platform.ExportRouting(r.Context(), r.PathValue("cluster_id"), revision)
	if e != nil {
		writePlatformError(w, e)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="haproxy.cfg"`)
	platformETag(w, revision)
	w.WriteHeader(200)
	_, _ = w.Write([]byte(raw))
}
