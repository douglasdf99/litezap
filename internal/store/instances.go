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
	// SettingsJSON holds the instance's settings overrides (a settings.Patch document).
	SettingsJSON string
	// FirstConnectedAt is when the instance first reached connected (zero if never); the ramp counts from it.
	FirstConnectedAt time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

const instanceCols = `id, name, state, state_reason, jid, token_hash, settings_json, first_connected_at, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanInstance(row scanner) (Instance, error) {
	var in Instance
	var state, firstConn, created, updated string
	err := row.Scan(&in.ID, &in.Name, &state, &in.StateReason, &in.JID, &in.TokenHash,
		&in.SettingsJSON, &firstConn, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return in, ErrNotFound
	}
	if err != nil {
		return in, err
	}
	in.State = instance.State(state)
	in.FirstConnectedAt = parseTS(firstConn)
	in.CreatedAt, _ = time.Parse(tsLayout, created)
	in.UpdatedAt, _ = time.Parse(tsLayout, updated)
	return in, nil
}

func (s *Store) CreateInstance(ctx context.Context, in Instance) error {
	ts := now()
	if in.SettingsJSON == "" {
		in.SettingsJSON = "{}"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO instances (`+instanceCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.Name, string(in.State), in.StateReason, in.JID, in.TokenHash, in.SettingsJSON,
		fmtTS(in.FirstConnectedAt), ts, ts)
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

func (s *Store) SetInstanceSettings(ctx context.Context, id, settingsJSON string) error {
	return s.exec1(ctx, `UPDATE instances SET settings_json = ?, updated_at = ? WHERE id = ?`, settingsJSON, now(), id)
}

// MarkFirstConnected records the first time an instance reached connected; later calls are no-ops.
func (s *Store) MarkFirstConnected(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE instances SET first_connected_at = ? WHERE id = ? AND first_connected_at = ''`, fmtTS(at), id)
	return err
}
