package customer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"io"
	"math/big"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amirotin/telemt_panel/internal/config"
)

func smtpCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	raw, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	private, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	cert, e := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))
	if e != nil {
		t.Fatal(e)
	}
	parsed, e := x509.ParseCertificate(raw)
	if e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return cert, roots
}
func TestSMTPRequiresVerifiedTLSAndAcceptsDATAWithoutQUITReply(t *testing.T) {
	for _, tc := range []struct {
		name, mode                string
		trusted, upgrade, success bool
	}{{"implicit TLS", "tls", true, true, true}, {"STARTTLS", "starttls", true, true, true}, {"untrusted TLS", "tls", false, true, false}, {"no STARTTLS", "starttls", true, false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			cert, roots := smtpCertificate(t)
			listener, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			defer listener.Close()
			done := make(chan struct{})
			var authCount atomic.Int32
			message := make(chan []byte, 1)
			go func() {
				defer close(done)
				conn, e := listener.Accept()
				if e != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				upgrade := func() bool {
					secure := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
					if secure.Handshake() != nil {
						return false
					}
					conn = secure
					return true
				}
				if tc.mode == "tls" && !upgrade() {
					return
				}
				reader := textproto.NewReader(bufio.NewReader(conn))
				_, _ = io.WriteString(conn, "220 localhost ESMTP\r\n")
				for {
					line, e := reader.ReadLine()
					if e != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"):
						if tc.mode == "starttls" && tc.upgrade {
							_, _ = io.WriteString(conn, "250-localhost\r\n250-STARTTLS\r\n250 AUTH PLAIN\r\n")
						} else {
							_, _ = io.WriteString(conn, "250-localhost\r\n250 AUTH PLAIN\r\n")
						}
					case line == "STARTTLS":
						_, _ = io.WriteString(conn, "220 ready\r\n")
						if !upgrade() {
							return
						}
						reader = textproto.NewReader(bufio.NewReader(conn))
					case strings.HasPrefix(line, "AUTH PLAIN "):
						authCount.Add(1)
						raw, e := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
						if e != nil || string(raw) != "\x00sender\x00smtp-fixture-password" {
							t.Error("SMTP credentials mismatch")
							return
						}
						_, _ = io.WriteString(conn, "235 authenticated\r\n")
					case strings.HasPrefix(line, "MAIL FROM:"), strings.HasPrefix(line, "RCPT TO:"):
						_, _ = io.WriteString(conn, "250 accepted\r\n")
					case line == "DATA":
						_, _ = io.WriteString(conn, "354 send data\r\n")
						raw, e := reader.ReadDotBytes()
						if e != nil {
							return
						}
						message <- raw
						_, _ = io.WriteString(conn, "250 queued\r\n")
					case line == "QUIT":
						return // Message was accepted; deliberately lose QUIT reply.
					default:
						t.Error("unexpected SMTP command")
						return
					}
				}
			}()
			host, port, _ := net.SplitHostPort(listener.Addr().String())
			number, _ := strconv.Atoi(port)
			t.Setenv("TELEMT_CUSTOMER_SMTP_WIRE", "smtp-fixture-password")
			sender := smtpMailer{cfg: config.CustomerEmailConfig{Host: host, Port: number, Mode: tc.mode, From: "noreply@example.test", Username: "sender", PasswordEnv: "TELEMT_CUSTOMER_SMTP_WIRE"}}
			if tc.trusted {
				sender.roots = roots
			}
			e = sender.Send(context.Background(), "recipient@example.test", "https://panel.example.test/portal#email_token=fixture")
			if (e == nil) != tc.success {
				t.Fatal("delivery result", e)
			}
			<-done
			if !tc.success {
				if authCount.Load() != 0 {
					t.Fatal("credentials sent without verified TLS")
				}
				return
			}
			if authCount.Load() != 1 {
				t.Fatal("missing SMTP authentication")
			}
			parsed, e := mail.ReadMessage(bytes.NewReader(<-message))
			if e != nil {
				t.Fatal(e)
			}
			body, e := io.ReadAll(quotedprintable.NewReader(parsed.Body))
			if e != nil || !strings.Contains(string(body), "#email_token=fixture") {
				t.Fatal("bad message", e)
			}
			if parsed.Header.Get("To") != "recipient@example.test" || parsed.Header.Get("From") != "noreply@example.test" {
				t.Fatal("wrong recipient")
			}
		})
	}
}
