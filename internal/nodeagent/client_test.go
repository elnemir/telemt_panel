package nodeagent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBindingValidation(t *testing.T) {
	for _, url := range []string{"http://public.example", "https://user:pass@example.com", "https://example.com/../x", "https://example.com?a=b", "https://example.com/#x"} {
		if ValidateBinding(Binding{URL: url, ID: "agent-identity-01", AuthEnv: "TEST_AGENT_AUTH"}) == nil {
			t.Fatal(url)
		}
	}
	for _, url := range []string{"https://example.com", "https://example.com/agent", "http://127.0.0.1:9092", "http://[::1]:9092"} {
		if err := ValidateBinding(Binding{URL: url, ID: "agent-identity-01", AuthEnv: "TEST_AGENT_AUTH"}); err != nil {
			t.Fatal(url, err)
		}
	}
}
func TestClientIdentityAndCredentialBoundary(t *testing.T) {
	const token = "Bearer test-agent-authorization-long-enough"
	t.Setenv("TEST_AGENT_AUTH", token)
	wrong := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != token || r.Header.Get(IdentityHeader) != "agent-identity-01" {
			t.Error("credentials or identity absent")
		}
		if wrong {
			w.Header().Set(IdentityHeader, "agent-identity-02")
		} else {
			w.Header().Set(IdentityHeader, "agent-identity-01")
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	c, err := NewClient(Binding{URL: srv.URL, ID: "agent-identity-01", AuthEnv: "TEST_AGENT_AUTH"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r, err := c.Request(context.Background(), "GET", "/v1/agent/health", "")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	wrong = true
	_, err = c.Request(context.Background(), "GET", "/v1/agent/health", "")
	if !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	t.Setenv("TEST_AGENT_AUTH", "")
	_, err = c.Request(context.Background(), "GET", "/v1/agent/health", "")
	if !errors.Is(err, ErrCredentials) {
		t.Fatal(err)
	}
}
