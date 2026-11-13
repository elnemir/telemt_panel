package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"

	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/nodeinstall"
	"github.com/amirotin/telemt_panel/internal/nodes"
)

type NodeInstaller interface {
	Preview(context.Context, nodeinstall.Request) (nodeinstall.Plan, error)
	Apply(context.Context, nodeinstall.Plan, *nodes.Registry, func(string)) (string, error)
}

type nodeInstallJob struct {
	ID      string           `json:"id"`
	Plan    nodeinstall.Plan `json:"plan"`
	State   string           `json:"state"`
	Code    string           `json:"code,omitempty"`
	NodeID  string           `json:"node_id,omitempty"`
	owner   string
	expires time.Time
}

type nodeInstallJobs struct {
	mu            sync.Mutex
	work          sync.WaitGroup
	runner        NodeInstaller
	ctx           context.Context
	cancel        context.CancelFunc
	jobs          map[string]*nodeInstallJob
	upgrades      map[string]*nodeUpgradeJob
	ingress       map[string]*clusterIngressJob
	nodeWrites    map[string]int
	upgradingNode string
	busy          bool
	closed        bool
}

func newNodeInstallJobs(parent context.Context, runner NodeInstaller) *nodeInstallJobs {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &nodeInstallJobs{runner: runner, ctx: ctx, cancel: cancel, jobs: make(map[string]*nodeInstallJob),
		upgrades: make(map[string]*nodeUpgradeJob), ingress: make(map[string]*clusterIngressJob), nodeWrites: make(map[string]int)}
}

func (j *nodeInstallJobs) close() {
	j.mu.Lock()
	j.closed = true
	j.cancel()
	j.mu.Unlock()
	j.work.Wait()
}

func (j *nodeInstallJobs) expire() {
	for id, job := range j.jobs {
		if !job.expires.IsZero() && time.Now().After(job.expires) {
			delete(j.jobs, id)
		}
	}
	for id, job := range j.upgrades {
		if !job.expires.IsZero() && time.Now().After(job.expires) {
			delete(j.upgrades, id)
		}
	}
	for id, job := range j.ingress {
		if !job.expires.IsZero() && time.Now().After(job.expires) {
			delete(j.ingress, id)
		}
	}
}

func (s *Server) installContext(w http.ResponseWriter, r *http.Request) (*nodeInstallJobs, string) {
	w.Header().Set("Cache-Control", "no-store")
	owner, _ := auth.UsernameFromContext(r.Context())
	if s.nodeInstallation == nil || owner == "" {
		auth.WriteError(w, http.StatusServiceUnavailable, "node_install_disabled", "SSH node installation is unavailable")
		return nil, ""
	}
	return s.nodeInstallation, owner
}

func (s *Server) handlePreviewNodeInstall(w http.ResponseWriter, r *http.Request) {
	j, owner := s.installContext(w, r)
	if j == nil {
		return
	}
	request := nodeinstall.Request{WithAgent: true}
	if decodeJSONBody(w, r, &request, jsonBodyOptions{MaxBytes: 4 << 10, RejectUnknown: true}) != nil {
		auth.WriteError(w, 400, "node_install_invalid", "invalid node installation request")
		return
	}
	j.mu.Lock()
	j.expire()
	if j.closed || j.busy || len(j.jobs) >= 32 {
		j.mu.Unlock()
		auth.WriteError(w, 409, "node_install_busy", "an installation or preview is already running")
		return
	}
	j.busy = true
	j.work.Add(1)
	j.mu.Unlock()
	defer j.work.Done()

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	stop := context.AfterFunc(j.ctx, cancel)
	defer stop()
	plan, err := j.runner.Preview(ctx, request)
	j.mu.Lock()
	defer j.mu.Unlock()
	j.busy = false
	if err != nil || ctx.Err() != nil {
		code := installErrorCode(err)
		auth.WriteError(w, 422, code, code)
		return
	}
	// Keep one pending preview per administrator and discard prior receipts.
	for id, job := range j.jobs {
		if job.owner == owner && job.State == "prepared" {
			delete(j.jobs, id)
		}
	}
	var id [16]byte
	_, _ = rand.Read(id[:])
	job := &nodeInstallJob{ID: hex.EncodeToString(id[:]), Plan: plan, State: "prepared", owner: owner, expires: time.Now().Add(5 * time.Minute)}
	j.jobs[job.ID] = job
	writeJSON(w, 200, job)
}

func (s *Server) handleApplyNodeInstall(w http.ResponseWriter, r *http.Request) {
	j, owner := s.installContext(w, r)
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.expire()
	job := j.jobs[r.PathValue("install_id")]
	if job == nil || job.owner != owner {
		auth.WriteError(w, 404, "node_install_not_found", "installation preview has expired")
		return
	}
	if job.State != "prepared" {
		writeJSON(w, 202, job)
		return
	}
	if j.closed || j.busy {
		auth.WriteError(w, 409, "node_install_busy", "an installation or preview is already running")
		return
	}
	j.busy = true
	job.State = "checking_host"
	job.expires = time.Time{}
	j.work.Go(func() {
		ctx, cancel := context.WithTimeout(j.ctx, 5*time.Minute)
		defer cancel()
		id, err := j.runner.Apply(ctx, job.Plan, s.nodes, func(state string) {
			j.mu.Lock()
			job.State = state
			j.mu.Unlock()
		})
		j.mu.Lock()
		defer j.mu.Unlock()
		job.NodeID = id
		job.State = "installed"
		if err != nil {
			job.State = "failed"
			job.Code = installErrorCode(err)
		}
		job.expires = time.Now().Add(time.Hour)
		j.busy = false
	})
	s.appendAudit(r, "node.install.start", job.ID, "")
	writeJSON(w, 202, job)
}

func (s *Server) handleNodeInstallStatus(w http.ResponseWriter, r *http.Request) {
	j, owner := s.installContext(w, r)
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.expire()
	job := j.jobs[r.PathValue("install_id")]
	if job == nil || job.owner != owner {
		auth.WriteError(w, 404, "node_install_not_found", "installation status is unavailable")
		return
	}
	writeJSON(w, 200, job)
}

func installErrorCode(err error) string {
	switch err {
	case nodeinstall.ErrInvalid, nodeinstall.ErrSSH, nodeinstall.ErrHostKey,
		nodeinstall.ErrUnsupported, nodeinstall.ErrExisting, nodeinstall.ErrPorts,
		nodeinstall.ErrArtifacts, nodeinstall.ErrFullArtifact, nodeinstall.ErrApply, nodeinstall.ErrHealth, nodeinstall.ErrTLSActivity, nodeinstall.ErrManagement,
		nodeinstall.ErrRegistry, nodeinstall.ErrUnconfirmed, nodeinstall.ErrUpgradeStart, nodeinstall.ErrUpgradeCheck:
		return err.Error()
	default:
		return "node_install_failed"
	}
}
