package host

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"time"
)

// SystemdSocket controls the installer-owned unit over its private local socket.
// Older helpers can still report status through an unprivileged systemctl read.
type SystemdSocket struct {
	*Systemd
	path string
}

func NewSystemdSocket(path string, statusRunner CmdRunner) *SystemdSocket {
	return &SystemdSocket{Systemd: NewSystemd(statusRunner), path: path}
}

func (s *SystemdSocket) Probe(ctx context.Context) error { return s.request(ctx, "ping") }

func (s *SystemdSocket) Status(ctx context.Context, service string) (ServiceStatus, error) {
	if strings.TrimSuffix(service, ".service") != "telemt" {
		return StatusUnknown, ErrPrivilegesUnavailable
	}
	reply, err := s.exchange(ctx, "status")
	if err == nil {
		switch reply {
		case "running\n":
			return StatusRunning, nil
		case "stopped\n":
			return StatusStopped, nil
		case "unknown\n":
			return StatusUnknown, nil
		}
	}
	return s.Systemd.Status(ctx, service)
}

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
	reply, err := s.exchange(ctx, verb)
	if err != nil {
		return err
	}
	if reply != "ok\n" {
		return errors.New("systemd control request failed")
	}
	return nil
}

func (s *SystemdSocket) exchange(ctx context.Context, verb string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 28*time.Second)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", s.path)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return "", err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if _, err := io.WriteString(conn, verb+"\n"); err != nil {
		return "", err
	}
	reply, err := bufio.NewReaderSize(conn, 16).ReadSlice('\n')
	return string(reply), err
}
