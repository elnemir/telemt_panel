package platform

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/amirotin/telemt_panel/internal/telemt"
)

// Cluster-managed fields are individually approved. In particular a section
// name never grants ownership of arbitrary keys added by future builds.
var clusterConfigFields = []string{
	"general.modes.classic", "general.modes.secure", "general.modes.tls",
	"censorship.tls_domain", "censorship.tls_domains",
	"censorship.mask", "censorship.tls_emulation",
	"timeouts.client_handshake", "timeouts.client_keepalive", "timeouts.client_ack",
	"general.telemetry.core_enabled", "general.telemetry.user_enabled", "general.telemetry.me_level",
}

type ClusterConfigValues map[string]json.RawMessage

type ClusterConfigTemplate struct {
	SourceNodeID string              `json:"source_node_id"`
	Revision     int64               `json:"revision"`
	Settings     ClusterConfigValues `json:"settings"`
}

type ClusterConfigJob struct {
	NodeID          string `json:"node_id"`
	DesiredRevision int64  `json:"desired_revision"`
	AppliedRevision int64  `json:"applied_revision"`
	Status          string `json:"status"`
	LastCode        string `json:"last_code"`
}

type ClusterConfig struct {
	Revision int64                  `json:"revision"`
	Fields   []string               `json:"fields"`
	Template *ClusterConfigTemplate `json:"template"`
	Jobs     []ClusterConfigJob     `json:"jobs"`
}

func validClusterConfigFields(fields []string) bool {
	if len(fields) == 0 || len(fields) > len(clusterConfigFields) {
		return false
	}
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		if seen[field] || !slices.Contains(clusterConfigFields, field) {
			return false
		}
		seen[field] = true
	}
	return true
}

func readConfigField(sections telemt.ConfigSections, path string) (json.RawMessage, bool) {
	section, rest, _ := strings.Cut(path, ".")
	value := sections[section]
	for _, part := range strings.Split(rest, ".") {
		var entries map[string]json.RawMessage
		if json.Unmarshal(value, &entries) != nil {
			return nil, false
		}
		value = entries[part]
	}
	var compact bytes.Buffer
	if len(value) == 0 || bytes.Equal(value, []byte("null")) || json.Compact(&compact, value) != nil {
		return nil, false
	}
	return compact.Bytes(), true
}

func ReadClusterConfigValues(sections telemt.ConfigSections, fields []string) (ClusterConfigValues, error) {
	if !validClusterConfigFields(fields) {
		return nil, ErrInvalid
	}
	values := make(ClusterConfigValues, len(fields))
	size := 0
	for _, field := range fields {
		value, ok := readConfigField(sections, field)
		if !ok || !validClusterConfigValue(field, value) {
			return nil, ErrConflict
		}
		size += len(value)
		if size > 16<<10 {
			return nil, ErrInvalid
		}
		values[field] = value
	}
	return values, nil
}

func validClusterConfigValue(field string, value json.RawMessage) bool {
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return false
	}
	switch {
	case field == "censorship.tls_domains":
		var domains []string
		return json.Unmarshal(value, &domains) == nil
	case field == "censorship.tls_domain" || field == "general.telemetry.me_level":
		var text string
		return json.Unmarshal(value, &text) == nil
	case strings.HasPrefix(field, "timeouts."):
		var seconds uint64
		return json.Unmarshal(value, &seconds) == nil
	default:
		var enabled bool
		return json.Unmarshal(value, &enabled) == nil
	}
}

func (s *Store) ClusterConfig(ctx context.Context, clusterID string) (ClusterConfig, error) {
	out := ClusterConfig{Fields: slices.Clone(clusterConfigFields), Jobs: []ClusterConfigJob{}}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		cluster, err := scanCluster(tx.QueryRowContext(ctx, "SELECT "+clusterColumns+" FROM clusters WHERE id=?", clusterID))
		if err != nil {
			return err
		}
		out.Revision = cluster.Revision
		var template ClusterConfigTemplate
		var raw string
		err = tx.QueryRowContext(ctx, "SELECT source_node_id,revision,settings FROM cluster_config_templates WHERE cluster_id=?", clusterID).
			Scan(&template.SourceNodeID, &template.Revision, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(raw), &template.Settings); err != nil {
			return err
		}
		out.Template = &template
		rows, err := tx.QueryContext(ctx, `SELECT j.node_id,j.desired_revision,j.applied_revision,j.status,j.last_code
			FROM cluster_config_jobs j JOIN cluster_members m ON m.node_id=j.node_id WHERE m.cluster_id=? AND m.state='active' ORDER BY j.node_id`, clusterID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var job ClusterConfigJob
			if err := rows.Scan(&job.NodeID, &job.DesiredRevision, &job.AppliedRevision, &job.Status, &job.LastCode); err != nil {
				return err
			}
			out.Jobs = append(out.Jobs, job)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) PutClusterConfig(ctx context.Context, clusterID string, revision int64, source string, values ClusterConfigValues) error {
	fields := make([]string, 0, len(values))
	for field, value := range values {
		if !validClusterConfigValue(field, value) {
			return ErrInvalid
		}
		fields = append(fields, field)
	}
	if !validClusterConfigFields(fields) {
		return ErrInvalid
	}
	encoded, err := json.Marshal(values)
	if err != nil || len(encoded) > 18<<10 {
		return ErrInvalid
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := checkCluster(ctx, tx, clusterID, revision); err != nil {
			return err
		}
		var active bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM cluster_members WHERE cluster_id=? AND node_id=? AND state='active')`, clusterID, source).Scan(&active); err != nil {
			return err
		}
		if !active {
			return ErrConflict
		}
		next, now := revision+1, s.now().Unix()
		if _, err := tx.ExecContext(ctx, `INSERT INTO cluster_config_templates(cluster_id,source_node_id,revision,settings) VALUES(?,?,?,?)
			ON CONFLICT(cluster_id) DO UPDATE SET source_node_id=excluded.source_node_id,revision=excluded.revision,settings=excluded.settings`, clusterID, source, next, string(encoded)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO cluster_config_jobs(node_id,desired_revision,next_attempt)
			SELECT node_id,?,? FROM cluster_members WHERE cluster_id=? AND state='active'
			ON CONFLICT(node_id) DO UPDATE SET desired_revision=excluded.desired_revision,next_attempt=excluded.next_attempt,status='pending',last_code=''`, next, now, clusterID); err != nil {
			return err
		}
		if err := bumpCluster(ctx, tx, clusterID, now); err != nil {
			return err
		}
		return auditTx(ctx, tx, now, "cluster.config.save", clusterID, next)
	})
}

