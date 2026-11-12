package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/nodeinstall"
	"github.com/amirotin/telemt_panel/internal/nodes"
)

type nodeUpgrader interface {
	UpgradeTelemtVersion() string
	Upgrade(context.Context, *nodes.Registry, string, uint64, nodeinstall.UpgradeRequest, string, func(string)) (string, error)
}

type nodeUpgradeJob struct {
	RequestID string `json:"request_id"`
	State     string `json:"state"`
	Code      string `json:"code,omitempty"`
	input     nodeinstall.UpgradeRequest
	owner     string
	expires   time.Time
}

type nodeUpgradeStatus struct {
	Available     bool            `json:"available"`
	TelemtVersion string          `json:"telemt_version,omitempty"`
	AgentVersion  string          `json:"agent_version,omitempty"`
	Job           *nodeUpgradeJob `json:"job,omitempty"`
}

func (j *nodeInstallJobs) beginNodeWrite(id string) (func(), bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.upgradingNode == id {
		return nil, false
	}
	j.nodeWrites[id]++
	return func() {
		j.mu.Lock()
		defer j.mu.Unlock()
		j.nodeWrites[id]--
		if j.nodeWrites[id] == 0 {
			delete(j.nodeWrites, id)
		}
	}, true
}

// nodeWrite keeps registry and management changes from racing the SSH snapshot.
func (s *Server) nodeWrite(w http.ResponseWriter, id string) (func(), bool) {
	if s.nodeInstallation == nil || id == nodes.DefaultID {
		return func() {}, true
	}
	release, ok := s.nodeInstallation.beginNodeWrite(id)
	if !ok {
		auth.WriteError(w, 409, "node_upgrade_busy", "node upgrade in progress")
	}
	return release, ok
}

func (s *Server) upgradeRunner(n nodes.Node) (nodeUpgrader, nodeUpgradeStatus) {
	if s.nodeInstallation == nil || n.ID == nodes.DefaultID || !n.Enabled ||
		!n.ManagedCredentials || n.Binding.ID == "" || n.Binding.URL != n.APIURL {
		return nil, nodeUpgradeStatus{}
	}
	runner, ok := s.nodeInstallation.runner.(nodeUpgrader)
	if !ok {
		return nil, nodeUpgradeStatus{}
	}
	return runner, nodeUpgradeStatus{Available: true, TelemtVersion: runner.UpgradeTelemtVersion(), AgentVersion: s.version}
}

func (s *Server) handleGetNodeUpgrade(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.requireNodes(w) {
		return
	}
	n, err := s.nodes.Get(r.PathValue("node_id"))
	if err != nil {
		writeNodeError(w, err)
		return
	}
	_, status := s.upgradeRunner(n)
	if j := s.nodeInstallation; j != nil {
		j.mu.Lock()
		defer j.mu.Unlock()
		j.expire()
		status.Job = j.upgrades[n.ID]
	}
	writeJSON(w, 200, status)
}

func (s *Server) handleStartNodeUpgrade(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.requireNodes(w) {
		return
	}
	n, err := s.nodes.Get(r.PathValue("node_id"))
	if err != nil {
		writeNodeError(w, err)
		return
	}
	owner, _ := auth.UsernameFromContext(r.Context())
	runner, status := s.upgradeRunner(n)
	if !status.Available || owner == "" {
		auth.WriteError(w, 503, "node_upgrade_disabled", "SSH node upgrade is unavailable")
		return
	}
	var input nodeinstall.UpgradeRequest
	if decodeJSONBody(w, r, &input, jsonBodyOptions{MaxBytes: 4 << 10, RejectUnknown: true}) != nil || !input.Valid() {
		auth.WriteError(w, 400, "node_upgrade_invalid", "invalid node upgrade request")
		return
	}
	if input.Telemt && status.TelemtVersion == "" {
		auth.WriteError(w, 422, "node_upgrade_no_artifact", "no approved Telemt artifact is configured")
		return
	}

	j := s.nodeInstallation
	j.mu.Lock()
	defer j.mu.Unlock()
	j.expire()
	if previous := j.upgrades[n.ID]; previous != nil && previous.RequestID == input.RequestID {
		if previous.input != input || previous.owner != owner {
			auth.WriteError(w, 409, "node_upgrade_busy", "the request identifier is already in use")
			return
		}
		status.Job = previous
		writeJSON(w, 202, status)
		return
	}
	if j.closed || j.busy || j.nodeWrites[n.ID] != 0 ||
		(j.upgrades[n.ID] == nil && len(j.upgrades) >= nodes.MaxNodes) {
		auth.WriteError(w, 409, "node_upgrade_busy", "another SSH operation or node write is in progress")
		return
	}
	job := &nodeUpgradeJob{RequestID: input.RequestID, State: "checking_host", input: input, owner: owner}
	j.upgrades[n.ID] = job
	j.busy = true
	j.upgradingNode = n.ID
	j.work.Go(func() {
		ctx, cancel := context.WithTimeout(j.ctx, 5*time.Minute)
		defer cancel()
		state, err := runner.Upgrade(ctx, s.nodes, n.ID, n.Revision, input, s.version, func(phase string) {
			j.mu.Lock()
			job.State = phase
			j.mu.Unlock()
		})
		j.mu.Lock()
		defer j.mu.Unlock()
		job.State = state
		if err != nil {
			job.Code = installErrorCode(err)
		}
		job.expires = time.Now().Add(time.Hour)
		j.upgradingNode = ""
		j.busy = false
	})
	s.appendAudit(r, "node.upgrade.start", n.ID, "")
	status.Job = job
	writeJSON(w, 202, status)
}
