package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"time"
)

// ---- usage (cloud E11) ----

// Usage is one instance's counters for one UTC day (YYYY-MM-DD).
type Usage struct {
	InstanceID        string `json:"instance_id"`
	Day               string `json:"day"`
	MessagesSent      int64  `json:"messages_sent"`
	MessagesReceived  int64  `json:"messages_received"`
	MediaBytes        int64  `json:"media_bytes"`
	ConnectedSeconds  int64  `json:"connected_seconds"`
	WebhookDeliveries int64  `json:"webhook_deliveries"`
}

// AddUsage adds the deltas to their rows (WebhookDeliveries is ignored: see
// SyncWebhookUsage), in one transaction.
func (s *Store) AddUsage(ctx context.Context, deltas []Usage) error {
	if len(deltas) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, d := range deltas {
		if _, err := tx.ExecContext(ctx, `INSERT INTO usage_daily
			(instance_id, day, messages_sent, messages_received, media_bytes, connected_seconds)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (instance_id, day) DO UPDATE SET
			  messages_sent = messages_sent + excluded.messages_sent,
			  messages_received = messages_received + excluded.messages_received,
			  media_bytes = media_bytes + excluded.media_bytes,
			  connected_seconds = connected_seconds + excluded.connected_seconds`,
			d.InstanceID, d.Day, d.MessagesSent, d.MessagesReceived, d.MediaBytes, d.ConnectedSeconds); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SyncWebhookUsage recounts deliveries to customer webhooks (not the operator's) per
// instance and day from sinceDay on. The count only grows, so deliveries pruned by the
// webhook retention are never subtracted.
func (s *Store) SyncWebhookUsage(ctx context.Context, sinceDay string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO usage_daily (instance_id, day, webhook_deliveries)
		SELECT d.instance_id, substr(d.delivered_at, 1, 10), COUNT(*)
		FROM webhook_deliveries d JOIN webhooks w ON w.id = d.webhook_id
		WHERE d.status = 'delivered' AND w.owner = '' AND d.instance_id <> '' AND d.delivered_at >= ?
		GROUP BY 1, 2
		ON CONFLICT (instance_id, day) DO UPDATE SET
		  webhook_deliveries = MAX(webhook_deliveries, excluded.webhook_deliveries)`, sinceDay)
	return err
}

// ListUsage returns the rows with from <= day <= to (YYYY-MM-DD), optionally of one
// instance, ordered by day then instance.
func (s *Store) ListUsage(ctx context.Context, from, to, instanceID string) ([]Usage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT instance_id, day, messages_sent, messages_received, media_bytes,
		connected_seconds, webhook_deliveries FROM usage_daily
		WHERE day >= ? AND day <= ? AND (? = '' OR instance_id = ?) ORDER BY day, instance_id`,
		from, to, instanceID, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Usage{}
	for rows.Next() {
		var u Usage
		if err := rows.Scan(&u.InstanceID, &u.Day, &u.MessagesSent, &u.MessagesReceived, &u.MediaBytes,
			&u.ConnectedSeconds, &u.WebhookDeliveries); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UsageTotals sums every usage row (monotonic totals for /metrics).
func (s *Store) UsageTotals(ctx context.Context) (Usage, error) {
	var u Usage
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(messages_sent), 0), COALESCE(SUM(messages_received), 0),
		COALESCE(SUM(media_bytes), 0), COALESCE(SUM(connected_seconds), 0), COALESCE(SUM(webhook_deliveries), 0)
		FROM usage_daily`).Scan(&u.MessagesSent, &u.MessagesReceived, &u.MediaBytes, &u.ConnectedSeconds, &u.WebhookDeliveries)
	return u, err
}

// ---- runtime engine settings ----

// GetKV returns a runtime setting (ErrNotFound when unset).
func (s *Store) GetKV(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM engine_kv WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

func (s *Store) SetKV(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO engine_kv (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, value, now())
	return err
}

func (s *Store) DeleteKV(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM engine_kv WHERE key = ?`, key)
	return err
}

// ---- per-instance operator flags ----

// Suspension is an instance whose sending the operator suspended.
type Suspension struct {
	InstanceID string    `json:"instance_id"`
	Reason     string    `json:"reason,omitempty"`
	Since      time.Time `json:"since"`
}

// SetSendingSuspended suspends or releases an instance's sending (ErrNotFound for an
// unknown instance).
func (s *Store) SetSendingSuspended(ctx context.Context, instanceID string, suspended bool, reason string) error {
	if _, err := s.GetInstance(ctx, instanceID); err != nil {
		return err
	}
	if !suspended {
		_, err := s.db.ExecContext(ctx, `DELETE FROM instance_flags WHERE instance_id = ?`, instanceID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO instance_flags (instance_id, sending_suspended, suspend_reason, updated_at)
		VALUES (?, 1, ?, ?) ON CONFLICT (instance_id) DO UPDATE SET sending_suspended = 1,
		suspend_reason = excluded.suspend_reason, updated_at = excluded.updated_at`, instanceID, reason, now())
	return err
}

func (s *Store) ListSuspensions(ctx context.Context) ([]Suspension, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT instance_id, suspend_reason, updated_at FROM instance_flags
		WHERE sending_suspended = 1 ORDER BY instance_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Suspension{}
	for rows.Next() {
		var x Suspension
		var ts string
		if err := rows.Scan(&x.InstanceID, &x.Reason, &ts); err != nil {
			return nil, err
		}
		x.Since = parseTS(ts)
		out = append(out, x)
	}
	return out, rows.Err()
}

// ---- counts for limits and metrics ----

func (s *Store) CountInstances(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM instances`).Scan(&n)
	return n, err
}

// CountBy runs one of the fixed grouping queries below and returns value -> count.
func (s *Store) countBy(ctx context.Context, q string) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

func (s *Store) CountInstancesByState(ctx context.Context) (map[string]int64, error) {
	return s.countBy(ctx, `SELECT state, COUNT(*) FROM instances GROUP BY state`)
}

// CountActiveOutbound counts queued and in_flight sends.
func (s *Store) CountActiveOutbound(ctx context.Context) (map[string]int64, error) {
	return s.countBy(ctx, `SELECT status, COUNT(*) FROM outbound_queue WHERE status IN ('queued', 'in_flight') GROUP BY status`)
}

func (s *Store) CountDeliveriesByStatus(ctx context.Context) (map[string]int64, error) {
	return s.countBy(ctx, `SELECT status, COUNT(*) FROM webhook_deliveries GROUP BY status`)
}

// ---- retention (cloud E15) ----

// ExpireMediaFiles deletes local media copies whose expiry passed before now and clears
// their path (the attachment is downloaded again on the next read). It returns how many
// files were dropped.
func (s *Store) ExpireMediaFiles(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, path FROM media WHERE path <> '' AND expires_at <> '' AND expires_at < ?`, fmtTS(now))
	if err != nil {
		return 0, err
	}
	type file struct{ id, path string }
	var files []file
	for rows.Next() {
		var f file
		if err := rows.Scan(&f.id, &f.path); err != nil {
			rows.Close()
			return 0, err
		}
		files = append(files, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, f := range files {
		if err := os.Remove(f.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			continue // retried on the next sweep
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE media SET path = '', expires_at = '' WHERE id = ? AND path = ?`, f.id, f.path); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// pruneBatch bounds each retention transaction: the store has one connection, so a
// long transaction would hold every other write (ingest, queue, webhooks) behind it.
var pruneBatch int64 = 2000

// PruneMessages deletes messages older than before, their media (rows and local files)
// and finished sends (sent or failed) last updated before it, in small transactions. It
// returns the number of messages deleted.
func (s *Store) PruneMessages(ctx context.Context, before time.Time) (int64, error) {
	cut := fmtTS(before)
	var total int64
	for {
		n, err := s.pruneMessageBatch(ctx, cut)
		total += n
		if err != nil || n < pruneBatch {
			if err != nil {
				return total, err
			}
			break
		}
	}
	for {
		res, err := s.db.ExecContext(ctx, `DELETE FROM outbound_queue WHERE seq IN (SELECT seq FROM outbound_queue
			WHERE status IN ('sent', 'failed') AND updated_at < ? LIMIT ?)`, cut, pruneBatch)
		if err != nil {
			return total, err
		}
		if n, _ := res.RowsAffected(); n < pruneBatch {
			return total, nil
		}
	}
}

func (s *Store) pruneMessageBatch(ctx context.Context, cut string) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, id FROM messages WHERE timestamp < ? LIMIT ?`, cut, pruneBatch)
	if err != nil {
		return 0, err
	}
	var seqs []any
	var ids []any
	for rows.Next() {
		var seq int64
		var id string
		if err := rows.Scan(&seq, &id); err != nil {
			rows.Close()
			return 0, err
		}
		seqs, ids = append(seqs, seq), append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(seqs) == 0 {
		return 0, err
	}
	in := strings.Repeat("?,", len(ids))
	in = in[:len(in)-1]
	prows, err := s.db.QueryContext(ctx, `SELECT path FROM media WHERE path <> '' AND message_id IN (`+in+`)`, ids...)
	if err != nil {
		return 0, err
	}
	var paths []string
	for prows.Next() {
		var p string
		if err := prows.Scan(&p); err != nil {
			prows.Close()
			return 0, err
		}
		paths = append(paths, p)
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM media WHERE message_id IN (`+in+`)`, ids...); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE seq IN (`+in+`)`, seqs...)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	for _, p := range paths {
		_ = os.Remove(p)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ScrubForExport removes the operator's data from a backup copy (its webhooks with their
// secrets, runtime settings, per-instance flags, proxy URLs) and the instance token
// hashes, and rewrites the file so the deleted
// rows do not linger in free pages. Run it on the copy, never on the live database.
func (s *Store) ScrubForExport(ctx context.Context) error {
	for _, q := range []string{
		`DELETE FROM webhooks WHERE owner = 'operator'`,
		`DELETE FROM engine_kv`,
		`DELETE FROM instance_flags`,
		// Proxy credentials never leave the server (the operator's pool, E4); instance
		// tokens are not exported either (a restore issues new ones).
		`UPDATE instances SET settings_json = json_remove(settings_json, '$.proxy_url')
			WHERE json_valid(settings_json) AND json_type(settings_json, '$.proxy_url') IS NOT NULL`,
		`UPDATE instances SET token_hash = 'exported:' || id`,
		`VACUUM`,
		`PRAGMA wal_checkpoint(TRUNCATE)`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// BackupTo writes a consistent copy of the database to path (which must not exist),
// without stopping writers (VACUUM INTO).
func (s *Store) BackupTo(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}
