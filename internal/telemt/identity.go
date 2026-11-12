package telemt

import "context"

// UserPolicySnapshot retains configured overrides separately from inherited
// effective limits. Cluster reconciliation must not adopt a node's defaults.
// This internal integration DTO does not change the existing panel user API.
type UserPolicySnapshot struct {
	ClusterQuota *bool `json:"cluster_quota"`
	UserInfo
	Limits *UserLimitState `json:"limits"`
}
type InheritedUserLimit struct {
	Value              *uint64 `json:"value"`
	ConfiguredOverride *uint64 `json:"configured_override"`
	GlobalValue        uint64  `json:"global_value"`
	Origin             string  `json:"origin"`
}
type UserLimitSnapshot struct {
	MaxTCPConns  InheritedUserLimit `json:"max_tcp_conns"`
	MaxUniqueIPs InheritedUserLimit `json:"max_unique_ips"`
}
type UserLimitState struct {
	Desired              UserLimitSnapshot  `json:"desired"`
	Effective            *UserLimitSnapshot `json:"effective"`
	EffectiveScope       string             `json:"effective_scope"`
	SubsystemConvergence string             `json:"subsystem_convergence"`
}

func (c *Client) UserPolicySnapshotsWithRevision(ctx context.Context) ([]UserPolicySnapshot, string, error) {
	return getRevision[[]UserPolicySnapshot](ctx, c, "/v1/users")
}
