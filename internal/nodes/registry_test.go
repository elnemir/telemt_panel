package nodes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/amirotin/telemt_panel/internal/atomicfile"
	"github.com/amirotin/telemt_panel/internal/store"
)

func stateFor(t *testing.T, path string) *store.Memory {
	t.Helper()
	s, err := store.NewState(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func openFor(t *testing.T, s State) *Registry {
	t.Helper()
	r, err := Open(s, "http://127.0.0.1:9091", "default-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}

func inputFor(address string) Input {
	return Input{Name: "Node", APIURL: address, Enabled: true, Labels: map[string]string{"region": "eu"}}
}

func TestPersistenceIdentityAndConflicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := stateFor(t, path)
	r := openFor(t, s)
	in := inputFor("https://example.test/api/")
	in.AuthHeaderEnv = "NODE_TEST_AUTH"
	n, err := r.Create(in)
	if err != nil {
		t.Fatal(err)
	}
	in.Labels["region"] = "mutated"
	got, _ := r.Get(n.ID)
	if got.APIURL != "https://example.test/api" || got.Labels["region"] != "eu" {
		t.Fatal("input aliases registry state")
	}
	got.Labels["region"] = "mutated again"
	r2 := openFor(t, stateFor(t, path))
	persisted, err := r2.Get(n.ID)
	if err != nil || persisted.Revision != 1 || persisted.Labels["region"] != "eu" {
		t.Fatal("restart lost registry", err)
	}
	raw, _, _ := s.GetSetting(settingKey)
	if strings.Contains(raw, "default-test-secret") {
		t.Fatal("default credential persisted")
	}
	if _, err := r.Replace(DefaultID, 1, inputFor("http://127.0.0.1:9091")); !errors.Is(err, ErrProtected) {
		t.Fatal(err)
	}
	if err := r.Delete(DefaultID, 1); !errors.Is(err, ErrProtected) {
		t.Fatal(err)
	}
	if _, err := r.Replace(n.ID, 2, persisted.Input); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	changed := persisted.Input
	changed.APIURL = "https://another.test"
	if _, err := r.Replace(n.ID, 1, changed); !errors.Is(err, ErrInvalid) {
		t.Fatal("endpoint identity changed", err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if _, err := r.Replace(n.ID, 1, persisted.Input); err == nil {
				wins.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal("concurrent stale updates succeeded", wins.Load())
	}
	if err := r.Delete(n.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := r.Delete(n.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Resolve(n.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted node fell back", err)
	}
}

type failingState struct {
	State
	failure error
}

func (s *failingState) SetSetting(key, value string) error {
	if atomicfile.Published(s.failure) {
		if err := s.State.SetSetting(key, value); err != nil {
			return err
		}
	}
	if s.failure != nil {
		return s.failure
	}
	return s.State.SetSetting(key, value)
}

func TestPublicationFailureMatchesState(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(fmt.Sprint(published), func(t *testing.T) {
			s := &failingState{State: stateFor(t, "")}
			r := openFor(t, s)
			n, _ := r.Create(inputFor("http://example.test"))
			in := n.Input
			in.Enabled = false
			s.failure = errors.New("disk test failure")
			if published {
				s.failure = &atomicfile.PublicationError{Err: s.failure}
			}
			if _, err := r.Replace(n.ID, 1, in); !errors.Is(err, ErrStorage) {
				t.Fatal(err)
			}
			got, _ := r.Get(n.ID)
			reopened := openFor(t, s)
			onDisk, _ := reopened.Get(n.ID)
			if got.Revision != onDisk.Revision || got.Enabled != onDisk.Enabled || got.Enabled == published {
				t.Fatal("memory and published state diverged")
			}
		})
	}
}

func TestValidationAndCorruptState(t *testing.T) {
	for _, address := range []string{"file:///tmp/config", "http://u:p@example.test", "http://example.test/?token=x",
		"http://example.test/#", "http://example.test/../api", "http://example.test/%2fsecret", "http://example.test//api", "http://example.test:65536", "http://example.test:0"} {
		if _, err := normalize(inputFor(address)); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted %s", address)
		}
	}
	for _, raw := range []string{`{}`, `{"version":2,"nodes":[]}`, `{"version":1,"nodes":[{"id":"default"}]}`, "invalid"} {
		s := stateFor(t, "")
		_ = s.SetSetting(settingKey, raw)
		if _, err := Open(s, "", ""); !errors.Is(err, ErrState) {
			t.Fatalf("accepted corrupt registry: %v", err)
		}
	}
}

func TestHTTPIsolationCredentialsFailureAndLifecycle(t *testing.T) {
	var callsA, callsB atomic.Int32
	server := func(secret, status string, calls *atomic.Int32) *httptest.Server {
		h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			calls.Add(1)
			if req.Header.Get("Authorization") != secret {
				t.Error("cross-node credential")
				w.WriteHeader(401)
				return
			}
			if req.URL.Path == "/v1/users" {
				fmt.Fprintf(w, `{"ok":true,"revision":"%s","data":[{"username":"same","enabled":true,"current_connections":%d}]}`, status, calls.Load())
			} else {
				fmt.Fprintf(w, `{"ok":true,"data":{"status":"%s"}}`, status)
			}
		}))
		t.Cleanup(h.Close)
		return h
	}
	a := server("test-A", "A", &callsA)
	b := server("test-B", "B", &callsB)
	r := openFor(t, stateFor(t, ""))
	t.Setenv("NODE_TEST_A", "test-A")
	t.Setenv("NODE_TEST_B", "test-B")
	ai := inputFor(a.URL)
	ai.AuthHeaderEnv = "NODE_TEST_A"
	bi := inputFor(b.URL)
	bi.AuthHeaderEnv = "NODE_TEST_B"
	an, _ := r.Create(ai)
	bn, _ := r.Create(bi)
	_, ac, err := r.Resolve(an.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, bc, err := r.Resolve(bn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ac == bc {
		t.Fatal("shared node client")
	}
	for _, pair := range []struct{ id, status string }{{an.ID, "A"}, {bn.ID, "B"}} {
		_, c, _ := r.Resolve(pair.id)
		users, rev, err := c.UsersWithRevision(context.Background())
		if err != nil || len(users) != 1 || users[0].Username != "same" || rev != pair.status {
			t.Fatal("user isolation", err)
		}
	}
	a.Close()
	if _, err := ac.Health(context.Background()); err == nil {
		t.Fatal("failed node accepted")
	}
	health, err := bc.Health(context.Background())
	if err != nil || health.Status != "B" {
		t.Fatal("A affected B", err)
	}
	if _, _, err := r.Resolve("unknown"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	t.Setenv("NODE_TEST_B", "")
	if _, _, err := r.Resolve(bn.ID); !errors.Is(err, ErrCredentials) {
		t.Fatal(err)
	}
	bi.Enabled = false
	if _, err := r.Replace(bn.ID, 1, bi); err != nil {
		t.Fatal(err)
	}
	if r.Current(bn) {
		t.Fatal("disabled node still current")
	}
	if _, _, err := r.Resolve(bn.ID); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
}

func TestRedirectCannotEscapeNode(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer source.Close()
	reg := openFor(t, stateFor(t, ""))
	n, _ := reg.Create(inputFor(source.URL))
	_, client, _ := reg.Resolve(n.ID)
	if _, err := client.Health(context.Background()); err == nil {
		t.Fatal("redirect accepted")
	}
	if leaked.Load() != 0 {
		t.Fatal("request left selected node")
	}
}

func TestSlowNodeDoesNotHoldRegistry(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		fmt.Fprint(w, `{"ok":true,"data":{"status":"ok"}}`)
	}))
	defer server.Close()
	reg := openFor(t, stateFor(t, ""))
	n, _ := reg.Create(inputFor(server.URL))
	snapshot, client, _ := reg.Resolve(n.ID)
	done := make(chan struct{})
	go func() { defer close(done); _, _ = client.Health(context.Background()) }()
	<-started
	other, err := reg.Create(inputFor("http://other.test"))
	if err != nil || other.ID == n.ID {
		t.Fatal("registry blocked", err)
	}
	if err := reg.Delete(n.ID, 1); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	if reg.Current(snapshot) {
		t.Fatal("retired snapshot accepted")
	}
}

func TestRegistryBoundsRemainReopenable(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(fmt.Sprint(large), func(t *testing.T) {
			st := stateFor(t, "")
			r := openFor(t, st)
			in := inputFor("http://example.test")
			if large {
				in.Labels = map[string]string{}
				for i := range 32 {
					in.Labels[fmt.Sprintf("label-%02d", i)] = strings.Repeat("<", 256)
				}
			}
			accepted := 0
			for range MaxNodes + 1 {
				_, err := r.Create(in)
				if errors.Is(err, ErrLimit) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				accepted++
			}
			if (!large && accepted != MaxNodes) || (large && (accepted == 0 || accepted >= MaxNodes)) {
				t.Fatal("limit not enforced", accepted)
			}
			reopened := openFor(t, st)
			if len(reopened.List()) != accepted+1 {
				t.Fatal("persisted limit state cannot reopen")
			}
		})
	}
}

func TestCredentialRotationReplacesOnlyTargetClient(t *testing.T) {
	r := openFor(t, stateFor(t, ""))
	in := inputFor("http://example.test")
	in.AuthHeaderEnv = "NODE_ROTATE_TEST"
	t.Setenv(in.AuthHeaderEnv, "test-first")
	n, err := r.Create(in)
	if err != nil {
		t.Fatal(err)
	}
	_, first, err := r.Resolve(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, defaultClient, err := r.Resolve(DefaultID)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(in.AuthHeaderEnv, "test-second")
	_, second, err := r.Resolve(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, defaultAgain, err := r.Resolve(DefaultID)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || defaultClient != defaultAgain {
		t.Fatal("credential rotation affected wrong scope")
	}
	t.Setenv(in.AuthHeaderEnv, "bad\r\nheader")
	if _, _, err := r.Resolve(n.ID); !errors.Is(err, ErrCredentials) {
		t.Fatal("invalid credential accepted", err)
	}
}
