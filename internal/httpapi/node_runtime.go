package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/geography"
	"github.com/amirotin/telemt_panel/internal/hub"
	"github.com/amirotin/telemt_panel/internal/nodeagent"
	"github.com/amirotin/telemt_panel/internal/nodes"
	"github.com/amirotin/telemt_panel/internal/quotareset"
	"github.com/amirotin/telemt_panel/internal/store"
	"github.com/amirotin/telemt_panel/internal/subpage"
	"github.com/amirotin/telemt_panel/internal/telemt"
)

type nodeStreamScopeKey struct{}

type nodeRuntime struct {
	node    nodes.Node
	client  *telemt.Client
	server  *Server
	handler http.Handler
	ctx     context.Context
	cancel  context.CancelFunc
	leases  sync.WaitGroup
	jobs    sync.WaitGroup
	scope   string
}

type nodeRuntimeEntry struct {
	mu      sync.Mutex // only this node; never held by an upstream request
	runtime *nodeRuntime
	store   *store.Composite
}

func (s *Server) nodeEntry(id string) *nodeRuntimeEntry {
	s.nodeRuntimeMu.Lock()
	defer s.nodeRuntimeMu.Unlock()
	if s.nodeRuntimesClosed {
		return nil
	}
	if s.nodeEntries == nil {
		s.nodeEntries = make(map[string]*nodeRuntimeEntry)
	}
	e := s.nodeEntries[id]
	if e == nil {
		e = &nodeRuntimeEntry{}
		s.nodeEntries[id] = e
	}
	return e
}

// stopRuntime is called with the entry lock, but without the registry-wide lock.
// Cancellation interrupts streams/requests before waiting for their leases.
func (e *nodeRuntimeEntry) stopRuntime() {
	rt := e.runtime
	if rt == nil {
		return
	}
	rt.cancel()
	rt.server.quotaResets.Close()
	rt.server.hub.Close()
	rt.jobs.Wait()
	rt.leases.Wait()
	rt.server.geography.Close()
	if rt.server.agentClient != nil {
		rt.server.agentClient.Close()
	}
	e.runtime = nil
}