func (s *Store) DeleteClusterConfig(ctx context.Context, clusterID string, revision int64) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := checkCluster(ctx, tx, clusterID, revision); err != nil {
			return err
		}
		var inFlight bool
		now := s.now().Unix()
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM cluster_config_jobs j JOIN cluster_members m ON m.node_id=j.node_id WHERE m.cluster_id=? AND j.lease_until>?)`, clusterID, now).Scan(&inFlight); err != nil {
			return err
		}
		if inFlight {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM cluster_config_jobs WHERE node_id IN (SELECT node_id FROM cluster_members WHERE cluster_id=?)`, clusterID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM cluster_config_templates WHERE cluster_id=?", clusterID); err != nil {
			return err
		}
		if err := bumpCluster(ctx, tx, clusterID, now); err != nil {
			return err
		}
		return auditTx(ctx, tx, now, "cluster.config.stop", clusterID, revision+1)
	})
}

type clusterConfigWork struct {
	ClusterConfigJob
	settings ClusterConfigValues
	lease    string
}

const clusterConfigLease = 2 * time.Minute

func (s *Store) claimClusterConfig(ctx context.Context) (clusterConfigWork, error) {
	var work clusterConfigWork
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		now := s.now()
		var raw string
		err := tx.QueryRowContext(ctx, `SELECT j.node_id,j.desired_revision,t.settings
			FROM cluster_config_jobs j JOIN cluster_members m ON m.node_id=j.node_id
			JOIN cluster_config_templates t ON t.cluster_id=m.cluster_id AND t.revision=j.desired_revision
			WHERE m.state='active' AND j.next_attempt<=? AND j.lease_until<=?
			ORDER BY j.next_attempt,j.node_id LIMIT 1`, now.Unix(), now.Unix()).Scan(&work.NodeID, &work.DesiredRevision, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(raw), &work.settings); err != nil {
			return fmt.Errorf("decode cluster template: %w", err)
		}
		work.lease = newID("l_")
		_, err = tx.ExecContext(ctx, `UPDATE cluster_config_jobs SET lease_until=?,lease_token=? WHERE node_id=?`, now.Add(clusterConfigLease).Unix(), work.lease, work.NodeID)
		return err
	})
	return work, err
}

func (s *Store) finishClusterConfig(ctx context.Context, work clusterConfigWork, result configDelivery) error {
	delay := time.Minute
	if result.status == "applied" {
		delay = 5 * time.Minute
	}
	now := s.now()
	leaseToken := ""
	leaseUntil := int64(0)
	if result.status == "pending" {
		delay = 8 * time.Second
		leaseToken = work.lease
		leaseUntil = now.Add(delay).Unix()
	}
	if result.uncertain {
		leaseToken = work.lease
		leaseUntil = now.Add(clusterConfigLease).Unix()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE cluster_config_jobs SET
		status=CASE WHEN desired_revision=? THEN ? ELSE status END,
		applied_revision=CASE WHEN desired_revision=? AND ?='applied' THEN desired_revision ELSE applied_revision END,
		last_code=CASE WHEN desired_revision=? THEN ? ELSE last_code END,
		next_attempt=CASE WHEN desired_revision=? THEN ? ELSE next_attempt END,
		lease_until=?,lease_token=?
		WHERE node_id=? AND lease_token=?`,
		work.DesiredRevision, result.status, work.DesiredRevision, result.status, work.DesiredRevision, result.code,
		work.DesiredRevision, now.Add(delay).Unix(), leaseUntil, leaseToken, work.NodeID, work.lease)
	return err
}

func (s *Store) checkClusterConfigWork(ctx context.Context, work clusterConfigWork) error {
	var active bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1
		FROM cluster_config_jobs j JOIN cluster_members m ON m.node_id=j.node_id
		WHERE j.node_id=? AND j.desired_revision=? AND j.lease_token=? AND j.lease_until>? AND m.state='active')`,
		work.NodeID, work.DesiredRevision, work.lease, s.now().Unix()).Scan(&active)
	if err == nil && !active {
		return ErrConflict
	}
	return err
}
