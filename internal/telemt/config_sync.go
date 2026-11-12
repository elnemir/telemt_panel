package telemt

import (
	"context"
	"encoding/json"
	"net/http"
)

type ConfigPublication struct {
	DesiredRevision      string `json:"desired_revision"`
	DesiredStatus        string `json:"desired_status"`
	RuntimeSampleCurrent bool   `json:"runtime_sample_current"`
	Comparison           struct {
		Status              string   `json:"status"`
		PendingPaths        []string `json:"pending_paths"`
		RuntimeDerivedPaths []string `json:"runtime_derived_paths"`
		Truncated           bool     `json:"truncated"`
	} `json:"comparison"`
	Reload *struct {
		Phase string `json:"phase"`
	} `json:"reload"`
}

type ConfigValidation struct {
	Valid        bool     `json:"valid"`
	BaseRevision string   `json:"base_revision"`
	ChangedPaths []string `json:"changed_paths"`
	Runtime      struct {
		Match string `json:"runtime_match"`
	} `json:"runtime"`
	Apply struct {
		WatcherPaths           []string `json:"watcher_paths"`
		GenerationPaths        []string `json:"generation_paths"`
		ProcessRestartRequired bool     `json:"process_restart_required"`
	} `json:"apply"`
}

func (c *Client) ClusterConfigPublication(ctx context.Context) (ConfigPublication, error) {
	return get[ConfigPublication](ctx, c, "/v1/config/state?sections=general,censorship,timeouts")
}

func (c *Client) ValidateClusterConfig(ctx context.Context, patch map[string]any, revision string) (ConfigValidation, error) {
	out, _, err := mutateRevision[ConfigValidation](ctx, c, http.MethodPost, "/v1/config/validate", patch, revision)
	return out, err
}

func (c *Client) PublishClusterConfig(ctx context.Context, patch map[string]any, revision, runtimeMatch string) (PatchConfigResult, error) {
	const path = "/v1/config?reload=instant&failure_policy=rollback"
	raw, status, err := c.requestConfigRevision(ctx, http.MethodPatch, path, patch, revision, runtimeMatch)
	var out PatchConfigResult
	if err != nil {
		return out, err
	}
	data, _, _, err := decodeEnvelope(raw, status)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(data, &out)
	return out, err
}
