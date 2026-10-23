package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// MutedForever is the muted_until of a chat muted without an end (whatsmeow's value).
var MutedForever = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)

// ChatState is a chat's state in the app state (marco 9), as last seen from any device.
type ChatState struct {
	Archived     bool
	Pinned       bool
	MutedUntil   time.Time // zero: not muted; MutedForever: no end
	MarkedUnread bool      // marked as unread by hand
	DeletedAt    time.Time // deleted (hidden from lists until a newer message)
}

// Muted reports whether the chat is muted at now.
func (s ChatState) Muted(now time.Time) bool {
	return !s.MutedUntil.IsZero() && s.MutedUntil.After(now)
}

// equal compares states at storage precision (times as stored, whatever their location).
func (s ChatState) equal(o ChatState) bool {
	return s.Archived == o.Archived && s.Pinned == o.Pinned && s.MarkedUnread == o.MarkedUnread &&
		fmtTS(s.MutedUntil) == fmtTS(o.MutedUntil) && fmtTS(s.DeletedAt) == fmtTS(o.DeletedAt)
}

const chatStateCols = `archived, pinned, muted_until, marked_unread, deleted_at`

type chatStateScan struct {
	archived, pinned, unread bool
	muted, deleted           string
}

func (c *chatStateScan) dest() []any {
	return []any{&c.archived, &c.pinned, &c.muted, &c.unread, &c.deleted}
}

func (c *chatStateScan) state() ChatState {
	return ChatState{Archived: c.archived, Pinned: c.pinned, MutedUntil: parseTS(c.muted),
		MarkedUnread: c.unread, DeletedAt: parseTS(c.deleted)}
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getChat(ctx context.Context, q querier, instanceID, jid string) (Chat, error) {
	var c Chat
	var ts, inbound string
	var st chatStateScan
	err := q.QueryRowContext(ctx, `SELECT jid, name, is_group, last_message_at, unread_count, has_inbound, last_inbound_at, `+
		chatStateCols+` FROM chats WHERE instance_id = ? AND jid = ?`, instanceID, jid).
		Scan(append([]any{&c.JID, &c.Name, &c.IsGroup, &ts, &c.UnreadCount, &c.HasInbound, &inbound}, st.dest()...)...)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.LastMessageAt, c.LastInboundAt, c.ChatState = parseTS(ts), parseTS(inbound), st.state()
	return c, err
}

// ChatChange is one app state change of a chat. Field: archived | pinned | muted |
// unread | deleted. Muted uses MutedUntil (zero: unmuted).
type ChatChange struct {
	Field      string
	Value      bool
	MutedUntil time.Time
	At         time.Time // when it happened (deleted_at); zero: now
}

// ApplyChatChange applies c to a chat of the log and reports whether its state changed
// (an echo of a change already applied changes nothing). ErrNotFound when the chat is
// not in the log.
func (s *Store) ApplyChatChange(ctx context.Context, instanceID, jid string, c ChatChange) (Chat, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Chat{}, false, err
	}
	defer tx.Rollback()
	cur, err := getChat(ctx, tx, instanceID, jid)
	if err != nil {
		return Chat{}, false, err
	}
	next := cur
	switch c.Field {
	case "archived":
		next.Archived = c.Value
		if c.Value {
			next.Pinned = false // archiving unpins (WhatsApp does the same)
		}
	case "pinned":
		next.Pinned = c.Value
	case "muted":
		next.MutedUntil = time.Time{}
		if c.Value {
			next.MutedUntil = c.MutedUntil
			if next.MutedUntil.IsZero() {
				next.MutedUntil = MutedForever
			}
		}
	case "unread":
		next.MarkedUnread = c.Value
		if !c.Value {
			next.UnreadCount = 0 // read on some device
		}
	case "deleted":
		if !cur.DeletedAt.IsZero() && !cur.DeletedAt.Before(cur.LastMessageAt) {
			return cur, false, nil // already hidden: the app state echo of our delete
		}
		at := c.At
		if at.IsZero() {
			at = time.Now()
		}
		next.DeletedAt, next.UnreadCount, next.MarkedUnread, next.Pinned = at, 0, false, false
	default:
		return Chat{}, false, fmt.Errorf("unknown chat field %q", c.Field)
	}
	if next.ChatState.equal(cur.ChatState) && next.UnreadCount == cur.UnreadCount {
		return cur, false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE chats SET archived = ?, pinned = ?, muted_until = ?, marked_unread = ?,
		deleted_at = ?, unread_count = ? WHERE instance_id = ? AND jid = ?`,
		next.Archived, next.Pinned, fmtTS(next.MutedUntil), next.MarkedUnread, fmtTS(next.DeletedAt), next.UnreadCount,
		instanceID, jid); err != nil {
		return Chat{}, false, err
	}
	return next, true, tx.Commit()
}

// PinnedChats counts the instance's pinned chats (WhatsApp allows 3).
func (s *Store) PinnedChats(ctx context.Context, instanceID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chats WHERE instance_id = ? AND pinned = 1`, instanceID).Scan(&n)
	return n, err
}

