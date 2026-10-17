package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/douglasdf99/litezap/internal/instance"
)

type Instance struct {
	ID          string
	Name        string
	State       instance.State
	StateReason string
	JID         string
	TokenHash   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const instanceCols = `id, name, state, state_reason, jid, token_hash, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanInstance(row scanner) (Instance, error) {
	var in Instance
	var state, created, updated string
	err := row.Scan(&in.ID, &in.Name, &state, &in.StateReason, &in.JID, &in.TokenHash, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return in, ErrNotFound
	}
	if err != nil {
		return in, err
	}
	in.State = instance.State(state)
	in.CreatedAt, _ = time.Parse(tsLayout, created)
	in.UpdatedAt, _ = time.Parse(tsLayout, updated)
	return in, nil
}

func (s *Store) CreateInstance(ctx context.Context, in Instance) error {
	ts := now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO instances (`+instanceCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.Name, string(in.State), in.StateReason, in.JID, in.TokenHash, ts, ts)
	return err
}

func (s *Store) GetInstance(ctx context.Context, id string) (Instance, error) {
	return scanInstance(s.db.QueryRowContext(ctx, `SELECT `+instanceCols+` FROM instances WHERE id = ?`, id))
}

func (s *Store) InstanceByTokenHash(ctx context.Context, hash string) (Instance, error) {
	return scanInstance(s.db.QueryRowContext(ctx, `SELECT `+instanceCols+` FROM instances WHERE token_hash = ?`, hash))
}

func (s *Store) ListInstances(ctx context.Context) ([]Instance, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+instanceCols+` FROM instances ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Instance{}
	for rows.Next() {
		in, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

func (s *Store) UpdateInstanceState(ctx context.Context, id string, st instance.State, reason string) error {
	return s.exec1(ctx, `UPDATE instances SET state = ?, state_reason = ?, updated_at = ? WHERE id = ?`,
		string(st), reason, now(), id)
}

func (s *Store) SetInstanceJID(ctx context.Context, id, jid string) error {
	return s.exec1(ctx, `UPDATE instances SET jid = ?, updated_at = ? WHERE id = ?`, jid, now(), id)
}

func (s *Store) SetInstanceTokenHash(ctx context.Context, id, hash string) error {
	return s.exec1(ctx, `UPDATE instances SET token_hash = ?, updated_at = ? WHERE id = ?`, hash, now(), id)
}

func (s *Store) DeleteInstance(ctx context.Context, id string) error {
	return s.exec1(ctx, `DELETE FROM instances WHERE id = ?`, id)
}
