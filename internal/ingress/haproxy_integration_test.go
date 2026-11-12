package ingress

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Run only inside the dedicated network-none HAProxy test container. The fixed
// TLS path belongs to that disposable container, never the developer's host.
func TestRealHAProxyNativeTupleAndWebAffinity(t *testing.T) {
	binaryPath := os.Getenv("TELEMT_TEST_HAPROXY")
	if binaryPath == "" {
		t.Skip("requires isolated HAProxy container")
	}
	if os.Getenv("TELEMT_TEST_CONTAINER") != "1" {
		t.Fatal("container-only fixed TLS path")
	}
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "proxy.example.test"}, DNSNames: []string{"proxy.example.test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	bundle := append(append([]byte{}, certPEM...), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})...)
	if e = os.MkdirAll("/etc/telemt-ingress", 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile("/etc/telemt-ingress/tls.pem", bundle, 0600); e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	targets := []Target{}
	for _, name := range []string{"a", "b"} {
		ln, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				conn, e := ln.Accept()
				if e != nil {
					return
				}
				go func() {
					defer conn.Close()
					conn.SetDeadline(time.Now().Add(time.Second))
					head := make([]byte, 16)
					if _, e := io.ReadFull(conn, head); e != nil {
						return
					}
					if !bytes.Equal(head[:12], []byte("\r\n\r\n\x00\r\nQUIT\n")) || head[12] != 0x21 || head[13] != 0x11 {
						return
					}
					addr := make([]byte, int(binary.BigEndian.Uint16(head[14:])))
					if len(addr) != 12 {
						return
					}
					if _, e := io.ReadFull(conn, addr); e != nil {
						return
					}
					payload := make([]byte, 7)
					if _, e := io.ReadFull(conn, payload); e != nil {
						return
					}
					wire, _ := json.Marshal(map[string]any{"node": name, "source": net.IP(addr[:4]).String(), "destination": net.IP(addr[4:8]).String(), "source_port": binary.BigEndian.Uint16(addr[8:10]), "destination_port": binary.BigEndian.Uint16(addr[10:12]), "payload": string(payload)})
					conn.Write(wire)
				}()
			}
		}()
		web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(io.LimitReader(r.Body, 1024))
			json.NewEncoder(w).Encode(map[string]string{"node": name, "host": r.Host, "xff": r.Header.Get("X-Forwarded-For"), "forwarded": r.Header.Get("Forwarded"), "uri": r.RequestURI, "body": string(raw), "authorization": r.Header.Get("Authorization")})
		}))
		t.Cleanup(web.Close)
		targets = append(targets, Target{NodeID: name, NativeAddress: ln.Addr().String(), WebAddress: strings.TrimPrefix(web.URL, "http://"), Weight: 1})
	}
	freePort := func() int {
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		p := l.Addr().(*net.TCPAddr).Port
		l.Close()
		return p
	}
	for _, drain := range []bool{false, true} {
		t.Run(map[bool]string{false: "balanced", true: "draining"}[drain], func(t *testing.T) {
			cfg := Config{Domain: "proxy.example.test", NativePort: freePort(), WebPort: 443, NativeBalance: "roundrobin", Targets: append([]Target(nil), targets...)}
			cfg.Targets[0].Drain = drain
			raw, e := cfg.Render()
			if e != nil {
				t.Fatal(e)
			}
			file := filepath.Join(t.TempDir(), "haproxy.cfg")
			os.WriteFile(file, []byte(raw), 0600)
			if out, e := exec.Command(binaryPath, "-c", "-f", file).CombinedOutput(); e != nil {
				t.Fatalf("configuration rejected: %v %s", e, out)
			}
			command := exec.Command(binaryPath, "-db", "-f", file)
			var log bytes.Buffer
			command.Stdout = &log
			command.Stderr = &log
			if e = command.Start(); e != nil {
				t.Fatal(e)
			}
			defer func() { command.Process.Kill(); command.Wait() }()
			tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: cfg.Domain}, DisableKeepAlives: true}
			defer tr.CloseIdleConnections()
			jar, _ := cookiejar.New(nil)
			client := &http.Client{Transport: tr, Jar: jar, Timeout: time.Second}
			endpoint := "https://127.0.0.1:" + strconv.Itoa(cfg.WebPort) + "/base/carrier?sequence=1%2F2"
			request := func() (*http.Response, error) {
				r, _ := http.NewRequest("POST", endpoint, strings.NewReader("unchanged"))
				r.Host = cfg.Domain + ":" + strconv.Itoa(cfg.WebPort)
				r.Header.Set("X-Forwarded-For", "198.51.100.44")
				r.Header.Set("Forwarded", "for=198.51.100.44")
				r.Header.Set("Authorization", "Bearer fixture-token")
				return client.Do(r)
			}
			deadline := time.Now().Add(10 * time.Second)
			ready := false
			for time.Now().Before(deadline) {
				res, e := request()
				if e == nil {
					io.Copy(io.Discard, res.Body)
					res.Body.Close()
					if res.StatusCode == 200 {
						ready = true
						break
					}
				}
				time.Sleep(100 * time.Millisecond)
			}
			if !ready {
				t.Fatal("ingress did not become ready")
			}
			var selected string
			for j := 0; j < 4; j++ {
				res, e := request()
				if e != nil {
					t.Fatal(e)
				}
				var got map[string]string
				e = json.NewDecoder(res.Body).Decode(&got)
				res.Body.Close()
				if e != nil || res.StatusCode != 200 || got["xff"] != "127.0.0.1" || got["forwarded"] != "" || got["host"] != cfg.Domain || got["uri"] != "/base/carrier?sequence=1%2F2" || got["body"] != "unchanged" || got["authorization"] != "Bearer fixture-token" {
					t.Fatal(got, e, res.StatusCode)
				}
				if selected != "" && got["node"] != selected {
					t.Fatal("WEB affinity changed", got)
				}
				selected = got["node"]
			}
			if drain && selected != "b" {
				t.Fatal("new client assigned drained node", selected)
			}
			if drain {
				r, _ := http.NewRequest("GET", endpoint, nil)
				r.Host = cfg.Domain
				r.AddCookie(&http.Cookie{Name: "TELEMT_INGRESS", Value: serverName("a")})
				pinned := &http.Client{Transport: tr, Timeout: time.Second}
				res, e := pinned.Do(r)
				if e != nil {
					t.Fatal(e)
				}
				var got map[string]string
				json.NewDecoder(res.Body).Decode(&got)
				res.Body.Close()
				if got["node"] != "a" {
					t.Fatal("drain lost existing affinity", got)
				}
			}
			seen := map[string]bool{}
			for j := 0; j < 4; j++ {
				conn, e := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(cfg.NativePort), time.Second)
				if e != nil {
					t.Fatal(e)
				}
				conn.SetDeadline(time.Now().Add(time.Second))
				sourcePort := conn.LocalAddr().(*net.TCPAddr).Port
				conn.Write([]byte("payload"))
				var got struct {
					Node, Source, Destination, Payload string
					SourcePort                         int `json:"source_port"`
					DestinationPort                    int `json:"destination_port"`
				}
				e = json.NewDecoder(conn).Decode(&got)
				conn.Close()
				if e != nil || got.Source != "127.0.0.1" || got.Destination != "127.0.0.1" || got.SourcePort != sourcePort || got.DestinationPort != cfg.NativePort || got.Payload != "payload" {
					t.Fatal("tuple/payload mismatch", got, e)
				}
				seen[got.Node] = true
			}
			if (!drain && len(seen) != 2) || (drain && (len(seen) != 1 || !seen["b"])) {
				t.Fatal("native balancing/drain", seen)
			}
		})
	}
}
