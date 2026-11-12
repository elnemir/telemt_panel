package host

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSystemdSocketScopeAndResult(t *testing.T) {
	dir, err := os.MkdirTemp("", "telemt-socket-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "control")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	commands := make(chan string, 4)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(conn).ReadString('\n')
			commands <- line
			if line != "stop\n" {
				_, _ = io.WriteString(conn, "ok\n")
			}
			_ = conn.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	manager := NewSystemdSocket(path, nil)
	if err := manager.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(ctx, "telemt.service"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Restart(ctx, "telemt-agent.service"); !errors.Is(err, ErrPrivilegesUnavailable) {
		t.Fatalf("non-Telemt unit accepted: %v", err)
	}
	if err := manager.Restart(ctx, "telemt"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(ctx, "telemt"); err == nil {
		t.Fatal("unconfirmed result accepted")
	}
	for _, want := range []string{"ping\n", "start\n", "restart\n", "stop\n"} {
		if got := <-commands; got != want {
			t.Fatalf("socket command %q, want %q", got, want)
		}
	}
}
