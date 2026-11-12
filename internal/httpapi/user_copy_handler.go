package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/nodes"
	"github.com/amirotin/telemt_panel/internal/platform"
	"github.com/amirotin/telemt_panel/internal/telemt"
	"github.com/amirotin/telemt_panel/internal/usercopy"
)

func (s *Server) withCopyNode(ctx context.Context, id string, run func(context.Context, usercopy.Client, uint64) error) error {
	if s.nodes == nil {
		return nodes.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if id == nodes.DefaultID {
		n, c, err := s.nodes.Resolve(id)
		if err != nil {
			return err
		}
		err = s.runCopyNode(ctx, id, c, n.Revision, run)
		s.pokeUsersAfterMutation()
		return err
	}
	rt, release, err := s.acquireNodeRuntime(id)
	if err != nil {
		return err
	}
	defer release()
	stop := context.AfterFunc(rt.ctx, cancel)
	defer stop()
	if rt.ctx.Err() != nil {
		return nodes.ErrDisabled
	}
	err = s.runCopyNode(ctx, id, rt.client, rt.node.Revision, run)
	rt.server.pokeUsersAfterMutation()
	return err
}
func (s *Server) requireUserCopies(w http.ResponseWriter) bool {
	if s.userCopies != nil && s.nodes != nil {
		return true
	}
	auth.WriteError(w, 503, "copy_unavailable", "copy requires durable central state and node registry")
	return false
}
func (s *Server) handlePrepareUserCopy(w http.ResponseWriter, r *http.Request) {
	if !s.requireUserCopies(w) {
		return
	}
	var selection usercopy.Selection
	if decodeJSONBody(w, r, &selection, jsonBodyOptions{MaxBytes: 8192, RejectUnknown: true}) != nil {
		auth.WriteError(w, 400, "bad_request", "invalid copy selection")
		return
	}
	preview, err := s.userCopies.Prepare(r.Context(), selection)
	if err != nil {
		status, code := 503, "copy_source_unavailable"
		if errors.Is(err, usercopy.ErrInvalid) {
			status, code = 400, "copy_invalid"
		}
		if errors.Is(err, usercopy.ErrBusy) {
			status, code = 409, "copy_preview_capacity"
		}
		auth.WriteError(w, status, code, code)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, preview)
}
func (s *Server) handleExecuteUserCopy(w http.ResponseWriter, r *http.Request) {
	if !s.requireUserCopies(w) {
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if decodeJSONBody(w, r, &body, jsonBodyOptions{MaxBytes: 1024, RejectUnknown: true}) != nil || len(body.Token) != 64 {
		auth.WriteError(w, 400, "bad_request", "copy preview token required")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	result, err := s.userCopies.Execute(r.Context(), key, body.Token)
	if err == nil {
		s.appendAudit(r, "user.copy", "", key)
	}
	writeAgentCommand(w, key, result, err)
}
func (s *Server) handleUserCopyStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireUserCopies(w) {
		return
	}
	result, ok := s.userCopies.Get(r.PathValue("key"))
	if !ok {
		auth.WriteError(w, 404, "copy_not_found", "copy command not found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}

func (s *Server) runCopyNode(ctx context.Context, id string, c usercopy.Client, revision uint64, run func(context.Context, usercopy.Client, uint64) error) error {
	if s.platform == nil {
		return run(ctx, c, revision)
	}
	s.platformMu.RLock()
	defer s.platformMu.RUnlock()
	return run(ctx, clusterProtectedCopyClient{Client: c, state: s.platform, node: id}, revision)
}

type clusterProtectedCopyClient struct {
	usercopy.Client
	state *platform.Store
	node  string
}

func (c clusterProtectedCopyClient) check(ctx context.Context, name string) error {
	owned, e := c.state.ManagedUser(ctx, c.node, name)
	if e != nil {
		return e
	}
	if owned {
		return platform.ErrConflict
	}
	return nil
}
func (c clusterProtectedCopyClient) CreateUserWithRevision(ctx context.Context, r telemt.CreateUserRequest, rev string) (telemt.UserInfo, error) {
	if e := c.check(ctx, r.Username); e != nil {
		return telemt.UserInfo{}, e
	}
	return c.Client.CreateUserWithRevision(ctx, r, rev)
}
func (c clusterProtectedCopyClient) PatchUserWithRevision(ctx context.Context, name string, p map[string]any, rev string) (telemt.UserInfo, error) {
	if e := c.check(ctx, name); e != nil {
		return telemt.UserInfo{}, e
	}
	return c.Client.PatchUserWithRevision(ctx, name, p, rev)
}
