package customer

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"time"

	"github.com/amirotin/telemt_panel/internal/config"
)

type smtpMailer struct {
	cfg   config.CustomerEmailConfig
	roots *x509.CertPool
}

func (m *smtpMailer) Send(ctx context.Context, to, link string) error {
	password, e := secretEnv(m.cfg.PasswordEnv)
	if e != nil {
		return e
	}
	roots := m.roots
	if m.cfg.CAFile != "" {
		file, e := os.Open(m.cfg.CAFile)
		if e != nil {
			return ErrDelivery
		}
		raw, e := io.ReadAll(io.LimitReader(file, 1024*1024+1))
		_ = file.Close()
		if e != nil || len(raw) > 1024*1024 {
			return ErrDelivery
		}
		roots, e = x509.SystemCertPool()
		if e != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(raw) {
			return ErrDelivery
		}
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, e := dialer.DialContext(ctx, "tcp", net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port)))
	if e != nil {
		return ErrDelivery
	}
	defer conn.Close()
	deadline := time.Now().Add(15 * time.Second)
	if until, ok := ctx.Deadline(); ok && until.Before(deadline) {
		deadline = until
	}
	if e = conn.SetDeadline(deadline); e != nil {
		return ErrDelivery
	}
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { _ = rawConn.Close() })
	defer stop()
	tc := &tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12, RootCAs: roots}
	if m.cfg.Mode == "tls" {
		secure := tls.Client(conn, tc)
		if e = secure.HandshakeContext(ctx); e != nil {
			return ErrDelivery
		}
		conn = secure
	}
	client, e := smtp.NewClient(conn, m.cfg.Host)
	if e != nil {
		return ErrDelivery
	}
	defer client.Close()
	if m.cfg.Mode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return ErrDelivery
		}
		if e = client.StartTLS(tc); e != nil {
			return ErrDelivery
		}
	}
	if e = client.Auth(smtp.PlainAuth("", m.cfg.Username, password, m.cfg.Host)); e != nil {
		return ErrDelivery
	}
	if e = client.Mail(m.cfg.From); e != nil {
		return ErrDelivery
	}
	if e = client.Rcpt(to); e != nil {
		return ErrDelivery
	}
	writer, e := client.Data()
	if e != nil {
		return ErrDelivery
	}
	var body bytes.Buffer
	_, _ = fmt.Fprintf(&body, "From: %s\r\nTo: %s\r\nDate: %s\r\nSubject: Telemt account verification\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", m.cfg.From, to, time.Now().UTC().Format(time.RFC1123Z))
	encoded := quotedprintable.NewWriter(&body)
	_, _ = fmt.Fprintf(encoded, "Confirm your Telemt account action using this one-time link:\n\n%s\n\nIf you did not request this action, ignore this message.\n", link)
	if e = encoded.Close(); e != nil {
		return ErrDelivery
	}
	if _, e = writer.Write(body.Bytes()); e != nil {
		return ErrDelivery
	}
	if e = writer.Close(); e != nil {
		return ErrDelivery
	}
	// DATA success is the SMTP acceptance boundary. A lost QUIT reply does not
	// revoke the accepted message or justify a second delivery.
	_ = client.Quit()
	return nil
}
