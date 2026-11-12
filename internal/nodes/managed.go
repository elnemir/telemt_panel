package nodes

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"strings"

	"github.com/amirotin/telemt_panel/internal/nodeagent"
)

// ManagedCredentials is backend-only. Registry snapshots and browser inputs
// never carry these fields.
type ManagedCredentials struct {
	Control     string
	Agent       string
	Certificate []byte
}

func credentialCipher(key []byte) (cipher.AEAD, error) {
	if len(key) == 0 {
		return nil, nil
	}
	if len(key) != 32 {
		return nil, ErrCredentials
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrCredentials
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func (c ManagedCredentials) TLS() (*tls.Config, error) {
	certificate, err := x509.ParseCertificate(c.Certificate)
	if err != nil || len(c.Certificate) > 4096 {
		return nil, ErrCredentials
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, nil
}

func (r *Registry) ManagedAvailable() bool { return r.seal != nil && r.Durable() }

func (r *Registry) CreateManaged(in Input, credentials ManagedCredentials) (Node, error) {
	if !r.ManagedAvailable() {
		return Node{}, ErrCredentials
	}
	return r.create(in, &credentials)
}

func (r *Registry) sealCredentials(id string, c ManagedCredentials, agent bool) ([]byte, error) {
	if !validToken(c.Control) || (agent && !validToken(c.Agent)) || (!agent && c.Agent != "") {
		return nil, ErrCredentials
	}
	if _, err := c.TLS(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, ErrCredentials
	}
	defer clear(raw)
	return r.seal.Seal(nil, nil, raw, credentialOwner(id)), nil
}

func credentialOwner(id string) []byte { return []byte("telemt-node-credentials:v1:" + id) }

func validToken(token string) bool {
	return len(token) >= 32 && len(token) <= 256 && !strings.ContainsAny(token, "\r\n")
}

func (r *Registry) openCredentials(n Node) (ManagedCredentials, error) {
	if r.seal == nil {
		return ManagedCredentials{}, ErrCredentials
	}
	raw, err := r.seal.Open(nil, nil, r.managed[n.ID], credentialOwner(n.ID))
	if err != nil {
		return ManagedCredentials{}, ErrCredentials
	}
	defer clear(raw)
	var credentials ManagedCredentials
	if json.Unmarshal(raw, &credentials) != nil || !validToken(credentials.Control) ||
		(n.Binding.URL != "" && !validToken(credentials.Agent)) {
		return ManagedCredentials{}, ErrCredentials
	}
	return credentials, nil
}

func (r *Registry) AgentClient(snapshot Node) (*nodeagent.Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, exists := r.nodes[snapshot.ID]
	if !exists || !n.Enabled || n.Revision != snapshot.Revision {
		return nil, ErrConflict
	}
	if !n.ManagedCredentials {
		return nodeagent.NewClient(n.Binding)
	}
	credentials, err := r.openCredentials(n)
	if err != nil {
		return nil, err
	}
	trust, err := credentials.TLS()
	if err != nil {
		return nil, err
	}
	return nodeagent.NewManagedClient(n.Binding, credentials.Agent, trust)
}