// LastChatMessage returns the newest message of a chat (ErrNotFound if it has none).
func (s *Store) LastChatMessage(ctx context.Context, instanceID, chatJID string) (Message, error) {
	return scanMessage(s.db.QueryRowContext(ctx, `SELECT `+messageCols+` FROM messages
		WHERE instance_id = ? AND chat_jid = ? AND type NOT IN ('reaction')
		ORDER BY timestamp DESC, seq DESC LIMIT 1`, instanceID, chatJID))
}

// SetStarred marks a logged message as starred (meta "starred") and reports whether it
// changed; ErrNotFound when the message is not in the log.
func (s *Store) SetStarred(ctx context.Context, instanceID, chatJID, waID string, starred bool) (bool, error) {
	expr := `json_remove(CASE WHEN meta_json = '' THEN '{}' ELSE meta_json END, '$.starred')`
	cond := `json_extract(CASE WHEN meta_json = '' THEN '{}' ELSE meta_json END, '$.starred') IS NOT NULL`
	if starred {
		expr = `json_set(CASE WHEN meta_json = '' THEN '{}' ELSE meta_json END, '$.starred', json('true'))`
		cond = `json_extract(CASE WHEN meta_json = '' THEN '{}' ELSE meta_json END, '$.starred') IS NULL`
	}
	res, err := s.db.ExecContext(ctx, `UPDATE messages SET meta_json = `+expr+`
		WHERE instance_id = ? AND chat_jid = ? AND wa_message_id = ? AND `+cond, instanceID, chatJID, waID)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return true, nil
	}
	var one int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM messages WHERE instance_id = ? AND chat_jid = ? AND wa_message_id = ?`,
		instanceID, chatJID, waID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return false, err
}

// ClearChatMessages deletes a chat's messages up to upTo (the newest message cleared, on
// WhatsApp's clock) with their media rows and files, as "clear chat" does on the phone,
// and resets the chat's unread counters; the chat itself stays. Messages still being sent
// (pending) are kept, and so are starred ones when keepStarred is set. ErrNotFound when
// the chat is not in the log.
func (s *Store) ClearChatMessages(ctx context.Context, instanceID, jid string, upTo time.Time, keepStarred bool) (int64, error) {
	if _, err := s.GetChat(ctx, instanceID, jid); err != nil {
		return 0, err
	}
	q := `SELECT seq, id FROM messages WHERE instance_id = ? AND chat_jid = ? AND timestamp <= ? AND status <> 'pending'`
	if keepStarred {
		q += ` AND json_extract(CASE WHEN meta_json = '' THEN '{}' ELSE meta_json END, '$.starred') IS NULL`
	}
	q += ` LIMIT ?`
	var total int64
	for {
		n, err := s.deleteMessageBatch(ctx, q, instanceID, jid, fmtTS(upTo), pruneBatch)
		total += n
		if err != nil {
			return total, err
		}
		if n < pruneBatch {
			break
		}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE chats SET unread_count = 0, marked_unread = 0 WHERE instance_id = ? AND jid = ?`,
		instanceID, jid)
	return total, err
}
