//go:build !lite

package platform

import (
	"context"
	"errors"
	"github.com/amirotin/telemt_panel/internal/ingress"
	"strings"
	"testing"
)

func TestRoutingCASMembershipAndDurability(t *testing.T) {
	s, path, key := testStore(t)
	ctx := context.Background()
	c, e := s.CreateCluster(ctx, "Ingress")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.AddMember(ctx, c.ID, c.Revision, "node-a"); e != nil {
		t.Fatal(e)
	}
	cfg := ingress.Config{Domain: "proxy.example.test", NativePort: 8443, WebPort: 443, NativeBalance: "source", Targets: []ingress.Target{{NodeID: "node-a", NativeAddress: "192.0.2.1:443", WebAddress: "192.0.2.1:18080", Weight: 10, Drain: true}}}
	if e = s.PutRouting(ctx, c.ID, 1, cfg); !errors.Is(e, ErrConflict) {
		t.Fatal("stale write", e)
	}
	if e = s.PutRouting(ctx, c.ID, 2, cfg); e != nil {
		t.Fatal(e)
	}
	raw, e := s.ExportRouting(ctx, c.ID, 3)
	if e != nil || !strings.Contains(raw, "weight 0") {
		t.Fatal(raw, e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(path, key)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	saved, e := s.GetRouting(ctx, c.ID)
	if e != nil || saved.Config == nil || saved.Config.Domain != cfg.Domain || saved.Revision != 3 {
		t.Fatal(saved, e)
	}
	if e = s.AddMember(ctx, c.ID, 3, "node-b"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ExportRouting(ctx, c.ID, 4); !errors.Is(e, ErrConflict) {
		t.Fatal("unreviewed membership exported", e)
	}
	cfg.Targets = append(cfg.Targets, ingress.Target{NodeID: "foreign", NativeAddress: "192.0.2.2:443", WebAddress: "192.0.2.2:18080", Weight: 1})
	if e = s.PutRouting(ctx, c.ID, 4, cfg); !errors.Is(e, ErrConflict) {
		t.Fatal("foreign target", e)
	}
	members, e := s.ListMembers(ctx, c.ID)
	if e != nil || members[0].Weight != 10 || !members[0].Drain {
		t.Fatal("failed transaction changed membership", members, e)
	}
}
