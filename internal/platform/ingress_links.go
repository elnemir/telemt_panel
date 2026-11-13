package platform

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/amirotin/telemt_panel/internal/telemt"
	"github.com/amirotin/telemt_panel/internal/userprojection"
)

type ingressEndpoint struct {
	host string
	port int
}

// RecordAppliedIngress publishes an endpoint only after the remote worker
// acknowledges activation. A late acknowledgement cannot overwrite a newer one.
func (s *Store) RecordAppliedIngress(ctx context.Context, clusterID string, revision int64, host string, port int) error {
	addr, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil || addr.Zone() != "" || !addr.IsGlobalUnicast() || revision < 1 || port < 1 || port > 65535 {
		return ErrInvalid
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO cluster_ingress_applied(cluster_id,revision,host,native_port)
		VALUES(?,?,?,?) ON CONFLICT(cluster_id) DO UPDATE SET
		revision=excluded.revision,host=excluded.host,native_port=excluded.native_port
		WHERE excluded.revision >= cluster_ingress_applied.revision`, clusterID, revision, addr.Unmap().String(), port)
	return err
}

type UserIngress struct {
	endpoint ingressEndpoint
	hash     string
	enabled  bool
}

// NodeUserIngress reads all cluster-owned usernames in one statement per node.
func (s *Store) NodeUserIngress(ctx context.Context, nodeID string) (map[string]UserIngress, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT i.username,i.secret_hash,
		NOT i.revoked AND COALESCE(json_extract(i.policy,'$.enabled'),0) AND m.state='active',
		COALESCE(a.host,''),COALESCE(a.native_port,0)
		FROM cluster_members m JOIN identities i ON i.cluster_id=m.cluster_id
		LEFT JOIN cluster_ingress_applied a ON a.cluster_id=m.cluster_id WHERE m.node_id=?`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]UserIngress)
	for rows.Next() {
		var name string
		var entry UserIngress
		if err := rows.Scan(&name, &entry.hash, &entry.enabled, &entry.endpoint.host, &entry.endpoint.port); err != nil {
			return nil, err
		}
		out[name] = entry
	}
	return out, rows.Err()
}

func replaceIngressAddress(raw string, endpoint ingressEndpoint) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("server", endpoint.host)
	q.Set("port", strconv.Itoa(endpoint.port))
	u.RawQuery = q.Encode()
	return u.String()
}

func (r UserIngress) Links(links telemt.UserLinks) telemt.UserLinks {
	out := telemt.UserLinks{Classic: []string{}, Secure: []string{}, TLS: []string{}, TLSDomains: []telemt.TLSDomainLink{}}
	if !r.enabled || r.endpoint.host == "" {
		return out
	}
	rewrite := func(input telemt.UserLinks) []string {
		result := []string{}
		for _, link := range ingressConnectionLinks(verifiedConnectionLinks(input, r.hash), r.endpoint) {
			result = append(result, link.URL)
		}
		return result
	}
	out.Classic = rewrite(telemt.UserLinks{Classic: links.Classic})
	out.Secure = rewrite(telemt.UserLinks{Secure: links.Secure})
	out.TLS = rewrite(telemt.UserLinks{TLS: links.TLS})
	seen := make(map[string]bool)
	for _, domain := range links.TLSDomains {
		if len(out.TLSDomains) >= 32 {
			break
		}
		if rewritten := rewrite(telemt.UserLinks{TLS: []string{domain.Link}}); len(rewritten) == 1 && !seen[rewritten[0]] {
			seen[rewritten[0]] = true
			out.TLSDomains = append(out.TLSDomains, telemt.TLSDomainLink{Domain: domain.Domain, Link: rewritten[0]})
		}
	}
	return out
}

// IdentityIngressLinkNodes returns candidates whose current identity revision
// is running. The caller reads the actual links from a candidate node and
// checks the secret again against this identity's hash before publication.
func (s *Store) IdentityIngressLinkNodes(ctx context.Context, identityID string, revision int64) (UserIngress, []string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT j.node_id,i.secret_hash,a.host,a.native_port
		FROM identities i JOIN cluster_ingress_applied a ON a.cluster_id=i.cluster_id
		JOIN identity_jobs j ON j.identity_id=i.id JOIN cluster_members m ON m.node_id=j.node_id AND m.cluster_id=i.cluster_id
		WHERE i.id=? AND i.revision=? AND NOT i.revoked AND json_extract(i.policy,'$.enabled')=1
		AND m.state='active' AND j.status='applied' AND j.in_runtime AND j.applied_revision=i.revision ORDER BY j.node_id`, identityID, revision)
	if err != nil {
		return UserIngress{}, nil, err
	}
	defer rows.Close()
	var route UserIngress
	var nodes []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id, &route.hash, &route.endpoint.host, &route.endpoint.port); err != nil {
			return UserIngress{}, nil, err
		}
		route.enabled = true
		nodes = append(nodes, id)
	}
	return route, nodes, rows.Err()
}

func (r UserIngress) ConnectionLinks(links telemt.UserLinks) []ConnectionLink {
	if !r.enabled {
		return nil
	}
	return ingressConnectionLinks(verifiedConnectionLinks(links, r.hash), r.endpoint)
}

func ingressConnectionLinks(links []ConnectionLink, endpoint ingressEndpoint) []ConnectionLink {
	out := []ConnectionLink{}
	if endpoint.host == "" {
		return out
	}
	seen := make(map[string]bool, len(links))
	for _, link := range links {
		if len(out) >= 32 {
			break
		}
		link.URL = replaceIngressAddress(link.URL, endpoint)
		if link.URL != "" && !seen[link.URL] {
			seen[link.URL] = true
			out = append(out, link)
		}
	}
	return out
}

func appliedEndpoint(ctx context.Context, tx *sql.Tx, clusterID string) (ingressEndpoint, error) {
	var endpoint ingressEndpoint
	err := tx.QueryRowContext(ctx, "SELECT host,native_port FROM cluster_ingress_applied WHERE cluster_id=?", clusterID).Scan(&endpoint.host, &endpoint.port)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return endpoint, err
}

func ProjectUserLinks(user *userprojection.User, routes map[string]UserIngress) {
	if route, managed := routes[user.Username]; managed {
		user.ClusterManaged = true
		user.Links = route.Links(user.Links)
	}
}

func (s *Store) ProjectNodeUserLinks(ctx context.Context, nodeID string, users []userprojection.User) error {
	routes, err := s.NodeUserIngress(ctx, nodeID)
	if err != nil {
		return err
	}
	for i := range users {
		ProjectUserLinks(&users[i], routes)
	}
	return nil
}
