// Package usercopy owns explicitly confirmed, one-shot copies across node scopes.
package usercopy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/amirotin/telemt_panel/internal/nodeagent"
	"github.com/amirotin/telemt_panel/internal/subpage"
	"github.com/amirotin/telemt_panel/internal/telemt"
)

var ErrInvalid = errors.New("invalid copy selection")
var ErrPreview = errors.New("copy preview expired or unavailable")
var ErrSource = errors.New("source user or secret unavailable")
var ErrChanged = errors.New("source configuration changed")
var ErrBusy = errors.New("copy preview capacity reached")

const MaxTargets = 8

type Client interface {
	UsersWithRevision(context.Context) ([]telemt.UserInfo, string, error)
	CreateUserWithRevision(context.Context, telemt.CreateUserRequest, string) (telemt.UserInfo, error)
	PatchUserWithRevision(context.Context, string, map[string]any, string) (telemt.UserInfo, error)
}

// WithNode owns one runtime lease at a time, merging cancellation into ctx.
// Registry revision is immutable for the duration of the callback's lease.
type WithNode func(ctx context.Context, id string, run func(context.Context, Client, uint64) error) error
type Options struct {
	Limits     bool `json:"limits"`
	Expiration bool `json:"expiration"`
	Enabled    bool `json:"enabled"`
	AdTag      bool `json:"ad_tag"`
	Overwrite  bool `json:"overwrite"`
}
type Selection struct {
	SourceID string   `json:"source_id"`
	Username string   `json:"username"`
	Targets  []string `json:"targets"`
	Options  Options  `json:"options"`
}
type Target struct {
	NodeID       string `json:"node_id"`
	NodeRevision uint64 `json:"node_revision"`
	Revision     string `json:"revision"`
	Exists       bool   `json:"exists"`
	State        string `json:"state"`
	Code         string `json:"code,omitempty"`
	InRuntime    bool   `json:"in_runtime"`
}
type Preview struct {
	Token              string                   `json:"token"`
	ExpiresAt          time.Time                `json:"expires_at"`
	Selection          Selection                `json:"selection"`
	SourceRevision     string                   `json:"source_revision"`
	SourceNodeRevision uint64                   `json:"source_node_revision"`
	Fields             telemt.CreateUserRequest `json:"fields"` // Secret is deliberately empty.
	Targets            []Target                 `json:"targets"`
}
type Result struct {
	SourceID string   `json:"source_id"`
	Username string   `json:"username"`
	Targets  []Target `json:"targets"`
}
type prepared struct {
	preview Preview
	user    telemt.CreateUserRequest
}
type Manager struct {
	mu       sync.Mutex
	previews map[string]prepared
	withNode WithNode
	journal  *nodeagent.Journal
	now      func() time.Time
}

// Reuse the durable command protocol in a distinct central state namespace.
// Neither requests nor secrets are persisted in this journal, only outcomes.
type journalState struct{ nodeagent.State }

func (s journalState) GetSetting(string) (string, bool, error) {
	return s.State.GetSetting("users.copy.commands.v1")
}
func (s journalState) SetSetting(_ string, value string) error {
	return s.State.SetSetting("users.copy.commands.v1", value)
}
func New(state nodeagent.State, withNode WithNode) (*Manager, error) {
	j, err := nodeagent.OpenJournal(journalState{state})
	if err != nil {
		return nil, err
	}
	return &Manager{previews: map[string]prepared{}, withNode: withNode, journal: j, now: time.Now}, nil
}
func (m *Manager) Get(key string) (nodeagent.CommandResult, bool) { return m.journal.Get(key) }

