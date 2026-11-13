//go:build !lite

package platform

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/amirotin/telemt_panel/internal/telemt"
	"github.com/amirotin/telemt_panel/internal/userprojection"
)

func TestIngressLinksFollowOwnershipAndLastAppliedEndpoint(t *testing.T) {
	s, path, key := testStore(t)
	ctx := context.Background()
	c, err := s.CreateCluster(ctx, "Links")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(ctx, c.ID, c.Revision, "node-a"); err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("a", 32)
	identity, err := s.CreateIdentity(ctx, c.ID, c.Revision+1, IdentityInput{Username: "eln", Secret: secret, Policy: Policy{Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	direct := "tg://proxy?server=192.0.2.1&port=51974&secret=ee" + secret + "6578616d706c652e6f7267"
	users := func() []userprojection.User {
		return []userprojection.User{
			{UserInfo: telemt.UserInfo{Username: "eln", Links: telemt.UserLinks{TLS: []string{direct}}}},
			{UserInfo: telemt.UserInfo{Username: "local", Links: telemt.UserLinks{TLS: []string{direct}}}},
		}
	}
	project := func(store *Store, node string) []userprojection.User {
		t.Helper()
		got := users()
		if err := store.ProjectNodeUserLinks(ctx, node, got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := project(s, "node-a")
	if !got[0].ClusterManaged || len(got[0].Links.TLS) != 0 || got[1].ClusterManaged || got[1].Links.TLS[0] != direct {
		t.Fatalf("unapplied route or local user projected incorrectly: %+v", got)
	}
	if err := s.RecordAppliedIngress(ctx, c.ID, 4, "192.0.2.10", 10443); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAppliedIngress(ctx, c.ID, 3, "192.0.2.99", 8443); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got = project(s, "node-a")
	if len(got[0].Links.TLS) != 1 {
		t.Fatal("applied link missing")
	}
	link, err := url.Parse(got[0].Links.TLS[0])
	if err != nil || link.Query().Get("server") != "192.0.2.10" || link.Query().Get("port") != "10443" ||
		link.Query().Get("secret") != "ee"+secret+"6578616d706c652e6f7267" {
		t.Fatal("applied endpoint or FakeTLS value differs")
	}
	foreign := project(s, "node-b")
	if foreign[0].ClusterManaged || foreign[0].Links.TLS[0] != direct {
		t.Fatal("same username on an unrelated node is local")
	}
	if err := s.UpdateIdentity(ctx, identity.ID, identity.Revision, identity.Policy, true, false); err != nil {
		t.Fatal(err)
	}
	if got := project(s, "node-a"); len(got[0].Links.TLS) != 0 {
		t.Fatal("old identity secret was published after rotation")
	}
}
