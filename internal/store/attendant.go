package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Built-in attendant persistence (spec 4.7): per-instance settings (secrets encrypted by
// the caller), agent profiles and daily usage.

// AttendantSettings is an instance's stored attendant settings row.
type AttendantSettings struct {
	SettingsJSON string
	SecretsEnc   string // ciphertext; the store never sees the keys
	UpdatedAt    time.Time
}

func (s *Store) GetAttendantSettings(ctx context.Context, instanceID string) (AttendantSettings, error) {
	var a AttendantSettings
	var up string
	err := s.db.QueryRowContext(ctx, `SELECT settings_json, secrets_enc, updated_at FROM attendant_settings WHERE instance_id = ?`,
		instanceID).Scan(&a.SettingsJSON, &a.SecretsEnc, &up)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	a.UpdatedAt = parseTS(up)
	return a, err
}

func (s *Store) PutAttendantSettings(ctx context.Context, instanceID string, a AttendantSettings) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO attendant_settings (instance_id, settings_json, secrets_enc, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (instance_id) DO UPDATE SET settings_json = excluded.settings_json, secrets_enc = excluded.secrets_enc,
		updated_at = excluded.updated_at`, instanceID, a.SettingsJSON, a.SecretsEnc, now())
	return err
}

// AttendantAgent is a stored agent profile.
type AttendantAgent struct {
	ID          string
	InstanceID  string
	Name        string
	ProfileJSON string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const attendantAgentCols = `id, instance_id, name, profile_json, created_at, updated_at`

func scanAttendantAgent(row scanner) (AttendantAgent, error) {
	var a AttendantAgent
	var created, updated string
	err := row.Scan(&a.ID, &a.InstanceID, &a.Name, &a.ProfileJSON, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	a.CreatedAt, a.UpdatedAt = parseTS(created), parseTS(updated)
	return a, err
}

func (s *Store) CreateAttendantAgent(ctx context.Context, a AttendantAgent) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO attendant_agents (`+attendantAgentCols+`) VALUES (?, ?, ?, ?, ?, ?)`,
		a.ID, a.InstanceID, a.Name, a.ProfileJSON, ts, ts)
	return err
}

func (s *Store) UpdateAttendantAgent(ctx context.Context, a AttendantAgent) error {
	return s.exec1(ctx, `UPDATE attendant_agents SET name = ?, profile_json = ?, updated_at = ? WHERE instance_id = ? AND id = ?`,
		a.Name, a.ProfileJSON, now(), a.InstanceID, a.ID)
}

// GetAttendantAgent returns an agent of the instance (ErrNotFound for another instance's).
func (s *Store) GetAttendantAgent(ctx context.Context, instanceID, id string) (AttendantAgent, error) {
	return scanAttendantAgent(s.db.QueryRowContext(ctx,
		`SELECT `+attendantAgentCols+` FROM attendant_agents WHERE instance_id = ? AND id = ?`, instanceID, id))
}

func (s *Store) ListAttendantAgents(ctx context.Context, instanceID string) ([]AttendantAgent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+attendantAgentCols+` FROM attendant_agents WHERE instance_id = ?
		ORDER BY created_at, id`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AttendantAgent
	for rows.Next() {
		a, err := scanAttendantAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) CountAttendantAgents(ctx context.Context, instanceID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM attendant_agents WHERE instance_id = ?`, instanceID).Scan(&n)
	return n, err
}

func (s *Store) DeleteAttendantAgent(ctx context.Context, instanceID, id string) error {
	return s.exec1(ctx, `DELETE FROM attendant_agents WHERE instance_id = ? AND id = ?`, instanceID, id)
}

// AttendantUsage is one day of an instance's attendant usage.
type AttendantUsage struct {
	Day          string
	Calls        int
	InputTokens  int64
	OutputTokens int64
	CostMicros   int64
}

// ReserveAttendantCall counts one provider call for the instance's day unless the day
// already has cap calls (cap <= 0: no limit); ok is false when the cap is reached. The
// check and the increment are one statement, so concurrent chats cannot overshoot.
func (s *Store) ReserveAttendantCall(ctx context.Context, instanceID, day string, cap int) (bool, error) {
	if cap <= 0 {
		cap = int(^uint32(0) >> 1)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO attendant_usage (instance_id, day) VALUES (?, ?)
		ON CONFLICT (instance_id, day) DO NOTHING`, instanceID, day); err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE attendant_usage SET calls = calls + 1 WHERE instance_id = ? AND day = ? AND calls < ?`,
		instanceID, day, cap)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// AddAttendantTokens adds tokens and cost to the instance's day (the call itself was
// counted by ReserveAttendantCall).
func (s *Store) AddAttendantTokens(ctx context.Context, instanceID, day string, in, out, costMicros int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO attendant_usage (instance_id, day, input_tokens, output_tokens, cost_micros)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT (instance_id, day) DO UPDATE SET input_tokens = input_tokens + excluded.input_tokens,
		output_tokens = output_tokens + excluded.output_tokens, cost_micros = cost_micros + excluded.cost_micros`,
		instanceID, day, in, out, costMicros)
	return err
}

// ListAttendantUsage returns the instance's usage from day `from` on (YYYY-MM-DD), newest
// first.
func (s *Store) ListAttendantUsage(ctx context.Context, instanceID, from string) ([]AttendantUsage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT day, calls, input_tokens, output_tokens, cost_micros FROM attendant_usage
		WHERE instance_id = ? AND day >= ? ORDER BY day DESC`, instanceID, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AttendantUsage{}
	for rows.Next() {
		var u AttendantUsage
		if err := rows.Scan(&u.Day, &u.Calls, &u.InputTokens, &u.OutputTokens, &u.CostMicros); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ClientIDsForMessages maps logged message ids of API sends (= outbound queue ids) to
// their client_message_id; ids without a queue row or a client id are left out. Used to
// tell the agent's own replies and automatic messages apart from other API sends.
func (s *Store) ClientIDsForMessages(ctx context.Context, instanceID string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, instanceID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, client_message_id FROM outbound_queue WHERE instance_id = ?
		AND client_message_id IS NOT NULL AND client_message_id != '' AND id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, cid string
		if err := rows.Scan(&id, &cid); err != nil {
			return nil, err
		}
		out[id] = cid
	}
	return out, rows.Err()
}
