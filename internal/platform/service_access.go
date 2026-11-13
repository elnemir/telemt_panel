package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"

	"github.com/amirotin/telemt_panel/internal/subpage"
	"github.com/amirotin/telemt_panel/internal/telemt"
)

type ConnectionLink struct {
	Mode string `json:"mode"`
	URL  string `json:"url"`
}
type ServiceNode struct {
	NodeID      string           `json:"node_id"`
	Status      string           `json:"status"`
	CheckedAt   int64            `json:"checked_at"`
	BudgetUntil int64            `json:"budget_until"`
	Links       []ConnectionLink `json:"links"`
}
type ServiceAccess struct {
	Status string           `json:"status"`
	Nodes  []ServiceNode    `json:"nodes"`
	Links  []ConnectionLink `json:"links"`
	Usage  *QuotaUsage      `json:"usage"`
}

// A node cannot inject arbitrary clickable schemes or another identity's link
// into a customer account. WEB links need the separate ingress contract.
func connectionLinks(links telemt.UserLinks, secret string) []ConnectionLink {
	return verifiedConnectionLinks(links, secretHash(secret))
}

func verifiedConnectionLinks(links telemt.UserLinks, expectedHash string) []ConnectionLink {
	out := []ConnectionLink{}
	seen := map[string]bool{}
	total := 0
	add := func(mode, raw string) {
		if len(out) >= 32 || len(raw) > 2048 || total+len(raw) > 16384 || seen[raw] {
			return
		}
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "tg" || u.Host != "proxy" || u.User != nil || u.Fragment != "" || u.Path != "" {
			return
		}
		q, e := url.ParseQuery(u.RawQuery)
		if e != nil || len(q) != 3 || len(q["server"]) != 1 || len(q["port"]) != 1 || len(q["secret"]) != 1 || q.Get("server") == "" {
			return
		}
		port, e := strconv.Atoi(q.Get("port"))
		if e != nil || port < 1 || port > 65535 {
			return
		}
		var check telemt.UserLinks
		switch mode {
		case "classic":
			check.Classic = []string{raw}
		case "secure":
			check.Secure = []string{raw}
		case "faketls":
			check.TLS = []string{raw}
		}
		found, ok := subpage.ExtractSecret(check)
		if !ok || secretHash(found) != expectedHash {
			return
		}
		seen[raw] = true
		total += len(raw)
		out = append(out, ConnectionLink{mode, raw})
	}
	for _, raw := range links.TLS {
		add("faketls", raw)
	}
	for _, d := range links.TLSDomains {
		add("faketls", d.Link)
	}
	for _, raw := range links.Secure {
		add("secure", raw)
	}
	for _, raw := range links.Classic {
		add("classic", raw)
	}
	return out
}

func (s *Store) CustomerServiceAccess(ctx context.Context, customerID, subscriptionID string) (ServiceAccess, error) {
	out := ServiceAccess{Status: "not_provisioned", Nodes: []ServiceNode{}, Links: []ConnectionLink{}}
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		sub, e := scanSubscription(tx.QueryRowContext(ctx, "SELECT "+subscriptionColumns+" FROM subscriptions WHERE id=? AND customer_id=?", subscriptionID, customerID))
		if e != nil {
			return e
		}
		var customerEnabled bool
		if e = tx.QueryRowContext(ctx, "SELECT enabled FROM customers WHERE id=?", customerID).Scan(&customerEnabled); e != nil {
			return e
		}
		if !sub.Enabled || !customerEnabled {
			out.Status = "disabled"
			return nil
		}
		now := s.now().Unix()
		period, e := paidPeriodTx(ctx, tx, sub.ID, now)
		if errors.Is(e, ErrNotFound) {
			out.Status = "unpaid"
			return nil
		}
		if e != nil {
			return e
		}
		if sub.IdentityID == "" {
			return nil
		}
		identity, e := scanIdentity(tx.QueryRowContext(ctx, "SELECT "+identityColumns+" FROM identities WHERE id=?", sub.IdentityID))
		if e != nil {
			return e
		}
		if identity.Revoked || !identity.Policy.Enabled {
			out.Status = "disabled"
			return nil
		}
		usage := QuotaUsage{PeriodID: period.ID, QuotaBytes: period.Terms.QuotaBytes, OverrunBytes: period.Terms.OverrunBytes}
		e = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(granted_bytes),0),COALESCE(SUM(observed_bytes),0),COUNT(*),MAX(reported_at) FROM quota_allocations WHERE period_id=?", period.ID).Scan(&usage.GrantedBytes, &usage.ObservedBytes, &usage.Allocations, &usage.LastReportedAt)
		if e != nil {
			return e
		}
		usage.UnknownBytes = usage.GrantedBytes - usage.ObservedBytes
		out.Usage = &usage
		endpoint, e := appliedEndpoint(ctx, tx, identity.ClusterID)
		if e != nil {
			return e
		}
		var candidates []ConnectionLink
		rows, e := tx.QueryContext(ctx, `SELECT j.node_id,j.status,j.last_code,j.updated_at,j.budget_until,j.links_cipher,j.budget_period_id,j.applied_revision,j.desired_revision,j.in_runtime
   FROM identity_jobs j JOIN cluster_members m ON m.node_id=j.node_id WHERE j.identity_id=? AND m.state='active' ORDER BY j.node_id`, sub.IdentityID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			node := ServiceNode{Links: []ConnectionLink{}}
			var state, code, budgetPeriod string
			var sealed []byte
			var applied, desired int64
			var inRuntime bool
			if e = rows.Scan(&node.NodeID, &state, &code, &node.CheckedAt, &node.BudgetUntil, &sealed, &budgetPeriod, &applied, &desired, &inRuntime); e != nil {
				return e
			}
			node.Status = "pending"
			switch {
			case state == "conflict":
				node.Status = "conflict"
			case state == "unavailable":
				node.Status = "unavailable"
			case code == "managed_quota_empty":
				node.Status = "quota_empty"
			case state == "applied" && applied == desired && inRuntime && budgetPeriod == period.ID && node.BudgetUntil > now && code == "managed_quota_active":
				node.Status = "configured"
				out.Status = "configured"
				if endpoint.host != "" && len(sealed) > 0 {
					raw, e := s.decrypt("links:"+identity.ID+":"+node.NodeID, sealed)
					if e != nil {
						return e
					}
					var links []ConnectionLink
					if e = json.Unmarshal([]byte(raw), &links); e != nil {
						return ErrSchema
					}
					candidates = append(candidates, links...)
				}
			case state == "applied" && node.BudgetUntil <= now:
				node.Status = "unavailable"
			}
			out.Nodes = append(out.Nodes, node)
		}
		out.Links = ingressConnectionLinks(candidates, endpoint)
		return rows.Err()
	})
	return out, e
}
