package platform

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/amirotin/telemt_panel/internal/telemt"
)

type clusterConfigClient interface {
	GetConfig(context.Context) (telemt.ConfigSections, string, error)
	ClusterConfigPublication(context.Context) (telemt.ConfigPublication, error)
	ValidateClusterConfig(context.Context, map[string]any, string) (telemt.ConfigValidation, error)
	PublishClusterConfig(context.Context, map[string]any, string, string) (telemt.PatchConfigResult, error)
}

type configDelivery struct {
	status    string
	code      string
	uncertain bool
}

// RunClusterConfig has its own worker so preparing a config does not hold up
// identity revocations, quota allocations, or paid entitlement delivery.
func (w *Worker) RunClusterConfig(ctx context.Context, onError func(error)) {
	ticker := time.NewTicker(workerPoll)
	defer ticker.Stop()
	for ctx.Err() == nil {
		ran, err := w.processClusterConfig(ctx)
		if err != nil && ctx.Err() == nil && onError != nil {
			onError(err)
		}
		if ran && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) processClusterConfig(ctx context.Context) (bool, error) {
	work, err := w.store.claimClusterConfig(ctx)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	operation, cancel := context.WithTimeout(ctx, telemt.ConfigMutationTimeout)
	defer cancel()
	result := configDelivery{status: "unavailable", code: "node_unavailable"}
	err = w.withNode(operation, work.NodeID, func(ctx context.Context, node NodeClient) error {
		client, ok := node.(clusterConfigClient)
		if !ok {
			result = configDelivery{status: "conflict", code: "config_unsupported"}
			return nil
		}
		result = reconcileClusterConfig(ctx, client, work.settings, func() error {
			return w.store.checkClusterConfigWork(ctx, work)
		})
		return nil
	})
	if err != nil {
		result = configDelivery{status: "unavailable", code: "node_unavailable"}
	}
	finish, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelFinish()
	return true, w.store.finishClusterConfig(finish, work, result)
}

func reconcileClusterConfig(ctx context.Context, client clusterConfigClient, settings ClusterConfigValues, beforeWrite func() error) configDelivery {
	unconfirmed := configDelivery{status: "unavailable", code: "not_confirmed"}
	current, revision, err := client.GetConfig(ctx)
	if err != nil {
		return clusterConfigFailure(err, false)
	}
	publication, err := client.ClusterConfigPublication(ctx)
	if err != nil {
		return clusterConfigFailure(err, false)
	}
	if revision == "" || publication.DesiredRevision != revision || publication.DesiredStatus != "valid" ||
		!publication.RuntimeSampleCurrent || publication.Comparison.Truncated {
		return unconfirmed
	}
	switch publication.Comparison.Status {
	case "published_match", "pending_publication", "process_deferred":
	default:
		return unconfirmed
	}
	patch := make(map[string]any)
	for field, desired := range settings {
		value, ok := readConfigField(current, field)
		if !ok {
			return configDelivery{status: "conflict", code: "config_unsupported"}
		}
		if !bytes.Equal(value, desired) || containsConfigPath(publication.Comparison.PendingPaths, field) {
			addClusterPatchField(patch, field, desired)
		}
	}
	if len(patch) == 0 {
		return configDelivery{status: "applied"}
	}
	if publication.Reload != nil {
		switch publication.Reload.Phase {
		case telemt.ReloadPhaseAccepted, telemt.ReloadPhasePreparing, telemt.ReloadPhaseActivating, telemt.ReloadPhaseDraining:
			return configDelivery{status: "pending", code: "publishing"}
		}
	}

	validation, err := client.ValidateClusterConfig(ctx, patch, revision)
	if err != nil {
		return clusterConfigFailure(err, false)
	}
	if !validation.Valid || validation.BaseRevision != revision || validation.Runtime.Match == "" {
		return unconfirmed
	}
	if validation.Apply.ProcessRestartRequired {
		return configDelivery{status: "conflict", code: "restart_required"}
	}
	for _, paths := range [][]string{validation.ChangedPaths, validation.Apply.WatcherPaths, validation.Apply.GenerationPaths} {
		for _, path := range paths {
			// Telemt may derive a fresh in-memory value while preparing
			// a candidate, e.g. an omitted censorship.fake_cert_len.
			if !clusterOwnsPath(settings, path) && !slices.Contains(publication.Comparison.RuntimeDerivedPaths, path) {
				return configDelivery{status: "conflict", code: "local_pending"}
			}
		}
	}
	if err := beforeWrite(); err != nil {
		return configDelivery{status: "unavailable", code: "revision_changed"}
	}
	result, err := client.PublishClusterConfig(ctx, patch, revision, validation.Runtime.Match)
	if err != nil {
		return clusterConfigFailure(err, true)
	}
	if result.ProcessRestartRequired {
		return configDelivery{status: "conflict", code: "restart_required"}
	}
	// A completed HTTP write still needs a fresh desired/runtime readback.
	// Retain the lease briefly after an asynchronous reply if the node is
	// being detached before that readback.
	return configDelivery{status: "pending", code: "publishing"}
}

func addClusterPatchField(patch map[string]any, path string, value any) {
	segments := strings.Split(path, ".")
	for _, part := range segments[:len(segments)-1] {
		child, ok := patch[part].(map[string]any)
		if !ok {
			child = make(map[string]any)
			patch[part] = child
		}
		patch = child
	}
	patch[segments[len(segments)-1]] = value
}

func configPathWithin(path, field string) bool {
	return path == field || strings.HasPrefix(path, field+".") || strings.HasPrefix(path, field+"[")
}

func containsConfigPath(paths []string, field string) bool {
	for _, path := range paths {
		if configPathWithin(path, field) || configPathWithin(field, path) {
			return true
		}
	}
	return false
}

func clusterOwnsPath(settings ClusterConfigValues, path string) bool {
	for field := range settings {
		if configPathWithin(path, field) {
			return true
		}
	}
	return false
}

func clusterConfigFailure(err error, writing bool) configDelivery {
	var api *telemt.APIError
	if errors.As(err, &api) && api.Status >= 400 && api.Status < 500 {
		switch {
		case api.Status == http.StatusNotFound || api.Status == http.StatusMethodNotAllowed:
			return configDelivery{status: "conflict", code: "config_unsupported"}
		case api.Code == "revision_conflict" || api.Code == "runtime_conflict":
			return configDelivery{status: "unavailable", code: "revision_changed"}
		case api.Code == "reload_in_progress":
			return configDelivery{status: "pending", code: "publishing"}
		case api.Status == http.StatusTooManyRequests:
			return configDelivery{status: "unavailable", code: "not_confirmed"}
		default:
			return configDelivery{status: "conflict", code: "config_rejected"}
		}
	}
	return configDelivery{status: "unavailable", code: "not_confirmed", uncertain: writing}
}
