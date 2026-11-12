// Package nodes owns independent Telemt identities and their connection scope.
package nodes

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amirotin/telemt_panel/internal/atomicfile"
	"github.com/amirotin/telemt_panel/internal/nodeagent"
	"github.com/amirotin/telemt_panel/internal/telemt"
)

const (
	DefaultID        = "default"
	settingKey       = "nodes.registry.v1"
	MaxNodes         = 256
	maxDocumentBytes = 2 * 1024 * 1024
)

var (
	ErrNotFound    = errors.New("node not found")
	ErrDisabled    = errors.New("node is disabled")
	ErrConflict    = errors.New("node revision changed")
	ErrProtected   = errors.New("configuration node is read-only")
	ErrCredentials = errors.New("node credentials are unavailable")
	ErrInvalid     = errors.New("invalid node configuration")
	ErrLimit       = errors.New("node registry capacity reached")
	ErrStorage     = errors.New("node registry persistence failed; reread before retry")
	ErrState       = errors.New("invalid persisted node registry")
	envName        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	nodeID         = regexp.MustCompile(`^n_[0-9a-f]{32}$`)
)

// State is the mandatory control-plane store, never the optional history DB.
type State interface {
	GetSetting(string) (string, bool, error)
	SetSetting(string, string) error
	StateDurable() bool
}

// Input replaces editable metadata. Credentials are references, never values.
type Input struct {
	nodeagent.Binding
	Name          string            `json:"name"`
	APIURL        string            `json:"api_url"`
	Enabled       bool              `json:"enabled"`
	AuthHeaderEnv string            `json:"auth_header_env,omitempty"`
	Labels        map[string]string `json:"labels"`
}

