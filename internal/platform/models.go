package platform

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"
)

const MaxClusters = 256
const MaxMembers = 64
const MaxIdentities = 10000

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

type Cluster struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Revision  int64  `json:"revision"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}
type Member struct {
	NodeID    string `json:"node_id"`
	ClusterID string `json:"cluster_id"`
	State     string `json:"state"`
	Weight    int    `json:"weight"`
	Drain     bool   `json:"drain"`
}

// Policy owns account conditions. Traffic quota allocation belongs to the
// cluster quota authority, never to per-node copies of the same total limit.
type Policy struct {
	Enabled           bool    `json:"enabled"`
	MaxTCPConns       *uint64 `json:"max_tcp_conns"`
	MaxUniqueIPs      *uint64 `json:"max_unique_ips"`
	RateLimitUpBps    *uint64 `json:"rate_limit_up_bps"`
	RateLimitDownBps  *uint64 `json:"rate_limit_down_bps"`
	ExpirationRFC3339 string  `json:"expiration_rfc3339"`
	UserAdTag         string  `json:"user_ad_tag"`
}
type Identity struct {
	BillingManaged bool   `json:"billing_managed"`
	ID             string `json:"id"`
	ClusterID      string `json:"cluster_id"`
	Username       string `json:"username"`
	Policy         Policy `json:"policy"`
	Revision       int64  `json:"revision"`
	Revoked        bool   `json:"revoked"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}
type IdentityInput struct {
	Username string `json:"username"`
	Policy   Policy `json:"policy"`
	Secret   string `json:"secret,omitempty"`
}
type Job struct {
	IdentityID         string `json:"identity_id"`
	NodeID             string `json:"node_id"`
	DesiredRevision    int64  `json:"desired_revision"`
	AppliedRevision    int64  `json:"applied_revision"`
	Status             string `json:"status"`
	Attempts           int    `json:"attempts"`
	NextAttempt        int64  `json:"next_attempt"`
	LastCode           string `json:"last_code"`
	InRuntime          bool   `json:"in_runtime"`
	UpdatedAt          int64  `json:"updated_at"`
	leaseToken         string
	previousSecretHash string
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if len(name) == 0 || len(name) > 128 {
		return "", ErrInvalid
	}
	return name, nil
}
func cleanPolicy(p Policy) (Policy, error) {
	// Zero has no positive override in Telemt. Canonical nil makes readback
	// comparable and explicitly means inheritance for connection/IP limits.
	for _, field := range []**uint64{&p.MaxTCPConns, &p.MaxUniqueIPs, &p.RateLimitUpBps, &p.RateLimitDownBps} {
		if *field != nil && **field == 0 {
			*field = nil
		}
	}
	for _, rate := range []*uint64{p.RateLimitUpBps, p.RateLimitDownBps} {
		if rate != nil && *rate > 100000000000 {
			return Policy{}, ErrInvalid
		}
	}
	if p.UserAdTag != "" {
		raw, e := hex.DecodeString(p.UserAdTag)
		if e != nil || len(raw) != 16 {
			return Policy{}, ErrInvalid
		}
		p.UserAdTag = strings.ToLower(p.UserAdTag)
	}
	if p.ExpirationRFC3339 != "" {
		v, e := time.Parse(time.RFC3339Nano, p.ExpirationRFC3339)
		if e != nil {
			return Policy{}, ErrInvalid
		}
		p.ExpirationRFC3339 = v.UTC().Format(time.RFC3339Nano)
	}
	return p, nil
}
func randomSecret() string {
	var secret [16]byte
	_, _ = rand.Read(secret[:])
	return hex.EncodeToString(secret[:])
}
func secretHash(secret string) string {
	v := sha256.Sum256([]byte(strings.ToLower(secret)))
	return hex.EncodeToString(v[:])
}
