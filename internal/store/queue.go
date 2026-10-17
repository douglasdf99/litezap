package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Outbound statuses (spec 4.1).
const (
	OutQueued   = "queued"
	OutInFlight = "in_flight"
	OutSent     = "sent"
	OutFailed   = "failed"
)

// Outbound is one item of the persistent send queue.
type Outbound struct {
	ID              string
	InstanceID      string
	ClientMessageID string
	ChatJID         string
	WAMessageID     string
	Payload         string // JSON, owned by the queue package
	Status          string
	Attempts        int
	NextAttemptAt   time.Time
	LastError       string
	NewRecipient    bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
	SentAt          time.Time
	ExpiresAt       time.Time // zero: never
	RequestHash     string
	LookupFailures  int
}

const outboundCols = `id, instance_id, COALESCE(client_message_id, ''), chat_jid, wa_message_id, payload_json, status,
	attempts, next_attempt_at, last_error, new_recipient, created_at, updated_at, sent_at, expires_at, request_hash, lookup_failures`

func scanOutbound(row scanner) (Outbound, error) {
	var o Outbound
	var next, created, updated, sent, expires string
	err := row.Scan(&o.ID, &o.InstanceID, &o.ClientMessageID, &o.ChatJID, &o.WAMessageID, &o.Payload, &o.Status,
		&o.Attempts, &next, &o.LastError, &o.NewRecipient, &created, &updated, &sent, &expires, &o.RequestHash, &o.LookupFailures)
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrNotFound
	}
	o.NextAttemptAt, o.CreatedAt, o.UpdatedAt, o.SentAt = parseTS(next), parseTS(created), parseTS(updated), parseTS(sent)
	o.ExpiresAt = parseTS(expires)
	return o, err
}

// Enqueue stores a new queued item, due at o.NextAttemptAt (zero: now). With a
// ClientMessageID already used by the instance, nothing is inserted and the existing
// item is returned with created=false (idempotency).
func (s *Store) Enqueue(ctx context.Context, o Outbound) (Outbound, bool, error) {
	var clientID any
	if o.ClientMessageID != "" {
		clientID = o.ClientMessageID
	}
	ts := now()
	due := ts
	if !o.NextAttemptAt.IsZero() {
		due = fmtTS(o.NextAttemptAt)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO outbound_queue
		(id, instance_id, client_message_id, chat_jid, wa_message_id, payload_json, status, next_attempt_at,
		 created_at, updated_at, expires_at, request_hash)
		VALUES (?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?)
		ON CONFLICT (instance_id, client_message_id) DO NOTHING`,
		o.ID, o.InstanceID, clientID, o.ChatJID, o.WAMessageID, o.Payload, due, ts, ts, fmtTS(o.ExpiresAt), o.RequestHash)
	if err != nil {
		return Outbound{}, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		got, err := s.OutboundByClientID(ctx, o.InstanceID, o.ClientMessageID)
		return got, false, err
	}
	got, err := s.GetOutbound(ctx, o.InstanceID, o.ID)
	return got, true, err
}

// OutboundByClientID finds an item by the client_message_id given at enqueue.
func (s *Store) OutboundByClientID(ctx context.Context, instanceID, clientID string) (Outbound, error) {
	return scanOutbound(s.db.QueryRowContext(ctx,
		`SELECT `+outboundCols+` FROM outbound_queue WHERE instance_id = ? AND client_message_id = ?`,
		instanceID, clientID))
}

func (s *Store) GetOutbound(ctx context.Context, instanceID, id string) (Outbound, error) {
	return scanOutbound(s.db.QueryRowContext(ctx,
		`SELECT `+outboundCols+` FROM outbound_queue WHERE instance_id = ? AND id = ?`, instanceID, id))
}

// chatHead restricts q to the oldest unfinished item of its chat: strict per-recipient order.
const chatHead = `q.seq = (SELECT MIN(p.seq) FROM outbound_queue p
	WHERE p.instance_id = q.instance_id AND p.chat_jid = q.chat_jid AND p.status IN ('queued', 'in_flight'))`

// NextOutbound returns the oldest queued item that heads its chat and is due at now
// (ErrNotFound when there is none).
func (s *Store) NextOutbound(ctx context.Context, instanceID string, at time.Time) (Outbound, error) {
	return scanOutbound(s.db.QueryRowContext(ctx, `SELECT `+outboundCols+` FROM outbound_queue q
		WHERE q.instance_id = ? AND q.status = 'queued' AND q.next_attempt_at <= ? AND `+chatHead+`
		ORDER BY q.seq LIMIT 1`, instanceID, fmtTS(at)))
}

// NextDue is the earliest next_attempt_at among queued chat heads (ok=false when the queue is empty).
func (s *Store) NextDue(ctx context.Context, instanceID string) (time.Time, bool, error) {
	var v sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT MIN(q.next_attempt_at) FROM outbound_queue q
		WHERE q.instance_id = ? AND q.status = 'queued' AND `+chatHead, instanceID).Scan(&v)
	if err != nil || !v.Valid {
		return time.Time{}, false, err
	}
	return parseTS(v.String), true, nil
}

