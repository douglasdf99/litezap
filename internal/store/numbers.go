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
