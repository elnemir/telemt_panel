package usercopy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amirotin/telemt_panel/internal/nodeagent"
	"github.com/amirotin/telemt_panel/internal/store"
	"github.com/amirotin/telemt_panel/internal/telemt"
)

const secret = "0123456789abcdef0123456789abcdef"

type fakeClient struct {
	mu             sync.Mutex
	user           *telemt.UserInfo
	rev            int
	writes         int
	failAfterWrite bool
	lastPatch      map[string]any
}

func links(s string) telemt.UserLinks {
	return telemt.UserLinks{Classic: []string{"tg://proxy?server=test&port=443&secret=" + s}}
}
func (c *fakeClient) UsersWithRevision(context.Context) ([]telemt.UserInfo, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	u := []telemt.UserInfo{}
	if c.user != nil {
		u = append(u, *c.user)
	}
	return u, fmt.Sprint(c.rev), nil
}
func (c *fakeClient) CreateUserWithRevision(_ context.Context, r telemt.CreateUserRequest, rev string) (telemt.UserInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fmt.Sprint(c.rev) != rev {
		return telemt.UserInfo{}, &telemt.APIError{Status: 409, Code: "revision_conflict"}
	}
	if c.user != nil {
		return telemt.UserInfo{}, &telemt.APIError{Status: 409, Code: "user_exists"}
	}
	c.writes++
	c.rev++
	u := telemt.UserInfo{Username: r.Username, Enabled: true, InRuntime: true, Links: links(r.Secret), MaxTCPConns: r.MaxTCPConns, DataQuotaBytes: r.DataQuotaBytes, RateLimitUpBps: r.RateLimitUpBps, RateLimitDownBps: r.RateLimitDownBps, MaxUniqueIPs: r.MaxUniqueIPs, ExpirationRFC3339: r.ExpirationRFC3339, UserAdTag: r.UserAdTag}
	if r.Enabled != nil {
		u.Enabled = *r.Enabled
	}
	c.user = &u
	if c.failAfterWrite {
		return telemt.UserInfo{}, errors.New("response lost")
	}
	return u, nil
}
func (c *fakeClient) PatchUserWithRevision(_ context.Context, _ string, p map[string]any, rev string) (telemt.UserInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fmt.Sprint(c.rev) != rev {
		return telemt.UserInfo{}, &telemt.APIError{Status: 409, Code: "revision_conflict"}
	}
	c.writes++
	c.rev++
	c.lastPatch = p
	u := *c.user
	u.Links = links(p["secret"].(string))
	if v, ok := p["enabled"]; ok {
		u.Enabled = *v.(*bool)
	}
	if v, ok := p["max_tcp_conns"]; ok {
		u.MaxTCPConns = v.(*uint64)
	}
	if v, ok := p["data_quota_bytes"]; ok {
		u.DataQuotaBytes = v.(*uint64)
	}
	if v, ok := p["rate_limit_up_bps"]; ok {
		u.RateLimitUpBps = v.(*uint64)
	}
	if v, ok := p["rate_limit_down_bps"]; ok {
		u.RateLimitDownBps = v.(*uint64)
	}
	if v, ok := p["max_unique_ips"]; ok {
		u.MaxUniqueIPs = v.(*uint64)
	}
	if v, ok := p["expiration_rfc3339"]; ok {
		u.ExpirationRFC3339 = ""
		if v != nil {
			u.ExpirationRFC3339 = v.(string)
		}
	}
	if v, ok := p["user_ad_tag"]; ok {
		u.UserAdTag = ""
		if v != nil {
			u.UserAdTag = v.(string)
		}
	}
	c.user = &u
	return u, nil
}
func fixture(t *testing.T) (*Manager, nodeagent.State, map[string]*fakeClient, Selection) {
	t.Helper()
	limit := uint64(1024)
	clients := map[string]*fakeClient{"A": {rev: 1, user: &telemt.UserInfo{Username: "alice", Enabled: false, InRuntime: true, Links: links(secret), DataQuotaBytes: &limit}}, "B": {rev: 1}, "C": {rev: 1}}
	st, err := store.NewState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	m, err := New(st, func(ctx context.Context, id string, run func(context.Context, Client, uint64) error) error {
		c := clients[id]
		if c == nil {
			return errors.New("node unavailable")
		}
		return run(ctx, c, 1)
	})
	if err != nil {
		t.Fatal(err)
	}
	return m, st, clients, Selection{SourceID: "A", Username: "alice", Targets: []string{"B", "C"}, Options: Options{Limits: true, Enabled: true, Expiration: true, AdTag: true}}
}
func execute(t *testing.T, m *Manager, key, token string) Result {
	t.Helper()
	r, e := m.Execute(context.Background(), key, token)
	if e != nil || r.Status != 200 {
		t.Fatal(r, e)
	}
	var result Result
	if json.Unmarshal(r.Body, &result) != nil {
		t.Fatal("bad result")
	}
	return result
}
func TestCopySameSecretAndLimitsDurableReplayAndNoSecretPersistence(t *testing.T) {
	m, st, clients, s := fixture(t)
	p, e := m.Prepare(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), secret) {
		t.Fatal("preview leaks secret")
	}
	// Callers cannot mutate the prepared selection or pointer-valued limits.
	*p.Fields.DataQuotaBytes = 999
	p.Targets[0].Revision = "tampered"
	key := nodeagent.NewCommandKey()
	result := execute(t, m, key, p.Token)
	for _, target := range result.Targets {
		if target.State != "persisted" || !target.InRuntime {
			t.Fatal(target)
		}
	}
	for _, id := range []string{"B", "C"} {
		c := clients[id]
		if c.writes != 1 || c.user.Enabled || *c.user.DataQuotaBytes != 1024 || c.user.Links.Classic[0] != links(secret).Classic[0] {
			t.Fatal(id, c.user)
		}
	}
	stored, _, _ := st.GetSetting("users.copy.commands.v1")
	if strings.Contains(stored, secret) {
		t.Fatal("journal contains secret")
	}
	restarted, e := New(st, m.withNode)
	if e != nil {
		t.Fatal(e)
	}
	execute(t, restarted, key, p.Token)
	if clients["B"].writes != 1 || clients["C"].writes != 1 {
		t.Fatal("replayed mutations")
	}
	if _, e = restarted.Execute(context.Background(), key, strings.Repeat("a", 64)); !errors.Is(e, nodeagent.ErrConflict) {
		t.Fatal(e)
	}
}
func TestCopyConflictsPartialFailuresAndLostResponse(t *testing.T) {
	m, _, clients, s := fixture(t)
	clients["B"].user = &telemt.UserInfo{Username: "alice", Links: links(strings.Repeat("f", 32)), TotalOctets: 888}
	clients["C"].failAfterWrite = true
	s.Targets = append(s.Targets, "missing")
	p, e := m.Prepare(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	key := nodeagent.NewCommandKey()
	r := execute(t, m, key, p.Token)
	if r.Targets[0].State != "skipped" || r.Targets[1].State != "unknown" || r.Targets[2].State != "unavailable" {
		t.Fatal(r)
	}
	execute(t, m, key, p.Token)
	if clients["B"].writes != 0 || clients["C"].writes != 1 {
		t.Fatal("conflict overwritten or unknown result replayed")
	}
}
func TestOverwriteTransfersNullLimitsButPreservesUnselectedExpiryAndUsage(t *testing.T) {
	m, _, clients, s := fixture(t)
	s.Targets = []string{"B"}
	s.Options.Overwrite = true
	s.Options.Expiration = false
	s.Options.AdTag = false
	clients["A"].user.DataQuotaBytes = nil
	limit := uint64(5)
	clients["B"].user = &telemt.UserInfo{Username: "alice", DataQuotaBytes: &limit, ExpirationRFC3339: "2030-01-01T00:00:00Z", TotalOctets: 1234, Links: links(strings.Repeat("f", 32))}
	p, e := m.Prepare(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	r := execute(t, m, nodeagent.NewCommandKey(), p.Token)
	c := clients["B"]
	if r.Targets[0].State != "persisted" || c.user.DataQuotaBytes != nil || c.user.TotalOctets != 1234 || c.user.ExpirationRFC3339 != "2030-01-01T00:00:00Z" {
		t.Fatal(r, c.user)
	}
	raw, _ := json.Marshal(c.lastPatch)
	if !strings.Contains(string(raw), `"data_quota_bytes":null`) || strings.Contains(string(raw), "expiration") {
		t.Fatal(string(raw))
	}
}
func TestSourceAndTargetRevisionsPreventStaleWrites(t *testing.T) {
	for _, changed := range []string{"A", "B"} {
		t.Run(changed, func(t *testing.T) {
			m, _, clients, s := fixture(t)
			p, e := m.Prepare(context.Background(), s)
			if e != nil {
				t.Fatal(e)
			}
			clients[changed].rev++
			r, e := m.Execute(context.Background(), nodeagent.NewCommandKey(), p.Token)
			if e != nil {
				t.Fatal(e)
			}
			if changed == "A" {
				if r.Status != 409 || clients["B"].writes != 0 || clients["C"].writes != 0 {
					t.Fatal(r)
				}
			} else {
				var result Result
				_ = json.Unmarshal(r.Body, &result)
				if result.Targets[0].Code != "revision_conflict" || clients["B"].writes != 0 || clients["C"].writes != 1 {
					t.Fatal(result)
				}
			}
		})
	}
}
func TestExpiredPreviewAndMissingSecretNeverMutate(t *testing.T) {
	m, _, clients, s := fixture(t)
	p, e := m.Prepare(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	m.now = func() time.Time { return p.ExpiresAt.Add(time.Second) }
	r, e := m.Execute(context.Background(), nodeagent.NewCommandKey(), p.Token)
	if e != nil || r.Status != 409 || clients["B"].writes != 0 {
		t.Fatal(r, e)
	}
	clients["A"].user.Links = telemt.UserLinks{}
	if _, e = m.Prepare(context.Background(), s); !errors.Is(e, ErrSource) {
		t.Fatal(e)
	}
}
func TestUnfinishedDurableIntentNeverResumes(t *testing.T) {
	m, st, clients, s := fixture(t)
	p, e := m.Prepare(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	key := nodeagent.NewCommandKey()
	digest := sha256.Sum256([]byte(p.Token))
	raw, _ := json.Marshal(map[string]nodeagent.CommandResult{key: {Key: key, Action: "user-copy/" + hex.EncodeToString(digest[:]), Started: time.Now().Unix()}})
	if st.SetSetting("users.copy.commands.v1", string(raw)) != nil {
		t.Fatal("save intent")
	}
	restarted, e := New(st, m.withNode)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = restarted.Execute(context.Background(), key, p.Token); !errors.Is(e, nodeagent.ErrUnknown) {
		t.Fatal(e)
	}
	if clients["B"].writes != 0 {
		t.Fatal("unfinished write resumed")
	}
}

func TestConcurrentReplayDispatchesEachTargetOnce(t *testing.T) {
	m, _, clients, s := fixture(t)
	p, e := m.Prepare(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	key := nodeagent.NewCommandKey()
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Go(func() {
			r, e := m.Execute(context.Background(), key, p.Token)
			if e == nil && r.Status != 200 {
				e = fmt.Errorf("status %d", r.Status)
			}
			errs <- e
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if clients["B"].writes != 1 || clients["C"].writes != 1 {
		t.Fatal("concurrent replay duplicated mutation")
	}
}
