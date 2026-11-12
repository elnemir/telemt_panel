package host

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"time"
)

// SystemdSocket controls the installer-owned unit over its private local socket.
// Status remains an ordinary unprivileged systemctl read.
type SystemdSocket struct {
	*Systemd
	path string
}

func NewSystemdSocket(path string, statusRunner CmdRunner) *SystemdSocket {
	return &SystemdSocket{Systemd: NewSystemd(statusRunner), path: path}
}

func (s *SystemdSocket) Probe(ctx context.Context) error { return s.request(ctx, "ping") }

func (s *SystemdSocket) Start(ctx context.Context, service string) error {
	return s.control(ctx, service, "start")
}

func (s *SystemdSocket) Stop(ctx context.Context, service string) error {
	return s.control(ctx, service, "stop")
}

func (s *SystemdSocket) Restart(ctx context.Context, service string) error {
	return s.control(ctx, service, "restart")
}

func (s *SystemdSocket) control(ctx context.Context, service, verb string) error {
	if strings.TrimSuffix(service, ".service") != "telemt" {
		return ErrPrivilegesUnavailable
	}
	return s.request(ctx, verb)
}

func (s *SystemdSocket) request(ctx context.Context, verb string) error {
	ctx, cancel := context.WithTimeout(ctx, 28*time.Second)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", s.path)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if _, err := io.WriteString(conn, verb+"\n"); err != nil {
		return err
	}
	var reply [3]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return err
	}
	if string(reply[:]) != "ok\n" {
		return errors.New("systemd control request failed")
	}
	return nil
}
