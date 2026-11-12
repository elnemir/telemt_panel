//go:build !lite

package nodeinstall

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const ControlListen = "127.0.0.1:9091"
const AgentListen = "127.0.0.1:9092"

func gatewayHandler(peer netip.Addr, control, agent http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		address, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil || address.Addr().Unmap() != peer.Unmap() {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/agent/") || r.URL.Path == "/v1/agent" {
			if agent != nil {
				agent.ServeHTTP(w, r)
				return
			}
		} else if strings.HasPrefix(r.URL.Path, "/v1/") {
			control.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

func upstream(host string) *httputil.ReverseProxy {
	target := &url.URL{Scheme: "http", Host: host}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 60 * time.Second
	return &httputil.ReverseProxy{
		Rewrite:   func(r *httputil.ProxyRequest) { r.SetURL(target) },
		Transport: transport,
	}
}

// RunGateway provides one TLS origin for the two fixed loopback APIs. It
// never relays to a caller-supplied address or exposes the panel application.
func RunGateway(ctx context.Context, listen, cert, key string, peer netip.Addr, withAgent bool) error {
	if !peer.IsValid() {
		return errors.New("invalid gateway peer")
	}
	var agent http.Handler
	if withAgent {
		agent = upstream(AgentListen)
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{
		Handler:           gatewayHandler(peer, upstream(ControlListen), agent),
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       45 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	defer stop()
	err = server.ServeTLS(listener, cert, key)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