func validSelection(s Selection) bool {
	if s.SourceID == "" || len(s.Username) == 0 || len(s.Username) > 128 || len(s.Targets) == 0 || len(s.Targets) > MaxTargets {
		return false
	}
	seen := map[string]bool{s.SourceID: true}
	for _, id := range s.Targets {
		if id == "" || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
func findUser(users []telemt.UserInfo, name string) (telemt.UserInfo, bool) {
	for _, u := range users {
		if u.Username == name {
			return u, true
		}
	}
	return telemt.UserInfo{}, false
}
func copyFields(u telemt.UserInfo, secret string, o Options) telemt.CreateUserRequest {
	r := telemt.CreateUserRequest{Username: u.Username, Secret: secret}
	if o.Limits {
		r.MaxTCPConns = u.MaxTCPConns
		r.DataQuotaBytes = u.DataQuotaBytes
		r.RateLimitUpBps = u.RateLimitUpBps
		r.RateLimitDownBps = u.RateLimitDownBps
		r.MaxUniqueIPs = u.MaxUniqueIPs
	}
	if o.Expiration {
		r.ExpirationRFC3339 = u.ExpirationRFC3339
	}
	if o.AdTag {
		r.UserAdTag = u.UserAdTag
	}
	if o.Enabled {
		r.Enabled = &u.Enabled
	}
	return r
}
func patchFor(u telemt.CreateUserRequest, o Options) map[string]any {
	p := map[string]any{"secret": u.Secret}
	if o.Limits {
		p["max_tcp_conns"] = u.MaxTCPConns
		p["data_quota_bytes"] = u.DataQuotaBytes
		p["rate_limit_up_bps"] = u.RateLimitUpBps
		p["rate_limit_down_bps"] = u.RateLimitDownBps
		p["max_unique_ips"] = u.MaxUniqueIPs
	}
	if o.Expiration {
		if u.ExpirationRFC3339 == "" {
			p["expiration_rfc3339"] = nil
		} else {
			p["expiration_rfc3339"] = u.ExpirationRFC3339
		}
	}
	if o.AdTag {
		if u.UserAdTag == "" {
			p["user_ad_tag"] = nil
		} else {
			p["user_ad_tag"] = u.UserAdTag
		}
	}
	if o.Enabled {
		p["enabled"] = u.Enabled
	}
	return p
}

func (m *Manager) Prepare(ctx context.Context, s Selection) (Preview, error) {
	if !validSelection(s) {
		return Preview{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s.Targets = append([]string(nil), s.Targets...)
	p := prepared{preview: Preview{Selection: s, Targets: make([]Target, 0, len(s.Targets))}}
	err := m.withNode(ctx, s.SourceID, func(ctx context.Context, c Client, nodeRevision uint64) error {
		users, revision, err := c.UsersWithRevision(ctx)
		if err != nil {
			return err
		}
		if revision == "" {
			return ErrSource
		}
		u, ok := findUser(users, s.Username)
		if !ok {
			return ErrSource
		}
		secret, ok := subpage.ExtractSecret(u.Links)
		if !ok {
			return ErrSource
		}
		p.user = copyFields(u, secret, s.Options)
		p.preview.SourceRevision = revision
		p.preview.SourceNodeRevision = nodeRevision
		return nil
	})
	if err != nil {
		return Preview{}, err
	}
	p.preview.Fields = p.user
	p.preview.Fields.Secret = ""
	for _, id := range s.Targets {
		target := Target{NodeID: id, State: "ready"}
		err := m.withNode(ctx, id, func(ctx context.Context, c Client, nr uint64) error {
			users, revision, err := c.UsersWithRevision(ctx)
			if err != nil {
				return err
			}
			if revision == "" {
				return ErrSource
			}
			_, target.Exists = findUser(users, s.Username)
			target.Revision = revision
			target.NodeRevision = nr
			if target.Exists && !s.Options.Overwrite {
				target.State = "skipped"
				target.Code = "user_exists"
			}
			return nil
		})
		if err != nil {
			target.State = "unavailable"
			target.Code = "node_unavailable"
		}
		p.preview.Targets = append(p.preview.Targets, target)
	}
	var token [32]byte
	_, _ = rand.Read(token[:])
	p.preview.Token = hex.EncodeToString(token[:])
	p.preview.ExpiresAt = m.now().Add(5 * time.Minute)
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range m.previews {
		if !m.now().Before(v.preview.ExpiresAt) {
			delete(m.previews, k)
		}
	}
	if len(m.previews) >= 32 {
		return Preview{}, ErrBusy
	}
	m.previews[p.preview.Token] = p
	return clonePreview(p.preview), nil
}
func clonePreview(p Preview) Preview {
	p.Targets = append([]Target(nil), p.Targets...)
	p.Selection.Targets = append([]string(nil), p.Selection.Targets...)
	// Fields contain pointers; serialization prevents external mutation of a prepared snapshot.
	raw, _ := json.Marshal(p.Fields)
	var fields telemt.CreateUserRequest
	_ = json.Unmarshal(raw, &fields)
	p.Fields = fields
	return p
}

func (m *Manager) Execute(ctx context.Context, key, token string) (nodeagent.CommandResult, error) {
	if len(token) != 64 {
		return nodeagent.CommandResult{}, ErrPreview
	}
	digest := sha256.Sum256([]byte(token))
	return m.journal.Execute(key, "user-copy/"+hex.EncodeToString(digest[:]), func() (int, []byte) {
		m.mu.Lock()
		p, ok := m.previews[token]
		delete(m.previews, token)
		m.mu.Unlock()
		if !ok || !m.now().Before(p.preview.ExpiresAt) {
			return 409, []byte(`{"code":"copy_preview_expired","message":"prepare the copy again"}`)
		}
		ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		// Recheck source identity and data before any target mutation.
		err := m.withNode(ctx, p.preview.Selection.SourceID, func(ctx context.Context, c Client, nr uint64) error {
			users, revision, err := c.UsersWithRevision(ctx)
			if err != nil {
				return err
			}
			u, ok := findUser(users, p.user.Username)
			secret, valid := subpage.ExtractSecret(u.Links)
			if nr != p.preview.SourceNodeRevision || revision != p.preview.SourceRevision || !ok || !valid || !reflect.DeepEqual(copyFields(u, secret, p.preview.Selection.Options), p.user) {
				return ErrChanged
			}
			return nil
		})
		if err != nil {
			return 409, []byte(`{"code":"copy_source_changed","message":"source changed or is unavailable; prepare again"}`)
		}
		result := Result{SourceID: p.preview.Selection.SourceID, Username: p.user.Username, Targets: append([]Target(nil), p.preview.Targets...)}
		for i := range result.Targets {
			target := &result.Targets[i]
			if target.State != "ready" {
				continue
			}
			attempted, accepted := false, false
			err := m.withNode(ctx, target.NodeID, func(ctx context.Context, c Client, nr uint64) error {
				if nr != target.NodeRevision {
					return ErrChanged
				}
				users, revision, err := c.UsersWithRevision(ctx)
				if err != nil {
					return err
				}
				_, exists := findUser(users, p.user.Username)
				if revision != target.Revision || exists != target.Exists {
					return ErrChanged
				}
				attempted = true
				if exists {
					_, err = c.PatchUserWithRevision(ctx, p.user.Username, patchFor(p.user, p.preview.Selection.Options), target.Revision)
				} else {
					_, err = c.CreateUserWithRevision(ctx, p.user, target.Revision)
				}
				if err != nil {
					return err
				}
				accepted = true
				users, _, err = c.UsersWithRevision(ctx)
				if err != nil {
					return err
				}
				u, ok := findUser(users, p.user.Username)
				secret, valid := subpage.ExtractSecret(u.Links)
				if !ok || !valid || !reflect.DeepEqual(copyFields(u, secret, p.preview.Selection.Options), p.user) {
					target.State = "unconfirmed"
					target.Code = "copy_readback_mismatch"
					return nil
				}
				target.State = "persisted"
				target.InRuntime = u.InRuntime
				return nil
			})
			if err != nil {
				target.State = "rejected"
				target.Code = "node_unavailable"
				if errors.Is(err, ErrChanged) {
					target.Code = "revision_conflict"
				} else {
					var api *telemt.APIError
					if !accepted && errors.As(err, &api) && api.Status >= 400 && api.Status < 500 {
						target.Code = "upstream_rejected"
					} else if attempted {
						target.State = "unknown"
						target.Code = "copy_result_unknown"
					}
				}
			}
		}
		raw, _ := json.Marshal(result)
		return 200, raw
	})
}
