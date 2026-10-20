package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// LiteZap agents persistence (migration 0013): agents, their runs and the drafts waiting
// for approval.

// AgentRow is a stored agent (the JSON columns are decoded by package agents).
type AgentRow struct {
	ID           string
	InstanceID   string
	Name         string
	Enabled      bool
	Mode         string
	Instructions string
	ToolsJSON    string
	TriggerJSON  string
	LimitsJSON   string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

const agentCols = `id, instance_id, name, enabled, mode, instructions, tools_json, trigger_json, limits_json, created_at, updated_at`

func scanAgent(row scanner) (AgentRow, error) {
	var a AgentRow
	var created, updated string
	err := row.Scan(&a.ID, &a.InstanceID, &a.Name, &a.Enabled, &a.Mode, &a.Instructions, &a.ToolsJSON, &a.TriggerJSON,
		&a.LimitsJSON, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	a.CreatedAt, a.UpdatedAt = parseTS(created), parseTS(updated)
	return a, err
}

func (s *Store) CreateAgent(ctx context.Context, a AgentRow) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO agents (`+agentCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.InstanceID, a.Name, a.Enabled, a.Mode, a.Instructions, a.ToolsJSON, a.TriggerJSON, a.LimitsJSON, ts, ts)
	return err
}

func (s *Store) UpdateAgent(ctx context.Context, a AgentRow) error {
	return s.exec1(ctx, `UPDATE agents SET name = ?, enabled = ?, mode = ?, instructions = ?, tools_json = ?, trigger_json = ?,
		limits_json = ?, updated_at = ? WHERE instance_id = ? AND id = ?`,
		a.Name, a.Enabled, a.Mode, a.Instructions, a.ToolsJSON, a.TriggerJSON, a.LimitsJSON, now(), a.InstanceID, a.ID)
}

// GetAgent returns an agent of the instance (ErrNotFound for another instance's).
func (s *Store) GetAgent(ctx context.Context, instanceID, id string) (AgentRow, error) {
	return scanAgent(s.db.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE instance_id = ? AND id = ?`, instanceID, id))
}

