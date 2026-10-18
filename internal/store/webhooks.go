package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Webhook is a registered event endpoint (spec 5.3). InstanceID "" means every instance.
type Webhook struct {
	ID         string
	InstanceID string
	URL        string
	Secret     string
	Events     []string
	Version    int
	Enabled    bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

const webhookCols = `id, COALESCE(instance_id, ''), url, secret, events_json, version, enabled, created_at, updated_at`

func scanWebhook(row scanner) (Webhook, error) {
	var w Webhook
	var evs, created, updated string
	err := row.Scan(&w.ID, &w.InstanceID, &w.URL, &w.Secret, &evs, &w.Version, &w.Enabled, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrNotFound
	}
	if err != nil {
		return w, err
	}
	_ = json.Unmarshal([]byte(evs), &w.Events)
	w.CreatedAt, w.UpdatedAt = parseTS(created), parseTS(updated)
	return w, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Store) CreateWebhook(ctx context.Context, w Webhook) error {
	evs, _ := json.Marshal(w.Events)
	ts := now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO webhooks (id, instance_id, url, secret, events_json, version, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, w.ID, nullable(w.InstanceID), w.URL, w.Secret, string(evs), w.Version, w.Enabled, ts, ts)
	return err
}

func (s *Store) GetWebhook(ctx context.Context, id string) (Webhook, error) {
	return scanWebhook(s.db.QueryRowContext(ctx, `SELECT `+webhookCols+` FROM webhooks WHERE id = ?`, id))
}

// ListWebhooks returns every webhook in creation order.
func (s *Store) ListWebhooks(ctx context.Context) ([]Webhook, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+webhookCols+` FROM webhooks ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// UpdateWebhook saves url, secret, events and enabled.
func (s *Store) UpdateWebhook(ctx context.Context, w Webhook) error {
	evs, _ := json.Marshal(w.Events)
	return s.exec1(ctx, `UPDATE webhooks SET url = ?, secret = ?, events_json = ?, enabled = ?, updated_at = ? WHERE id = ?`,
		w.URL, w.Secret, string(evs), w.Enabled, now(), w.ID)
}

// DeleteWebhook removes a webhook and its deliveries.
func (s *Store) DeleteWebhook(ctx context.Context, id string) error {
	return s.exec1(ctx, `DELETE FROM webhooks WHERE id = ?`, id)
}

// Delivery statuses.
const (
	DeliveryPending   = "pending"
	DeliveryDelivered = "delivered"
	DeliveryFailed    = "failed"
)

// Delivery is one event to deliver to one webhook.
type Delivery struct {
	ID             string
	WebhookID      string
	EventID        string
	EventType      string
	InstanceID     string
	Payload        []byte
	Status         string
	Attempts       int
	NextAttemptAt  time.Time
	ExpiresAt      time.Time
	LastStatusCode int
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeliveredAt    time.Time
}

const deliveryCols = `id, webhook_id, event_id, event_type, instance_id, payload, status, attempts, next_attempt_at,
	expires_at, last_status_code, last_error, created_at, updated_at, delivered_at`

func scanDelivery(row scanner) (Delivery, error) {
	var d Delivery
	var next, exp, created, updated, delivered string
	err := row.Scan(&d.ID, &d.WebhookID, &d.EventID, &d.EventType, &d.InstanceID, &d.Payload, &d.Status, &d.Attempts,
		&next, &exp, &d.LastStatusCode, &d.LastError, &created, &updated, &delivered)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	d.NextAttemptAt, d.ExpiresAt, d.CreatedAt = parseTS(next), parseTS(exp), parseTS(created)
	d.UpdatedAt, d.DeliveredAt = parseTS(updated), parseTS(delivered)
	return d, err
}

// InsertDeliveries adds pending deliveries in one transaction and returns how many were
// new. A repeated (webhook, event) pair is ignored, and so is a delivery whose webhook
// no longer exists (deleted after the event was matched).
func (s *Store) InsertDeliveries(ctx context.Context, ds []Delivery, at time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ts := fmtTS(at)
	n := 0
	for _, d := range ds {
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO webhook_deliveries
			(id, webhook_id, event_id, event_type, instance_id, payload, status, next_attempt_at, expires_at, created_at, updated_at)
			SELECT ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM webhooks WHERE id = ?)`,
			d.ID, d.WebhookID, d.EventID, d.EventType, d.InstanceID, d.Payload, fmtTS(d.NextAttemptAt), fmtTS(d.ExpiresAt),
			ts, ts, d.WebhookID)
		if err != nil {
			return 0, err
		}
		if k, _ := res.RowsAffected(); k > 0 {
			n++
		}
	}
	return n, tx.Commit()
}

func excludeClause(exclude []string) (string, []any) {
	if len(exclude) == 0 {
		return "", nil
	}
	args := make([]any, len(exclude))
	for i, id := range exclude {
		args[i] = id
	}
	return ` AND webhook_id NOT IN (?` + strings.Repeat(",?", len(exclude)-1) + `)`, args
}

