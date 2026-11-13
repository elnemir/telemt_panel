package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/nodeinstall"
)

type ingressInstaller interface {
	InstallIngress(context.Context, nodeinstall.IngressRequest, string, string, int64, func(context.Context) bool, func(string)) (int, error)
}

type clusterIngressJob struct {
	Revision int64  `json:"revision"`
	Host     string `json:"host"`
	State    string `json:"state"`
	Code     string `json:"code,omitempty"`
	MaxConn  int    `json:"max_connections,omitempty"`
	input    nodeinstall.IngressRequest
	owner    string
	expires  time.Time
}

type clusterIngressStatus struct {
	Available bool               `json:"available"`
	Job       *clusterIngressJob `json:"job,omitempty"`
}

func (s *Server) handleGetClusterIngressInstall(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.requirePlatform(w) {
		return
	}
	clusterID := r.PathValue("cluster_id")
	if _, err := s.platform.GetCluster(r.Context(), clusterID); err != nil {
		writePlatformError(w, err)
		return
	}
	status := clusterIngressStatus{}
	if j := s.nodeInstallation; j != nil {
		j.mu.Lock()
		defer j.mu.Unlock()
		j.expire()
		_, status.Available = j.runner.(ingressInstaller)
		status.Job = j.ingress[clusterID]
	}
	writeJSON(w, 200, status)
}

func (s *Server) handleStartClusterIngressInstall(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	j, owner := s.installContext(w, r)
	if j == nil {
		return
	}
	runner, ok := j.runner.(ingressInstaller)
	if !ok {
		auth.WriteError(w, 503, "node_install_disabled", "SSH installation is unavailable")
		return
	}
	revision, ok := platformRevision(w, r)
	if !ok {
		return
	}
	var input nodeinstall.IngressRequest
	if decodeJSONBody(w, r, &input, jsonBodyOptions{MaxBytes: 4 << 10, RejectUnknown: true}) != nil || !input.Valid() {
		auth.WriteError(w, 400, "node_install_invalid", "invalid SSH ingress installation request")
		return
	}

	clusterID := r.PathValue("cluster_id")
	export := func(ctx context.Context) (string, error) {
		s.platformMu.RLock()
		defer s.platformMu.RUnlock()
		return s.platform.ExportRouting(ctx, clusterID, revision)
	}
	config, err := export(r.Context())
	if err != nil {
		writePlatformError(w, err)
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.expire()
	if previous := j.ingress[clusterID]; previous != nil && previous.input.RequestID == input.RequestID {
		if previous.input != input || previous.owner != owner || previous.Revision != revision {
			auth.WriteError(w, 409, "node_install_busy", "the request identifier is already in use")
			return
		}
		writeJSON(w, 202, previous)
		return
	}
	if j.closed || j.busy || (j.ingress[clusterID] == nil && len(j.ingress) >= 128) {
		auth.WriteError(w, 409, "node_install_busy", "an SSH operation is already running")
		return
	}
	job := &clusterIngressJob{Revision: revision, Host: input.Host, State: "checking_host", input: input, owner: owner}
	j.ingress[clusterID] = job
	j.busy = true
	j.work.Go(func() {
		ctx, cancel := context.WithTimeout(j.ctx, 8*time.Minute)
		defer cancel()
		capacity, err := runner.InstallIngress(ctx, input, config, clusterID, revision, func(ctx context.Context) bool {
			current, err := export(ctx)
			return err == nil && current == config
		}, func(state string) {
			j.mu.Lock()
			job.State = state
			j.mu.Unlock()
		})
		j.mu.Lock()
		defer j.mu.Unlock()
		job.State = "applied"
		job.MaxConn = capacity
		if err != nil {
			job.State = "failed"
			job.Code = ingressInstallErrorCode(err)
		}
		job.expires = time.Now().Add(time.Hour)
		j.busy = false
	})
	s.appendAudit(r, "cluster.ingress.install", clusterID, "")
	writeJSON(w, 202, job)
}

func ingressInstallErrorCode(err error) string {
	switch err {
	case nodeinstall.ErrIngressStale, nodeinstall.ErrIngressPackages, nodeinstall.ErrIngressConflict, nodeinstall.ErrIngressConfig,
		nodeinstall.ErrIngressCapacity, nodeinstall.ErrIngressRollback:
		return err.Error()
	case nodeinstall.ErrUnconfirmed:
		return "ingress_install_unconfirmed"
	case nodeinstall.ErrUnsupported:
		return "ingress_install_unsupported"
	default:
		return installErrorCode(err)
	}
}