func (s *Server) openNodeStore(id string) (*store.Composite, error) {
	statePath := ""
	if s.cfg.DataDir != "" {
		dir := filepath.Join(s.cfg.DataDir, "nodes", id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		statePath = filepath.Join(dir, "panel-state.json")
	}
	state, err := store.NewState(statePath)
	if err != nil {
		return nil, err
	}
	opts := store.OpenOptions{Driver: s.cfg.Store.Driver}
	if opts.Driver == "sqlite" {
		dir := filepath.Join(s.cfg.Store.Path+".nodes", id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			_ = state.Close()
			return nil, err
		}
		opts.Path = filepath.Join(dir, "history.sqlite")
	}
	history, err := store.Open(opts)
	if err != nil {
		_ = state.Close()
		return nil, err
	}
	result, err := store.NewComposite(state, history)
	if err != nil {
		_ = state.Close()
		_ = history.Close()
		return nil, err
	}
	return result, nil
}

func newNodeStreamScope(id string) string {
	var token [16]byte
	_, _ = rand.Read(token[:]) // crypto/rand.Read is infallible on supported Go runtimes.
	return id + ":" + hex.EncodeToString(token[:]) + ":"
}

func (s *Server) acquireNodeRuntime(id string) (*nodeRuntime, func(), error) {
	// Validate before allocating an entry: random unknown IDs cannot grow the map.
	if _, _, err := s.nodes.Resolve(id); err != nil {
		return nil, nil, err
	}
	e := s.nodeEntry(id)
	if e == nil {
		return nil, nil, nodes.ErrDisabled
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	s.nodeRuntimeMu.Lock()
	closed := s.nodeRuntimesClosed
	s.nodeRuntimeMu.Unlock()
	if closed {
		return nil, nil, nodes.ErrDisabled
	}
	n, client, err := s.nodes.Resolve(id)
	if err != nil {
		return nil, nil, err
	}
	if e.runtime != nil && (e.runtime.node.Revision != n.Revision || e.runtime.client != client) {
		e.stopRuntime()
	}
	if e.runtime == nil {
		if e.store == nil {
			e.store, err = s.openNodeStore(id)
			if err != nil {
				return nil, nil, nodes.ErrStorage
			}
		}
		cfg := *s.cfg
		cfg.Telemt.URL = n.APIURL
		cfg.Telemt.AuthHeader = "" // the credential belongs only to the resolved client
		cfg.Subpage.Enabled = false
		child := &Server{platform: s.platform, platformMu: s.platformMu, platformNodeID: id, cfg: &cfg, tc: client, st: e.store, hub: hub.New(hub.Config{}, client, e.store),
			sessions: s.sessions, logStreams: s.logStreams, geoip: s.geoip, version: s.version,
			subSvc:   subpage.NewService(cfg.Subpage.Secret, cfg.Subpage.BasePath, e.store),
			subIndex: subpage.NewIndex(cfg.Subpage.Secret, client, e.store)}
		if n.Binding.URL != "" {
			child.agentClient, _ = nodeagent.NewClient(n.Binding)
		}
		child.quotaResets = quotareset.New(client, child.quotaResetEvent)
		child.quotaSchedules = quotareset.NewScheduler(e.store, child.quotaResets)
		timeout := time.Duration(cfg.Geography.BuildTimeoutSecs) * time.Second
		child.geography = geography.NewService(geography.Dependencies{History: e.store, State: e.store, GeoIP: s.geoip, Live: child.hub, BuildTimeout: timeout, RequestTimeout: timeout + 2*time.Second})
		ctx, cancel := context.WithCancel(context.Background())
		rt := &nodeRuntime{node: n, client: client, server: child, handler: child.nodeManagementHandler(), ctx: ctx, cancel: cancel, scope: newNodeStreamScope(id)}
		e.runtime = rt
		child.hub.StartPersistentCollectors()
		rt.jobs.Go(func() { child.quotaSchedules.Run(ctx) })
	}
	rt := e.runtime
	rt.leases.Add(1)
	return rt, rt.leases.Done, nil
}

// refreshNode retires changed/disabled/deleted runtimes immediately after a
// registry mutation. The reconcile loop also detects credential changes.
func (s *Server) refreshNode(id string) {
	s.nodeRuntimeMu.Lock()
	e := s.nodeEntries[id]
	s.nodeRuntimeMu.Unlock()
	if e == nil {
		return
	}
	e.mu.Lock()
	n, client, err := s.nodes.Resolve(id)
	if err != nil || (e.runtime != nil && (n.Revision != e.runtime.node.Revision || client != e.runtime.client)) {
		e.stopRuntime()
	}
	if _, getErr := s.nodes.Get(id); getErr != nil {
		if e.store != nil {
			_ = e.store.Close()
			e.store = nil
		}
		s.nodeRuntimeMu.Lock()
		if s.nodeEntries[id] == e {
			delete(s.nodeEntries, id)
		}
		s.nodeRuntimeMu.Unlock()
	}
	e.mu.Unlock()
}

func (s *Server) runNodeRuntimes(ctx context.Context) {
	if s.nodes == nil || s.cfg.Agent.Enabled {
		return
	}
	reconcile := func() {
		s.nodeRuntimeMu.Lock()
		ids := make([]string, 0, len(s.nodeEntries))
		for id := range s.nodeEntries {
			ids = append(ids, id)
		}
		s.nodeRuntimeMu.Unlock()
		for _, id := range ids {
			s.refreshNode(id)
		}
		for _, n := range s.nodes.List() {
			if ctx.Err() != nil {
				return
			}
			if n.ID == nodes.DefaultID {
				continue
			}
			s.refreshNode(n.ID)
			if n.Enabled {
				if _, release, err := s.acquireNodeRuntime(n.ID); err == nil {
					release()
				}
			}
		}
	}
	reconcile()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}

// CloseNodeRuntimes runs after the reconciliation loop has stopped. It is also
// a test seam for Handler-only servers, which do not call Run.
func (s *Server) CloseNodeRuntimes() {
	s.nodeRuntimeMu.Lock()
	entries := s.nodeEntries
	s.nodeRuntimesClosed = true
	s.nodeEntries = nil
	s.nodeRuntimeMu.Unlock()
	for _, e := range entries {
		e.mu.Lock()
		e.stopRuntime()
		if e.store != nil {
			_ = e.store.Close()
		}
		e.mu.Unlock()
	}
}

func (s *Server) handleNodeManagement(w http.ResponseWriter, r *http.Request) {
	if !s.requireNodes(w) {
		return
	}
	id := r.PathValue("node_id")
	if _, _, err := s.nodes.Resolve(id); err != nil {
		writeNodeError(w, err)
		return
	}
	var target http.Handler
	ctx := r.Context()
	scope := ""
	if id == nodes.DefaultID {
		s.nodeDefaultOnce.Do(func() {
			s.nodeDefaultScope = newNodeStreamScope(nodes.DefaultID)
			s.nodeDefaultHandler = s.nodeManagementHandler()
		})
		target = s.nodeDefaultHandler
		scope = s.nodeDefaultScope
	} else {
		rt, release, err := s.acquireNodeRuntime(id)
		if err != nil {
			writeNodeError(w, err)
			return
		}
		defer release()
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		stop := context.AfterFunc(rt.ctx, cancel)
		defer func() { stop(); cancel() }()
		target = rt.handler
		scope = rt.scope
	}
	ctx = context.WithValue(ctx, nodeStreamScopeKey{}, scope)
	forwarded := r.Clone(ctx)
	u := *r.URL
	u.Path = "/api/" + r.PathValue("path")
	u.RawPath = ""
	forwarded.URL = &u
	w.Header().Set("X-Telemt-Node", id)
	w.Header().Set("Cache-Control", "no-store")
	target.ServeHTTP(w, forwarded)
}

func (s *Server) handleNodeHost(w http.ResponseWriter, r *http.Request) {
	if s.agentClient != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		res, err := s.agentClient.Request(ctx, "GET", "/v1/agent/host", "")
		if err != nil {
			auth.WriteError(w, 503, "node_agent_unavailable", "node agent unavailable")
			return
		}
		raw, err := nodeagent.ReadBody(res)
		var info hostInfo
		if err != nil || res.StatusCode != 200 || json.Unmarshal(raw, &info) != nil {
			auth.WriteError(w, 502, "node_agent_unavailable", "invalid agent host response")
			return
		}
		info.ActiveStore = s.st.Driver()
		info.HistoryTemporary = s.cfg.Store.Driver == "sqlite" && s.st.Driver() == "memory"
		info.Caps.RestartPanel = false
		info.Caps.SelfUpdate = false
		writeJSON(w, 200, info)
		return
	}
	writeJSON(w, http.StatusOK, hostInfo{ServiceManager: "none", LogSource: "none", PrivilegesMode: "manual", OS: "remote", Arch: "unknown", PanelVariant: store.Variant, StorageDrivers: store.AvailableDrivers(), ActiveStore: s.st.Driver(), HistoryTemporary: s.cfg.Store.Driver == "sqlite" && s.st.Driver() == "memory"})
}

func nodeOperationUnavailable(w http.ResponseWriter, r *http.Request) {
	auth.WriteError(w, http.StatusNotImplemented, "node_operation_unavailable", "This operation requires a node agent or a node-aware subscription service")
}
