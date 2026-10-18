package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Agent hook persistence (spec 4.3): the per-instance config, the per-chat override and
// pause (columns of chats), and the log of recent hook calls.

// GetAgentHook returns the stored config JSON of an instance's agent hook.
func (s *Store) GetAgentHook(ctx context.Context, instanceID string) (string, error) {
	var js string
	err := s.db.QueryRowContext(ctx, `SELECT config_json FROM agent_hooks WHERE instance_id = ?`, instanceID).Scan(&js)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return js, err
}

// PutAgentHook stores (replacing) an instance's agent hook config.
func (s *Store) PutAgentHook(ctx context.Context, instanceID, configJSON string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_hooks (instance_id, config_json, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (instance_id) DO UPDATE SET config_json = excluded.config_json, updated_at = excluded.updated_at`,
		instanceID, configJSON, now())
	return err
}

func (s *Store) DeleteAgentHook(ctx context.Context, instanceID string) error {
	return s.exec1(ctx, `DELETE FROM agent_hooks WHERE instance_id = ?`, instanceID)
}

// ChatAgent is the agent state of a chat: Enabled nil inherits the instance default.
type ChatAgent struct {
	JID         string
	Name        string
	IsGroup     bool
	Enabled     *bool
	PausedUntil time.Time // zero: not paused
}

func scanChatAgent(row scanner) (ChatAgent, error) {
	var c ChatAgent
	var en sql.NullBool
	var paused string
	err := row.Scan(&c.JID, &c.Name, &c.IsGroup, &en, &paused)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if en.Valid {
		v := en.Bool
		c.Enabled = &v
	}
	c.PausedUntil = parseTS(paused)
	return c, err
}

const chatAgentCols = `jid, name, is_group, agent_enabled, agent_paused_until`

// GetChatAgent returns a chat's agent state (ErrNotFound when the chat is not in the log).
func (s *Store) GetChatAgent(ctx context.Context, instanceID, jid string) (ChatAgent, error) {
	return scanChatAgent(s.db.QueryRowContext(ctx, `SELECT `+chatAgentCols+` FROM chats WHERE instance_id = ? AND jid = ?`,
		instanceID, jid))
}

// SetChatAgent sets a chat's override (nil = inherit), creating the chat row when needed
// (a chat can be configured before it writes). clearPause also ends any pause.
func (s *Store) SetChatAgent(ctx context.Context, instanceID, jid string, enabled *bool, clearPause bool) error {
	var en any
	if enabled != nil {
		en = *enabled
	}
	q := `INSERT INTO chats (instance_id, jid, is_group, agent_enabled) VALUES (?, ?, ?, ?)
		ON CONFLICT (instance_id, jid) DO UPDATE SET agent_enabled = excluded.agent_enabled`
	if clearPause {
		q += `, agent_paused_until = ''`
	}
	_, err := s.db.ExecContext(ctx, q, instanceID, jid, strings.HasSuffix(jid, "@g.us"), en)
	return err
}

// PauseChatAgent pauses the hook in a chat until the given time; an existing longer pause
// is kept.
func (s *Store) PauseChatAgent(ctx context.Context, instanceID, jid string, until time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO chats (instance_id, jid, is_group, agent_paused_until) VALUES (?, ?, ?, ?)
		ON CONFLICT (instance_id, jid) DO UPDATE SET agent_paused_until = MAX(chats.agent_paused_until, excluded.agent_paused_until)`,
		instanceID, jid, strings.HasSuffix(jid, "@g.us"), fmtTS(until))
	return err
}

// ListChatAgents returns the chats with an override or a pause still running at now,
// most recently active first.
func (s *Store) ListChatAgents(ctx context.Context, instanceID string, now time.Time, limit int) ([]ChatAgent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+chatAgentCols+` FROM chats
		WHERE instance_id = ? AND (agent_enabled IS NOT NULL OR agent_paused_until > ?)
		ORDER BY last_message_at DESC, jid LIMIT ?`, instanceID, fmtTS(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChatAgent{}
	for rows.Next() {
		c, err := scanChatAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AgentCall is one entry of the hook call log.
type AgentCall struct {
	ID         string
	InstanceID string
	ChatJID    string
	MessageID  string
	Status     string // ok | failed | skipped | discarded | paused
	Reason     string
	Detail     string
	HTTPStatus int
	DurationMS int64
	Replied    bool
	ReplyID    string
	Actions    string
	CreatedAt  time.Time
}

// agentCallsKept bounds the log per instance.
const agentCallsKept = 500

// InsertAgentCall logs a call and prunes the instance's log to the newest entries.
func (s *Store) InsertAgentCall(ctx context.Context, c AgentCall) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO agent_calls
		(id, instance_id, chat_jid, message_id, status, reason, detail, http_status, duration_ms, replied, reply_id, actions, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.InstanceID, c.ChatJID, c.MessageID, c.Status, c.Reason, c.Detail, c.HTTPStatus, c.DurationMS,
		c.Replied, c.ReplyID, c.Actions, fmtTS(c.CreatedAt))
	if err != nil {
		return err
	}
	seq, err := res.LastInsertId()
	if err != nil || seq%50 != 0 { // prune now and then
		return nil
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM agent_calls WHERE instance_id = ? AND seq <= (
		SELECT seq FROM agent_calls WHERE instance_id = ? ORDER BY seq DESC LIMIT 1 OFFSET ?)`,
		c.InstanceID, c.InstanceID, agentCallsKept)
	return err
}

// ListAgentCalls returns an instance's most recent calls, newest first.
func (s *Store) ListAgentCalls(ctx context.Context, instanceID string, limit int) ([]AgentCall, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, instance_id, chat_jid, message_id, status, reason, detail,
		http_status, duration_ms, replied, reply_id, actions, created_at
		FROM agent_calls WHERE instance_id = ? ORDER BY seq DESC LIMIT ?`, instanceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentCall{}
	for rows.Next() {
		var c AgentCall
		var at string
		if err := rows.Scan(&c.ID, &c.InstanceID, &c.ChatJID, &c.MessageID, &c.Status, &c.Reason, &c.Detail,
			&c.HTTPStatus, &c.DurationMS, &c.Replied, &c.ReplyID, &c.Actions, &at); err != nil {
			return nil, err
		}
		c.CreatedAt = parseTS(at)
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountAgentReplies counts the replies queued by the hook in a chat since t (loop guard).
func (s *Store) CountAgentReplies(ctx context.Context, instanceID, chatJID string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_calls
		WHERE instance_id = ? AND chat_jid = ? AND replied = 1 AND created_at >= ?`,
		instanceID, chatJID, fmtTS(since)).Scan(&n)
	return n, err
}
