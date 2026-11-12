package httpapi

import (
	"context"
	"net/http"

	"github.com/amirotin/telemt_panel/internal/platform"
	"github.com/amirotin/telemt_panel/internal/telemt"
)

type clusterConfigSource struct {
	SourceNodeID   string   `json:"source_node_id"`
	Fields         []string `json:"fields"`
	SourceRevision string   `json:"source_revision,omitempty"`
}

type clusterConfigPreview struct {
	SourceRevision string                       `json:"source_revision"`
	Settings       platform.ClusterConfigValues `json:"settings"`
}

func (s *Server) handleGetClusterConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	data, err := s.platform.ClusterConfig(r.Context(), r.PathValue("cluster_id"))
	if err != nil {
		writePlatformError(w, err)
		return
	}
	platformETag(w, data.Revision)
	writeJSON(w, http.StatusOK, data)
}

func (s *Server) readClusterConfigSource(ctx context.Context, clusterID string, in clusterConfigSource) (clusterConfigPreview, error) {
	if _, err := s.platform.GetCluster(ctx, clusterID); err != nil {
		return clusterConfigPreview{}, err
	}
	members, err := s.platform.ListMembers(ctx, clusterID)
	if err != nil {
		return clusterConfigPreview{}, err
	}
	active := false
	for _, member := range members {
		if member.State == "active" && member.NodeID == in.SourceNodeID {
			active = true
			break
		}
	}
	if !active {
		return clusterConfigPreview{}, platform.ErrConflict
	}
	var preview clusterConfigPreview
	err = s.withPlatformNode(ctx, in.SourceNodeID, func(ctx context.Context, node platform.NodeClient) error {
		client, ok := node.(*telemt.Client)
		if !ok {
			return platform.ErrUnavailable
		}
		sections, revision, err := client.GetConfig(ctx)
		if err != nil {
			return platform.ErrUnavailable
		}
		if revision == "" || (in.SourceRevision != "" && in.SourceRevision != revision) {
			return platform.ErrConflict
		}
		preview.SourceRevision = revision
		preview.Settings, err = platform.ReadClusterConfigValues(sections, in.Fields)
		return err
	})
	return preview, err
}

func (s *Server) handlePreviewClusterConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	var in clusterConfigSource
	if decodeJSONBody(w, r, &in, jsonBodyOptions{MaxBytes: 4096, RejectUnknown: true}) != nil || in.SourceRevision != "" {
		writePlatformError(w, platform.ErrInvalid)
		return
	}
	data, err := s.readClusterConfigSource(r.Context(), r.PathValue("cluster_id"), in)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func (s *Server) handlePutClusterConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	revision, ok := platformRevision(w, r)
	if !ok {
		return
	}
	var in clusterConfigSource
	if decodeJSONBody(w, r, &in, jsonBodyOptions{MaxBytes: 4096, RejectUnknown: true}) != nil || in.SourceRevision == "" {
		writePlatformError(w, platform.ErrInvalid)
		return
	}
	clusterID := r.PathValue("cluster_id")
	preview, err := s.readClusterConfigSource(r.Context(), clusterID, in)
	if err == nil {
		err = s.platform.PutClusterConfig(r.Context(), clusterID, revision, in.SourceNodeID, preview.Settings)
	}
	if err != nil {
		writePlatformError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleDeleteClusterConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	revision, ok := platformRevision(w, r)
	if !ok {
		return
	}
	if err := s.platform.DeleteClusterConfig(r.Context(), r.PathValue("cluster_id"), revision); err != nil {
		writePlatformError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