// DueDeliveries returns pending deliveries due at now, oldest first, skipping the
// excluded webhooks (busy or backing off).
func (s *Store) DueDeliveries(ctx context.Context, now time.Time, exclude []string, limit int) ([]Delivery, error) {
	ex, exArgs := excludeClause(exclude)
	args := append([]any{fmtTS(now)}, exArgs...)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT `+deliveryCols+` FROM webhook_deliveries
		WHERE status = 'pending' AND next_attempt_at <= ?`+ex+` ORDER BY next_attempt_at, seq LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// NextDeliveryAt is when the next pending delivery (outside the excluded webhooks) is due.
func (s *Store) NextDeliveryAt(ctx context.Context, exclude []string) (time.Time, bool, error) {
	ex, args := excludeClause(exclude)
	var v sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(next_attempt_at) FROM webhook_deliveries
		WHERE status = 'pending'`+ex, args...).Scan(&v); err != nil {
		return time.Time{}, false, err
	}
	if !v.Valid || v.String == "" {
		return time.Time{}, false, nil
	}
	return parseTS(v.String), true, nil
}

// FinishDelivery records an attempt of a pending delivery: status pending (retry at next),
// delivered or failed. ErrNotFound means it is no longer pending (retried, expired, deleted).
func (s *Store) FinishDelivery(ctx context.Context, id, status string, attempts int, next time.Time, code int, lastErr string, at time.Time) error {
	ts := fmtTS(at)
	delivered := ""
	if status == DeliveryDelivered {
		delivered = ts
	}
	return s.exec1(ctx, `UPDATE webhook_deliveries SET status = ?, attempts = ?, next_attempt_at = ?, last_status_code = ?,
		last_error = ?, updated_at = ?, delivered_at = ? WHERE id = ? AND status = 'pending'`,
		status, attempts, fmtTS(next), code, lastErr, ts, delivered, id)
}

func (s *Store) GetDelivery(ctx context.Context, webhookID, id string) (Delivery, error) {
	return scanDelivery(s.db.QueryRowContext(ctx,
		`SELECT `+deliveryCols+` FROM webhook_deliveries WHERE webhook_id = ? AND id = ?`, webhookID, id))
}

// ListDeliveries returns a webhook's deliveries newest first (optionally of one status),
// strictly older than cursor (a seq; 0 = from the newest), plus the next cursor (0 = end).
func (s *Store) ListDeliveries(ctx context.Context, webhookID, status string, limit int, cursor int64) ([]Delivery, int64, error) {
	q := `SELECT ` + deliveryCols + `, seq FROM webhook_deliveries WHERE webhook_id = ?`
	args := []any{webhookID}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	if cursor > 0 {
		q += ` AND seq < ?`
		args = append(args, cursor)
	}
	q += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Delivery
	var seqs []int64
	for rows.Next() {
		var d Delivery
		var seq int64
		var next, exp, created, updated, delivered string
		if err := rows.Scan(&d.ID, &d.WebhookID, &d.EventID, &d.EventType, &d.InstanceID, &d.Payload, &d.Status, &d.Attempts,
			&next, &exp, &d.LastStatusCode, &d.LastError, &created, &updated, &delivered, &seq); err != nil {
			return nil, 0, err
		}
		d.NextAttemptAt, d.ExpiresAt, d.CreatedAt = parseTS(next), parseTS(exp), parseTS(created)
		d.UpdatedAt, d.DeliveredAt = parseTS(updated), parseTS(delivered)
		out = append(out, d)
		seqs = append(seqs, seq)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var nextCursor int64
	if len(out) > limit {
		out = out[:limit]
		nextCursor = seqs[limit-1]
	}
	return out, nextCursor, nil
}

// RetryDelivery puts a failed delivery back in the queue with a fresh deadline.
// ErrNotFound means there is no failed delivery with that id for that webhook.
func (s *Store) RetryDelivery(ctx context.Context, webhookID, id string, at, expires time.Time) error {
	ts := fmtTS(at)
	return s.exec1(ctx, `UPDATE webhook_deliveries SET status = 'pending', attempts = 0, next_attempt_at = ?, expires_at = ?,
		last_error = '', updated_at = ? WHERE webhook_id = ? AND id = ? AND status = 'failed'`, ts, fmtTS(expires), ts, webhookID, id)
}

// ExpireDeliveries moves pending deliveries past their deadline to the dead-letter.
func (s *Store) ExpireDeliveries(ctx context.Context, at time.Time) (int64, error) {
	ts := fmtTS(at)
	res, err := s.db.ExecContext(ctx, `UPDATE webhook_deliveries SET status = 'failed',
		last_error = CASE WHEN last_error = '' THEN 'expired: not delivered within the retry period'
		                  ELSE 'expired: ' || last_error END,
		updated_at = ? WHERE status = 'pending' AND expires_at <= ?`, ts, ts)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PurgeDeliveries deletes finished deliveries of a status last updated before before.
func (s *Store) PurgeDeliveries(ctx context.Context, status string, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM webhook_deliveries WHERE status = ? AND status <> 'pending' AND updated_at < ?`,
		status, fmtTS(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