// MarkInFlight moves a queued item to in_flight (newRecipient: it counts against the ramp)
// and logs m as "pending" in the same transaction, so a receipt that arrives before
// MarkSent still finds the row.
func (s *Store) MarkInFlight(ctx context.Context, id string, newRecipient bool, m Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var instanceID string
	if err := tx.QueryRowContext(ctx, `SELECT instance_id FROM outbound_queue WHERE id = ? AND status = 'queued'`, id).Scan(&instanceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE outbound_queue SET status = 'in_flight', new_recipient = ?, updated_at = ?
		WHERE id = ?`, newRecipient, now(), id); err != nil {
		return err
	}
	m.Status = "pending"
	if _, err := insertMessage(ctx, tx, instanceID, m, nil); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkSent finishes an in_flight item: the pending log row becomes sent (unless a receipt
// already raised it) and the chat is updated, in one transaction.
func (s *Store) MarkSent(ctx context.Context, id string, sentAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var instanceID, chat string
	if err := tx.QueryRowContext(ctx, `SELECT instance_id, chat_jid FROM outbound_queue WHERE id = ?`, id).Scan(&instanceID, &chat); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	ts := fmtTS(sentAt)
	if _, err := tx.ExecContext(ctx, `UPDATE outbound_queue SET status = 'sent', sent_at = ?, last_error = '', updated_at = ?
		WHERE id = ?`, ts, now(), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET timestamp = ?,
		status = CASE WHEN status = 'pending' THEN 'sent' ELSE status END
		WHERE instance_id = ? AND id = ?`, ts, instanceID, id); err != nil {
		return err
	}
	if err := upsertChat(ctx, tx, instanceID, chat, sentAt, ChatUpdate{}); err != nil {
		return err
	}
	return tx.Commit()
}

// Requeue returns an in_flight item to the queue, due at next, with the given attempt count.
func (s *Store) Requeue(ctx context.Context, id string, next time.Time, attempts int, lastErr string) error {
	return s.exec1(ctx, `UPDATE outbound_queue SET status = 'queued', next_attempt_at = ?, attempts = ?,
		last_error = ?, new_recipient = 0, updated_at = ? WHERE id = ? AND status = 'in_flight'`,
		fmtTS(next), attempts, lastErr, now(), id)
}

// Defer postpones a queued item without counting an attempt (ramp limit reached).
func (s *Store) Defer(ctx context.Context, id string, next time.Time, reason string) error {
	return s.exec1(ctx, `UPDATE outbound_queue SET next_attempt_at = ?, last_error = ?, updated_at = ?
		WHERE id = ? AND status = 'queued'`, fmtTS(next), reason, now(), id)
}

// DeferLookup postpones a queued item whose recipient lookup failed, recording the count
// of consecutive lookup failures (no send attempt is spent).
func (s *Store) DeferLookup(ctx context.Context, id string, next time.Time, failures int, reason string) error {
	return s.exec1(ctx, `UPDATE outbound_queue SET next_attempt_at = ?, lookup_failures = ?, last_error = ?, updated_at = ?
		WHERE id = ? AND status = 'queued'`, fmtTS(next), failures, reason, now(), id)
}

// MarkFailed finishes an item for good and drops its pending log row.
func (s *Store) MarkFailed(ctx context.Context, id string, attempts int, lastErr string) error {
	if err := s.exec1(ctx, `UPDATE outbound_queue SET status = 'failed', attempts = ?, last_error = ?, updated_at = ?
		WHERE id = ? AND status IN ('queued', 'in_flight')`, attempts, lastErr, now(), id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE id = ? AND status = 'pending'`, id)
	return err
}

// RetargetChat moves the queued items of a chat to another JID (e.g. the number resolved
// with the extra 9th digit), keeping their order.
func (s *Store) RetargetChat(ctx context.Context, instanceID, from, to string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE outbound_queue SET chat_jid = ?, updated_at = ?
		WHERE instance_id = ? AND chat_jid = ? AND status = 'queued'`, to, now(), instanceID, from)
	return err
}

// RecoverInFlight runs at boot: an in_flight item whose log row was already raised past
// pending (a receipt arrived) was sent; anything else goes back to queued.
// It returns how many items were requeued.
func (s *Store) RecoverInFlight(ctx context.Context) (int, error) {
	ts := now()
	if _, err := s.db.ExecContext(ctx, `UPDATE outbound_queue SET status = 'sent', sent_at = ?, updated_at = ?
		WHERE status = 'in_flight' AND EXISTS (SELECT 1 FROM messages m
		  WHERE m.instance_id = outbound_queue.instance_id AND m.wa_message_id = outbound_queue.wa_message_id
		    AND m.from_me = 1 AND m.status <> 'pending')`,
		ts, ts); err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE outbound_queue SET status = 'queued', new_recipient = 0, updated_at = ?
		WHERE status = 'in_flight'`, ts)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// NewRecipientsSince counts distinct chats that consumed the ramp since t, and whether chatJID is one of them.
func (s *Store) NewRecipientsSince(ctx context.Context, instanceID, chatJID string, t time.Time) (count int, counted bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT chat_jid), COALESCE(MAX(chat_jid = ?), 0) FROM outbound_queue
		WHERE instance_id = ? AND new_recipient = 1 AND status = 'sent' AND sent_at >= ?`,
		chatJID, instanceID, fmtTS(t)).Scan(&count, &counted)
	return count, counted, err
}

// IsOwnSent reports whether the API sent a message with this WhatsApp id (human detection, spec 4.3).
func (s *Store) IsOwnSent(ctx context.Context, instanceID, waID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbound_queue WHERE instance_id = ? AND wa_message_id = ?`,
		instanceID, waID).Scan(&n)
	return n > 0, err
}
