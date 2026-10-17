package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// CachedNumber is a phone lookup result (IsOnWhatsApp).
type CachedNumber struct {
	Phone     string
	Found     bool
	JID       string
	CheckedAt time.Time
}

func (s *Store) GetCachedNumber(ctx context.Context, instanceID, phone string) (CachedNumber, error) {
	c := CachedNumber{Phone: phone}
	var checked string
	err := s.db.QueryRowContext(ctx, `SELECT found, jid, checked_at FROM number_cache WHERE instance_id = ? AND phone = ?`,
		instanceID, phone).Scan(&c.Found, &c.JID, &checked)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.CheckedAt = parseTS(checked)
	return c, err
}

func (s *Store) PutCachedNumber(ctx context.Context, instanceID string, c CachedNumber) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO number_cache (instance_id, phone, found, jid, checked_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (instance_id, phone) DO UPDATE SET found = excluded.found, jid = excluded.jid, checked_at = excluded.checked_at`,
		instanceID, c.Phone, c.Found, c.JID, fmtTS(c.CheckedAt))
	return err
}

// LookupsSince counts numbers looked up (cache entries written) since t: the send queue's
// daily lookup cap. A Brazilian number answered with the other 9th-digit form writes two
// entries, so the count errs on the safe side.
func (s *Store) LookupsSince(ctx context.Context, instanceID string, t time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM number_cache WHERE instance_id = ? AND checked_at >= ?`,
		instanceID, fmtTS(t)).Scan(&n)
	return n, err
}