// ListAgents returns the instance's agents, oldest first.
func (s *Store) ListAgents(ctx context.Context, instanceID string) ([]AgentRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+agentCols+` FROM agents WHERE instance_id = ? ORDER BY created_at, id`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentRow{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteAgent removes an agent with its runs and drafts.
func (s *Store) DeleteAgent(ctx context.Context, instanceID, id string) error {
	return s.exec1(ctx, `DELETE FROM agents WHERE instance_id = ? AND id = ?`, instanceID, id)
}

// AgentRun is one execution of an agent.
type AgentRun struct {
	ID           string
	InstanceID   string
	AgentID      string
	AgentName    string
	Trigger      string
	ChatJID      string
	MessageID    string
	Mode         string
	Status       string
	StepsJSON    string
	Output       string
	Model        string
	ModelCalls   int
	InputTokens  int64
	OutputTokens int64
	CostMicros   int64
	Sent         int
	Drafts       int
	Error        string
	StartedAt    time.Time
	EndedAt      time.Time
}

const agentRunCols = `id, instance_id, agent_id, agent_name, trigger, chat_jid, message_id, mode, status, steps_json, output, model,
	model_calls, input_tokens, output_tokens, cost_micros, sent, drafts, error, started_at, ended_at`

func scanAgentRun(row scanner) (AgentRun, error) {
	var r AgentRun
	var started, ended string
	err := row.Scan(&r.ID, &r.InstanceID, &r.AgentID, &r.AgentName, &r.Trigger, &r.ChatJID, &r.MessageID, &r.Mode, &r.Status,
		&r.StepsJSON, &r.Output, &r.Model, &r.ModelCalls, &r.InputTokens, &r.OutputTokens, &r.CostMicros, &r.Sent, &r.Drafts,
		&r.Error, &started, &ended)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	r.StartedAt, r.EndedAt = parseTS(started), parseTS(ended)
	return r, err
}

// AgentRunRetention is how long runs (and their drafts) are kept.
const AgentRunRetention = 30 * 24 * time.Hour

// InsertAgentRun logs a run (usually with status running) and, now and then, prunes runs
// older than AgentRunRetention.
func (s *Store) InsertAgentRun(ctx context.Context, r AgentRun) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO agent_runs (`+agentRunCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.InstanceID, r.AgentID, r.AgentName, r.Trigger, r.ChatJID, r.MessageID, r.Mode, r.Status, nonEmpty(r.StepsJSON, "[]"),
		r.Output, r.Model, r.ModelCalls, r.InputTokens, r.OutputTokens, r.CostMicros, r.Sent, r.Drafts, r.Error,
		fmtTS(r.StartedAt), fmtTS(r.EndedAt))
	if err != nil {
		return err
	}
	if seq, err := res.LastInsertId(); err == nil && seq%100 == 0 {
		_, err = s.db.ExecContext(ctx, `DELETE FROM agent_runs WHERE started_at < ?`, fmtTS(r.StartedAt.Add(-AgentRunRetention)))
		return err
	}
	return nil
}

// FinishAgentRun stores a run's outcome.
func (s *Store) FinishAgentRun(ctx context.Context, r AgentRun) error {
	return s.exec1(ctx, `UPDATE agent_runs SET status = ?, steps_json = ?, output = ?, model = ?, model_calls = ?,
		input_tokens = ?, output_tokens = ?, cost_micros = ?, sent = ?, drafts = ?, error = ?, ended_at = ?
		WHERE instance_id = ? AND id = ?`,
		r.Status, nonEmpty(r.StepsJSON, "[]"), r.Output, r.Model, r.ModelCalls, r.InputTokens, r.OutputTokens, r.CostMicros,
		r.Sent, r.Drafts, r.Error, fmtTS(r.EndedAt), r.InstanceID, r.ID)
}

func (s *Store) GetAgentRun(ctx context.Context, instanceID, id string) (AgentRun, error) {
	return scanAgentRun(s.db.QueryRowContext(ctx, `SELECT `+agentRunCols+` FROM agent_runs WHERE instance_id = ? AND id = ?`,
		instanceID, id))
}

// ListAgentRuns returns an agent's runs (every agent's with agentID ""), newest first.
func (s *Store) ListAgentRuns(ctx context.Context, instanceID, agentID string, limit int) ([]AgentRun, error) {
	q := `SELECT ` + agentRunCols + ` FROM agent_runs WHERE instance_id = ?`
	args := []any{instanceID}
	if agentID != "" {
		q += ` AND agent_id = ?`
		args = append(args, agentID)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY seq DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentRun{}
	for rows.Next() {
		r, err := scanAgentRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AgentDayStats is an agent's runs and cost since a time (today, for the dashboard).
type AgentDayStats struct {
	Runs       int
	CostMicros int64
}

// AgentStatsSince sums each agent's runs and cost since t.
func (s *Store) AgentStatsSince(ctx context.Context, instanceID string, since time.Time) (map[string]AgentDayStats, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id, COUNT(*), COALESCE(SUM(cost_micros), 0) FROM agent_runs
		WHERE instance_id = ? AND started_at >= ? GROUP BY agent_id`, instanceID, fmtTS(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]AgentDayStats{}
	for rows.Next() {
		var id string
		var st AgentDayStats
		if err := rows.Scan(&id, &st.Runs, &st.CostMicros); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

// AgentDraft is a write tool call waiting for a human decision.
type AgentDraft struct {
	ID         string
	InstanceID string
	AgentID    string
	RunID      string
	Tool       string
	ArgsJSON   string
	ChatJID    string
	Status     string
	DecidedBy  string
	DecidedAt  time.Time
	Result     string
	CreatedAt  time.Time
}

const agentDraftCols = `id, instance_id, agent_id, run_id, tool, args_json, chat_jid, status, decided_by, decided_at, result, created_at`

func scanAgentDraft(row scanner) (AgentDraft, error) {
	var d AgentDraft
	var decided, created string
	err := row.Scan(&d.ID, &d.InstanceID, &d.AgentID, &d.RunID, &d.Tool, &d.ArgsJSON, &d.ChatJID, &d.Status, &d.DecidedBy,
		&decided, &d.Result, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	d.DecidedAt, d.CreatedAt = parseTS(decided), parseTS(created)
	return d, err
}

func (s *Store) CreateAgentDraft(ctx context.Context, d AgentDraft) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_drafts (`+agentDraftCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.InstanceID, d.AgentID, d.RunID, d.Tool, d.ArgsJSON, d.ChatJID, d.Status, d.DecidedBy, fmtTS(d.DecidedAt), d.Result,
		fmtTS(d.CreatedAt))
	return err
}

func (s *Store) GetAgentDraft(ctx context.Context, instanceID, id string) (AgentDraft, error) {
	return scanAgentDraft(s.db.QueryRowContext(ctx, `SELECT `+agentDraftCols+` FROM agent_drafts WHERE instance_id = ? AND id = ?`,
		instanceID, id))
}

// ListAgentDrafts returns drafts (of one status, or all with ""), newest first.
func (s *Store) ListAgentDrafts(ctx context.Context, instanceID, status string, limit int) ([]AgentDraft, error) {
	q := `SELECT ` + agentDraftCols + ` FROM agent_drafts WHERE instance_id = ?`
	args := []any{instanceID}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY seq DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentDraft{}
	for rows.Next() {
		d, err := scanAgentDraft(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ClaimAgentDraft moves a pending draft to status (approving or rejected) for decidedBy;
// false when it is no longer pending (decided by someone else meanwhile).
func (s *Store) ClaimAgentDraft(ctx context.Context, instanceID, id, status, decidedBy string, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE agent_drafts SET status = ?, decided_by = ?, decided_at = ?
		WHERE instance_id = ? AND id = ? AND status = 'pending'`, status, decidedBy, fmtTS(at), instanceID, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// SettleAgentDraft stores a decided draft's final status and result.
func (s *Store) SettleAgentDraft(ctx context.Context, instanceID, id, status, result string) error {
	return s.exec1(ctx, `UPDATE agent_drafts SET status = ?, result = ? WHERE instance_id = ? AND id = ?`,
		status, result, instanceID, id)
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
