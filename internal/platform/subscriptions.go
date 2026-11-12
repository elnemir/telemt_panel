package platform

import (
	"context"
	"database/sql"
	"errors"
)

const subscriptionColumns = "id,customer_id,cluster_id,COALESCE(identity_id,''),enabled,created_at"

func scanSubscription(row scanner) (Subscription, error) {
	var s Subscription
	e := row.Scan(&s.ID, &s.CustomerID, &s.ClusterID, &s.IdentityID, &s.Enabled, &s.CreatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	return s, e
}
func (s *Store) CustomerSubscription(ctx context.Context, customerID, id string) (Subscription, error) {
	return scanSubscription(s.db.QueryRowContext(ctx, "SELECT "+subscriptionColumns+" FROM subscriptions WHERE id=? AND customer_id=?", id, customerID))
}
func (s *Store) CustomerSubscriptions(ctx context.Context, customerID string, offset int) ([]Subscription, error) {
	if offset < 0 {
		return nil, ErrInvalid
	}
	rows, e := s.db.QueryContext(ctx, "SELECT "+subscriptionColumns+" FROM subscriptions WHERE customer_id=? ORDER BY created_at DESC,id DESC LIMIT 100 OFFSET ?", customerID, offset)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Subscription{}
	for rows.Next() {
		v, e := scanSubscription(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) CustomerPaidThrough(ctx context.Context, customerID, subscriptionID string) (*int64, error) {
	var end *int64
	e := s.db.QueryRowContext(ctx, "SELECT MAX(ends_at) FROM paid_periods WHERE subscription_id IN (SELECT id FROM subscriptions WHERE id=? AND customer_id=?)", subscriptionID, customerID).Scan(&end)
	return end, e
}