type Node struct {
	Input
	ID             string    `json:"id"`
	Source         string    `json:"source"`
	Revision       uint64    `json:"revision"`
	AuthConfigured bool      `json:"auth_configured"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type document struct {
	Version int    `json:"version"`
	Nodes   []Node `json:"nodes"`
}

type cachedClient struct {
	client *telemt.Client
	auth   string
}

type Registry struct {
	mu          sync.Mutex
	state       State
	nodes       map[string]Node
	clients     map[string]cachedClient
	defaultAuth string
	lookupEnv   func(string) (string, bool)
}

// Open validates stored identities before serving. The legacy configuration
// remains authoritative for default; its secret is never copied into state.
func Open(state State, defaultURL, defaultAuth string) (*Registry, error) {
	r := &Registry{state: state, nodes: map[string]Node{}, clients: map[string]cachedClient{},
		defaultAuth: defaultAuth, lookupEnv: os.LookupEnv}
	raw, exists, err := state.GetSetting(settingKey)
	if err != nil {
		return nil, ErrStorage
	}
	if exists {
		var doc document
		if len(raw) > maxDocumentBytes || json.Unmarshal([]byte(raw), &doc) != nil || doc.Version != 1 || len(doc.Nodes) > MaxNodes {
			return nil, ErrState
		}
		for _, n := range doc.Nodes {
			input, err := normalize(n.Input)
			if err != nil || !nodeID.MatchString(n.ID) || n.Revision == 0 || n.Source != "registry" ||
				n.CreatedAt.IsZero() || n.UpdatedAt.Before(n.CreatedAt) {
				return nil, ErrState
			}
			if _, duplicate := r.nodes[n.ID]; duplicate {
				return nil, ErrState
			}
			n.Input = input
			n.AuthConfigured = n.AuthHeaderEnv != ""
			r.nodes[n.ID] = n
		}
	}
	if defaultURL != "" {
		input, err := normalize(Input{Name: "Default", APIURL: defaultURL, Enabled: true})
		if err != nil {
			return nil, ErrInvalid
		}
		r.nodes[DefaultID] = Node{Input: input, ID: DefaultID, Source: "configuration",
			Revision: 1, AuthConfigured: defaultAuth != ""}
	}
	return r, nil
}

func normalize(in Input) (Input, error) {
	if nodeagent.ValidateBinding(in.Binding) != nil {
		return Input{}, ErrInvalid
	}
	in.Name = strings.TrimSpace(in.Name)
	u, err := url.Parse(in.APIURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") ||
		u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		u.Opaque != "" || u.RawPath != "" || strings.Contains(in.APIURL, "#") ||
		strings.Contains(u.Path, "//") || strings.Contains(u.Path, "\\") {
		return Input{}, ErrInvalid
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return Input{}, ErrInvalid
		}
	}
	if port := u.Port(); port != "" {
		value, err := strconv.ParseUint(port, 10, 16)
		if err != nil || value == 0 {
			return Input{}, ErrInvalid
		}
	}
	if len(in.APIURL) > 2048 || len(in.Name) == 0 || len(in.Name) > 128 ||
		(in.AuthHeaderEnv != "" && !envName.MatchString(in.AuthHeaderEnv)) || len(in.Labels) > 32 {
		return Input{}, ErrInvalid
	}
	for k, v := range in.Labels {
		if len(k) == 0 || len(k) > 64 || len(v) > 256 {
			return Input{}, ErrInvalid
		}
	}
	in.APIURL = strings.TrimRight(u.String(), "/")
	in.Labels = cloneLabels(in.Labels)
	return in, nil
}

func cloneLabels(labels map[string]string) map[string]string {
	result := make(map[string]string, len(labels))
	for k, v := range labels {
		result[k] = v
	}
	return result
}

func clone(n Node) Node           { n.Labels = cloneLabels(n.Labels); return n }
func (r *Registry) Durable() bool { return r.state.StateDurable() }

func (r *Registry) List() []Node {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]Node, 0, len(r.nodes))
	for _, n := range r.nodes {
		result = append(result, clone(n))
	}
	slices.SortFunc(result, func(a, b Node) int { return strings.Compare(a.ID, b.ID) })
	return result
}

func (r *Registry) Get(id string) (Node, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, exists := r.nodes[id]
	if !exists {
		return Node{}, ErrNotFound
	}
	return clone(n), nil
}

func (r *Registry) Create(in Input) (Node, error) {
	in, err := normalize(in)
	if err != nil {
		return Node{}, err
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return Node{}, ErrStorage
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	count := len(r.nodes)
	if _, exists := r.nodes[DefaultID]; exists {
		count--
	}
	if count >= MaxNodes {
		return Node{}, ErrLimit
	}
	now := time.Now().UTC()
	n := Node{Input: in, ID: "n_" + hex.EncodeToString(token[:]), Source: "registry",
		Revision: 1, AuthConfigured: in.AuthHeaderEnv != "", CreatedAt: now, UpdatedAt: now}
	if _, exists := r.nodes[n.ID]; exists {
		return Node{}, ErrConflict
	}
	if err := r.persist(n.ID, &n); err != nil {
		return Node{}, err
	}
	return clone(n), nil
}

func (r *Registry) Replace(id string, revision uint64, in Input) (Node, error) {
	in, err := normalize(in)
	if err != nil {
		return Node{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	n, err := r.mutable(id, revision)
	if err != nil {
		return Node{}, err
	}
	// Reusing an identity for another server would contaminate future history.
	if n.APIURL != in.APIURL || (n.Binding.ID != "" && (n.Binding.ID != in.Binding.ID || n.Binding.URL != in.Binding.URL)) {
		return Node{}, ErrInvalid
	}
	if n.Revision == ^uint64(0) {
		return Node{}, ErrConflict
	}
	n.Input = in
	n.Revision++
	n.UpdatedAt = time.Now().UTC()
	n.AuthConfigured = in.AuthHeaderEnv != ""
	if err := r.persist(id, &n); err != nil {
		return Node{}, err
	}
	return clone(n), nil
}

func (r *Registry) Delete(id string, revision uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.mutable(id, revision); err != nil {
		return err
	}
	return r.persist(id, nil)
}

func (r *Registry) mutable(id string, revision uint64) (Node, error) {
	n, exists := r.nodes[id]
	if !exists {
		return Node{}, ErrNotFound
	}
	if n.Source != "registry" {
		return Node{}, ErrProtected
	}
	if revision == 0 || revision != n.Revision {
		return Node{}, ErrConflict
	}
	return n, nil
}

// persist publishes before replacing memory, except for a published-but-not-
// durable error. In that case both stores must retain the published version.
func (r *Registry) persist(id string, next *Node) error {
	doc := document{Version: 1, Nodes: []Node{}}
	for key, n := range r.nodes {
		if key != id && n.Source == "registry" {
			doc.Nodes = append(doc.Nodes, n)
		}
	}
	if next != nil {
		doc.Nodes = append(doc.Nodes, *next)
	}
	slices.SortFunc(doc.Nodes, func(a, b Node) int { return strings.Compare(a.ID, b.ID) })
	raw, err := json.Marshal(doc)
	if err != nil {
		return ErrStorage
	}
	if len(raw) > maxDocumentBytes {
		return ErrLimit
	}
	err = r.state.SetSetting(settingKey, string(raw))
	if err == nil || atomicfile.Published(err) {
		if next == nil {
			delete(r.nodes, id)
		} else {
			r.nodes[id] = *next
		}
		if c, ok := r.clients[id]; ok {
			c.client.CloseIdleConnections()
			delete(r.clients, id)
		}
	}
	if err != nil {
		return ErrStorage
	}
	return nil
}

// Resolve only selects an exact enabled identity. Network I/O happens after
// this returns, so a slow node cannot hold the registry mutex.
func (r *Registry) Resolve(id string) (Node, *telemt.Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, exists := r.nodes[id]
	if !exists {
		return Node{}, nil, ErrNotFound
	}
	if !n.Enabled {
		return Node{}, nil, ErrDisabled
	}
	auth := ""
	if id == DefaultID {
		auth = r.defaultAuth
	} else if n.AuthHeaderEnv != "" {
		value, ok := r.lookupEnv(n.AuthHeaderEnv)
		if !ok || value == "" {
			return Node{}, nil, ErrCredentials
		}
		auth = value
	}
	if strings.ContainsAny(auth, "\r\n") {
		return Node{}, nil, ErrCredentials
	}
	c, exists := r.clients[id]
	if !exists || c.auth != auth {
		if exists {
			c.client.CloseIdleConnections()
		}
		c = cachedClient{client: telemt.NewIsolated(n.APIURL, auth), auth: auth}
		r.clients[id] = c
	}
	return clone(n), c.client, nil
}

func (r *Registry) Current(snapshot Node) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, exists := r.nodes[snapshot.ID]
	return exists && n.Enabled && n.Revision == snapshot.Revision
}

func (r *Registry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, c := range r.clients {
		c.client.CloseIdleConnections()
		delete(r.clients, id)
	}
}
