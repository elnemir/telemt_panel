package telemt

import (
	"context"
	"net/http"
	"net/url"
)

// ClusterQuotaGrant uses Telemt's integer wire format. Customer-facing platform
// models encode money/bytes as decimal strings separately.
type ClusterQuotaGrant struct {
	Refillable     bool   `json:"refillable"`
	BootID         string `json:"boot_id"`
	PeriodID       string `json:"period_id"`
	Sequence       uint64 `json:"sequence"`
	GrantedBytes   uint64 `json:"granted_bytes"`
	PeriodStartsAt int64  `json:"period_starts_at"`
	PeriodEndsAt   int64  `json:"period_ends_at"`
	ValidUntil     int64  `json:"valid_until"`
}
type ClusterQuotaStatus struct {
	Username       string             `json:"username"`
	SecretHash     string             `json:"secret_hash"`
	Enabled        bool               `json:"enabled"`
	BootID         string             `json:"boot_id"`
	Required       bool               `json:"required"`
	Grant          *ClusterQuotaGrant `json:"grant"`
	ChargedBytes   uint64             `json:"charged_bytes"`
	AvailableBytes uint64             `json:"available_bytes"`
}

func (c *Client) ClusterQuotaWithRevision(ctx context.Context, username string) (ClusterQuotaStatus, string, error) {
	return getRevision[ClusterQuotaStatus](ctx, c, "/v1/users/"+url.PathEscape(username)+"/cluster-quota")
}
func (c *Client) ApplyClusterQuota(ctx context.Context, username, secretHash, revision string, grant ClusterQuotaGrant) (ClusterQuotaStatus, string, error) {
	body := struct {
		SecretHash string            `json:"secret_hash"`
		Grant      ClusterQuotaGrant `json:"grant"`
	}{secretHash, grant}
	return mutateRevision[ClusterQuotaStatus](ctx, c, http.MethodPost, "/v1/users/"+url.PathEscape(username)+"/cluster-quota", body, revision)
}

// Managed enrollment is an orchestration operation, not an extra field accepted
// by the existing node-local customer-creation handler.
func (c *Client) CreateManagedUserWithRevision(ctx context.Context, req CreateUserRequest, revision string) (UserInfo, error) {
	body := struct {
		CreateUserRequest
		ClusterQuota bool `json:"cluster_quota"`
	}{req, true}
	out, _, err := mutateRevision[userSecret](ctx, c, http.MethodPost, "/v1/users", body, revision)
	return out.User, err
}
