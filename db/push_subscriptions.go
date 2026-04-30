package db

import (
	"context"
	"time"
)

// PushSubscription represents a stored web push subscription.
type PushSubscription struct {
	ID        string
	Endpoint  string
	P256DH    string
	Auth      string
	UserAgent string
	CreatedAt time.Time
}

// CreatePushSubscription stores a new push subscription, replacing any existing
// subscription for the same endpoint.
func (db *DB) CreatePushSubscription(ctx context.Context, id, endpoint, p256dh, auth, userAgent string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec(
			`INSERT INTO push_subscriptions (id, endpoint, p256dh, auth, user_agent)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(endpoint) DO UPDATE SET
			   id = excluded.id,
			   p256dh = excluded.p256dh,
			   auth = excluded.auth,
			   user_agent = excluded.user_agent,
			   created_at = CURRENT_TIMESTAMP`,
			id, endpoint, p256dh, auth, userAgent,
		)
		return err
	})
}

// ListPushSubscriptions returns all stored push subscriptions.
func (db *DB) ListPushSubscriptions(ctx context.Context) ([]PushSubscription, error) {
	var subs []PushSubscription
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		rows, err := rx.Query(
			`SELECT id, endpoint, p256dh, auth, user_agent, created_at FROM push_subscriptions ORDER BY created_at`,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s PushSubscription
			if err := rows.Scan(&s.ID, &s.Endpoint, &s.P256DH, &s.Auth, &s.UserAgent, &s.CreatedAt); err != nil {
				return err
			}
			subs = append(subs, s)
		}
		return rows.Err()
	})
	return subs, err
}

// DeletePushSubscription removes a push subscription by ID.
func (db *DB) DeletePushSubscription(ctx context.Context, id string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec(`DELETE FROM push_subscriptions WHERE id = ?`, id)
		return err
	})
}

// DeletePushSubscriptionByEndpoint removes a push subscription by endpoint URL.
func (db *DB) DeletePushSubscriptionByEndpoint(ctx context.Context, endpoint string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec(`DELETE FROM push_subscriptions WHERE endpoint = ?`, endpoint)
		return err
	})
}
