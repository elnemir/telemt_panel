//go:build !lite

package platform

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/amirotin/telemt_panel/internal/telemt"
)

func TestClusterConfigurationMembershipAndLease(t *testing.T) {
	ctx := context.Background()
	store, _, _ := testStore(t)
	now := time.Unix(1900000000, 0)
	store.now = func() time.Time { return now }
	cluster, err := store.CreateCluster(ctx, "Config")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddMember(ctx, cluster.ID, 1, "a"); err != nil {
		t.Fatal(err)
	}
	values := ClusterConfigValues{"timeouts.client_handshake": json.RawMessage("11")}
	if err := store.PutClusterConfig(ctx, cluster.ID, 1, "a", values); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale cluster revision accepted: %v", err)
	}
	if err := store.PutClusterConfig(ctx, cluster.ID, 2, "a", values); err != nil {
		t.Fatal(err)
	}
	work, err := store.claimClusterConfig(ctx)
	if err != nil || work.NodeID != "a" || work.DesiredRevision != 3 {
		t.Fatalf("incorrect first work item: %v %v", work, err)
	}
	if err := store.AddMember(ctx, cluster.ID, 3, "b"); err != nil {
		t.Fatal(err)
	}
	if err := store.PutClusterConfig(ctx, cluster.ID, 4, "b", values); err != nil {
		t.Fatal(err)
	}
	if err := store.finishClusterConfig(ctx, work, configDelivery{status: "applied"}); err != nil {
		t.Fatal(err)
	}
	settings, err := store.ClusterConfig(ctx, cluster.ID)
	if err != nil || settings.Template == nil || settings.Template.Revision != 5 || len(settings.Jobs) != 2 {
		t.Fatalf("template not queued on both nodes: %+v %v", settings, err)
	}
	for _, job := range settings.Jobs {
		if job.DesiredRevision != 5 || job.AppliedRevision != 0 || job.Status != "pending" {
			t.Fatalf("stale work marked new revision as applied: %+v", job)
		}
	}
	work, err = store.claimClusterConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveMember(ctx, cluster.ID, 5, work.NodeID); err != nil {
		t.Fatal(err)
	}
	if err := store.finalizeRemovals(ctx); err != nil {
		t.Fatal(err)
	}
	if owner, _ := store.NodeCluster(ctx, work.NodeID); owner != cluster.ID {
		t.Fatal("membership released with in-flight configuration")
	}
	if err := store.finishClusterConfig(ctx, work, configDelivery{status: "unavailable", uncertain: true}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(clusterConfigLease)
	if err := store.finalizeRemovals(ctx); err != nil {
		t.Fatal(err)
	}
	if owner, _ := store.NodeCluster(ctx, work.NodeID); owner != "" {
		t.Fatal("membership did not release after the lease expired")
	}
}

type configurationNodeFixture struct {
	sections  telemt.ConfigSections
	state     telemt.ConfigPublication
	validate  telemt.ConfigValidation
	published bool
}

func (n *configurationNodeFixture) GetConfig(context.Context) (telemt.ConfigSections, string, error) {
	return n.sections, "r1", nil
}
func (n *configurationNodeFixture) ClusterConfigPublication(context.Context) (telemt.ConfigPublication, error) {
	return n.state, nil
}
func (n *configurationNodeFixture) ValidateClusterConfig(context.Context, map[string]any, string) (telemt.ConfigValidation, error) {
	return n.validate, nil
}
func (n *configurationNodeFixture) PublishClusterConfig(context.Context, map[string]any, string, string) (telemt.PatchConfigResult, error) {
	n.published = true
	return telemt.PatchConfigResult{}, nil
}

func TestClusterConfigurationRequiresPublishedReadback(t *testing.T) {
	ctx := context.Background()
	values := ClusterConfigValues{"timeouts.client_handshake": json.RawMessage("11")}
	node := configurationNodeFixture{
		sections: telemt.ConfigSections{"timeouts": json.RawMessage(`{"client_handshake":11}`)},
		state:    telemt.ConfigPublication{DesiredRevision: "r1", DesiredStatus: "valid", RuntimeSampleCurrent: true},
		validate: telemt.ConfigValidation{Valid: true, BaseRevision: "r1"},
	}
	allowWrite := func() error { return nil }
	node.state.Comparison.Status = "pending_publication"
	node.state.Comparison.PendingPaths = []string{"timeouts.client_handshake"}
	node.validate.Runtime.Match = "runtime"
	node.validate.Apply.GenerationPaths = []string{"server.api.auth_header"}
	if got := reconcileClusterConfig(ctx, &node, values, allowWrite); got.code != "local_pending" || node.published {
		t.Fatalf("unrelated pending settings must not be activated: %+v", got)
	}
	node.validate.Apply.GenerationPaths = []string{"timeouts.client_handshake"}
	if got := reconcileClusterConfig(ctx, &node, values, allowWrite); got.status != "pending" || !node.published {
		t.Fatalf("patch acknowledgment is not runtime confirmation: %+v", got)
	}
	node.state.Comparison.Status = "published_match"
	node.state.Comparison.PendingPaths = nil
	node.published = false
	if got := reconcileClusterConfig(ctx, &node, values, allowWrite); got.status != "applied" || node.published {
		t.Fatalf("matching published settings should not be written again: %+v", got)
	}
	if _, err := ReadClusterConfigValues(node.sections, []string{"server.api.auth_header"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted a node-local field: %v", err)
	}
}
