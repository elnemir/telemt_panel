//go:build !lite

package platform

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestDesiredAndJobsCommitAtomicallyAndSurviveReopen(t *testing.T) {
	s, path, key := testStore(t)
	ctx := context.Background()
	c, e := s.CreateCluster(ctx, " Europe ")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.CreateCluster(ctx, "europe"); !errors.Is(e, ErrConflict) {
		t.Fatal("duplicate name", e)
	}
	for index, id := range []string{"node-a", "node-b"} {
		if e = s.AddMember(ctx, c.ID, int64(index+1), id); e != nil {
			t.Fatal(e)
		}
	}
	const secret = "0123456789abcdef0123456789abcdef"
	input := IdentityInput{Username: "user", Secret: secret, Policy: Policy{Enabled: true}}
	if _, e = s.CreateIdentity(ctx, c.ID, 1, input); !errors.Is(e, ErrConflict) {
		t.Fatal("stale revision", e)
	}
	users, e := s.ListIdentities(ctx, c.ID, 0, 20)
	if e != nil || len(users) != 0 {
		t.Fatal(users, e)
	}
	i, e := s.CreateIdentity(ctx, c.ID, 3, input)
	if e != nil {
		t.Fatal(e)
	}
	jobs, e := s.IdentityJobs(ctx, i.ID)
	if e != nil || len(jobs) != 2 {
		t.Fatal(jobs, e)
	}
	for _, j := range jobs {
		if j.DesiredRevision != 1 || j.Status != "pending" {
			t.Fatal(j)
		}
	}
	if e = s.AddMember(ctx, c.ID, 3, "node-c"); e != nil {
		t.Fatal(e)
	}
	jobs, e = s.IdentityJobs(ctx, i.ID)
	if e != nil || len(jobs) != 3 {
		t.Fatal(jobs, e)
	}
	if e = s.UpdateIdentity(ctx, i.ID, 1, Policy{Enabled: false}, true, false); e != nil {
		t.Fatal(e)
	}
	if e = s.UpdateIdentity(ctx, i.ID, 1, input.Policy, false, false); !errors.Is(e, ErrConflict) {
		t.Fatal("stale update", e)
	}
	// A failing job insertion rolls the identity back as well.
	if _, e = s.db.ExecContext(ctx, `CREATE TRIGGER reject_job BEFORE INSERT ON identity_jobs BEGIN SELECT RAISE(ABORT,'injected job failure'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CreateIdentity(ctx, c.ID, 4, IdentityInput{Username: "rollback", Policy: input.Policy}); e == nil {
		t.Fatal("expected job failure")
	}
	users, e = s.ListIdentities(ctx, c.ID, 0, 20)
	if e != nil || len(users) != 1 {
		t.Fatal("orphan identity", users, e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(path)
	if e != nil || strings.Contains(string(data), secret) {
		t.Fatal("plaintext persisted", e)
	}
	reopened, e := Open(path, key)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	current, e := reopened.GetIdentity(ctx, i.ID)
	if e != nil || current.Revision != 2 || current.Policy.Enabled {
		t.Fatal(current, e)
	}
	got, e := reopened.RevealIdentitySecret(ctx, i.ID)
	if e != nil || len(got) != 32 || got == secret {
		t.Fatal("rotation lost", e)
	}
	jobs, e = reopened.IdentityJobs(ctx, i.ID)
	if e != nil || len(jobs) != 3 {
		t.Fatal(jobs, e)
	}
	for _, j := range jobs {
		if j.DesiredRevision != 2 {
			t.Fatal(j)
		}
	}
}
func TestConcurrentPolicyChangesHaveOneRevisionWinner(t *testing.T) {
	s, _, i, _, _, _ := identityFixture(t)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			e := s.UpdateIdentity(context.Background(), i.ID, 1, Policy{Enabled: false}, true, false)
			if e == nil {
				wins.Add(1)
			} else if !errors.Is(e, ErrConflict) {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal(wins.Load())
	}
	if j := job(t, s, i.ID); j.DesiredRevision != 2 {
		t.Fatal(j)
	}
}
func TestNodeOwnershipAndRevokedNamesRemainReserved(t *testing.T) {
	s, c, i, _, _, _ := identityFixture(t)
	ctx := context.Background()
	other, e := s.CreateCluster(ctx, "Other")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.AddMember(ctx, other.ID, 1, "n1"); !errors.Is(e, ErrConflict) {
		t.Fatal("node joined two clusters", e)
	}
	if e = s.UpdateIdentity(ctx, i.ID, 1, i.Policy, false, true); e != nil {
		t.Fatal(e)
	}
	if owned, e := s.ManagedUser(ctx, "n1", i.Username); e != nil || !owned {
		t.Fatal(owned, e)
	}
	if e = s.UpdateIdentity(ctx, i.ID, 2, i.Policy, false, false); !errors.Is(e, ErrConflict) {
		t.Fatal("revoked identity revived", e)
	}
	if e = s.DeleteEmptyCluster(ctx, c.ID, c.Revision); !errors.Is(e, ErrConflict) {
		t.Fatal("deleted owned state", e)
	}
	if e = s.DeleteEmptyCluster(ctx, other.ID, 1); e != nil {
		t.Fatal(e)
	}
}
