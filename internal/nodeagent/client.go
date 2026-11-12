package nodeagent

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var IDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{16,128}$`)
var EnvPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

const IdentityHeader = "X-Telemt-Agent-ID"

var ErrCredentials = errors.New("agent credentials unavailable")
var ErrIdentity = errors.New("agent identity mismatch")
var ErrResponse = errors.New("agent response invalid")

type Binding struct {
	URL     string `json:"agent_url,omitempty"`
	ID      string `json:"agent_id,omitempty"`
	AuthEnv string `json:"agent_auth_env,omitempty"`
}

func ValidateBinding(b Binding) error { return validateBinding(b, false) }

func ValidateManagedBinding(b Binding) error { return validateBinding(b, true) }

func validateBinding(b Binding, managed bool) error {
	if b.URL == "" && b.ID == "" && b.AuthEnv == "" {
		return nil
	}
	if !IDPattern.MatchString(b.ID) || (!managed && !EnvPattern.MatchString(b.AuthEnv)) ||
		(managed && b.AuthEnv != "") || len(b.URL) > 2048 {
		return ErrIdentity
	}
	u, err := url.Parse(b.URL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(b.URL, "#") || u.RawPath != "" || u.Opaque != "" || strings.ContainsAny(u.Path, "\\") || strings.Contains(u.Path, "//") {
		return ErrIdentity
	}
	if port := u.Port(); port != "" {
		value, err := strconv.ParseUint(port, 10, 16)
		if err != nil || value == 0 {
			return ErrIdentity
		}
	}
	for _, p := range strings.Split(u.Path, "/") {
		if p == "." || p == ".." {
			return ErrIdentity
		}
	}
	if u.Scheme != "https" {
		if managed {
			return ErrIdentity
		}
		ip, e := netip.ParseAddr(u.Hostname())
		if u.Scheme != "http" || (u.Hostname() != "localhost" && (e != nil || !ip.IsLoopback())) {
			return ErrIdentity
		}
	}
	return nil
}

type Client struct {
	binding Binding
	auth    string
	http    *http.Client
}

func NewClient(b Binding) (*Client, error) {
	if b.URL == "" || ValidateBinding(b) != nil {
		return nil, ErrIdentity
	}
	return newClient(b, "", nil), nil
}

func NewManagedClient(b Binding, auth string, trust *tls.Config) (*Client, error) {
	if b.URL == "" || ValidateManagedBinding(b) != nil || trust == nil {
		return nil, ErrIdentity
	}
	if len(auth) < 32 || strings.ContainsAny(auth, "\r\n") {
		return nil, ErrCredentials
	}
	return newClient(b, auth, trust), nil
}

func newClient(b Binding, auth string, trust *tls.Config) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if trust != nil {
		tr.TLSClientConfig = trust
		tr.Proxy = nil
	}
	tr.ResponseHeaderTimeout = 55 * time.Second
	return &Client{binding: b, auth: auth, http: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *Client) Close() { c.http.CloseIdleConnections() }
func (c *Client) Request(ctx context.Context, method, path, key string) (*http.Response, error) {
	return c.RequestBody(ctx, method, path, key, nil)
}
func (c *Client) RequestBody(ctx context.Context, method, path, key string, body []byte) (*http.Response, error) {
	value := c.auth
	if value == "" {
		value = os.Getenv(c.binding.AuthEnv)
	}
	if len(value) < 32 || strings.ContainsAny(value, "\r\n") {
		return nil, ErrCredentials
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.binding.URL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, ErrResponse
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", value)
	req.Header.Set(IdentityHeader, c.binding.ID)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if res.Header.Get(IdentityHeader) != c.binding.ID {
		res.Body.Close()
		return nil, ErrIdentity
	}
	return res, nil
}
func ReadBody(r *http.Response) ([]byte, error) { return ReadLimitedBody(r, 2*1024*1024) }
func ReadLimitedBody(r *http.Response, limit int) ([]byte, error) {
	defer r.Body.Close()
	b, e := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
	if e != nil || len(b) > limit {
		return nil, ErrResponse
	}
	return b, nil
}
