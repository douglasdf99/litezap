package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Message is one entry of the message log (received, sent by the API or by the phone).
type Message struct {
	ID          string // LiteZap id (for API sends, the outbound_queue id)
	WAMessageID string
	ChatJID     string
	SenderJID   string
	FromMe      bool
	Source      string // contact | api | device | history
	Type        string
	Text        string
	MediaJSON   string
	ReactionTo  string
	Status      string // received | pending | sent | delivered | read
	Timestamp   time.Time
	MetaJSON    string    // structured content (MessageMeta)
	EditedAt    time.Time // zero: never edited
	RevokedAt   time.Time // zero: not deleted for everyone
	// Media, when set, is inserted with the message (same transaction). Not read back by queries.
	Media *Media
}

// ChatUpdate says how inserting a message changes its chat.
type ChatUpdate struct {
	Name        string // kept only when non-empty
	IsGroup     bool
	CountUnread bool // increment unread_count
	Inbound     bool // the chat has talked to us (ramp)
}

type Chat struct {
	JID           string
	Name          string
	IsGroup       bool
	LastMessageAt time.Time
	UnreadCount   int
	HasInbound    bool
	LastInboundAt time.Time // newest message received in the chat (zero if none)
	ChatState               // marco 9: archived, pinned, muted, unread mark, deleted
}

// Media is a received attachment, downloaded on demand through the worker.
type Media struct {
	ID         string
	InstanceID string
	MessageID  string
	Kind       string
	Mime       string
	Size       int64
	FileName   string
	Ref        []byte
	Path       string
	ExpiresAt  time.Time
}

const messageCols = `id, wa_message_id, chat_jid, sender_jid, from_me, source, type, text, media_json, reaction_to, status, timestamp, meta_json, edited_at, revoked_at`

// messageInsertCols are the columns written by insertMessage (edits and revokes come later).
const messageInsertCols = `id, wa_message_id, chat_jid, sender_jid, from_me, source, type, text, media_json, reaction_to, status, timestamp, meta_json`

func scanMessage(row scanner, extra ...any) (Message, error) {
	var m Message
	var ts, edited, revoked string
	dest := append([]any{&m.ID, &m.WAMessageID, &m.ChatJID, &m.SenderJID, &m.FromMe, &m.Source, &m.Type,
		&m.Text, &m.MediaJSON, &m.ReactionTo, &m.Status, &ts, &m.MetaJSON, &edited, &revoked}, extra...)
	err := row.Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	m.Timestamp, m.EditedAt, m.RevokedAt = parseTS(ts), parseTS(edited), parseTS(revoked)
	return m, err
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// insertMessage inserts m (ignoring duplicates of the same chat and WhatsApp id) and,
// when inserted, its media and (if cu is not nil) the chat update. It reports whether m was new.
func insertMessage(ctx context.Context, x execer, instanceID string, m Message, cu *ChatUpdate) (bool, error) {
	res, err := x.ExecContext(ctx, `INSERT OR IGNORE INTO messages (instance_id, `+messageInsertCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		instanceID, m.ID, m.WAMessageID, m.ChatJID, m.SenderJID, m.FromMe, m.Source, m.Type,
		m.Text, m.MediaJSON, m.ReactionTo, m.Status, fmtTS(m.Timestamp), m.MetaJSON)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if md := m.Media; md != nil {
		if _, err := x.ExecContext(ctx, `INSERT INTO media
			(id, instance_id, message_id, kind, mime, size, file_name, ref, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			md.ID, instanceID, m.ID, md.Kind, md.Mime, md.Size, md.FileName, md.Ref, now()); err != nil {
			return false, err
		}
	}
	if cu == nil {
		return true, nil
	}
	return true, upsertChat(ctx, x, instanceID, m.ChatJID, m.Timestamp, *cu)
}

// upsertChat applies a message at time at to its chat. A chat never stops being a group
// (or having talked to us), whatever a later update says.
func upsertChat(ctx context.Context, x execer, instanceID, jid string, at time.Time, cu ChatUpdate) error {
	unread := 0
	if cu.CountUnread {
		unread = 1
	}
	inboundAt := ""
	if cu.Inbound {
		inboundAt = fmtTS(at)
	}
	_, err := x.ExecContext(ctx, `INSERT INTO chats (instance_id, jid, name, is_group, last_message_at, unread_count, has_inbound, last_inbound_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (instance_id, jid) DO UPDATE SET
		  name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE chats.name END,
		  is_group = MAX(chats.is_group, excluded.is_group),
		  last_message_at = MAX(chats.last_message_at, excluded.last_message_at),
		  unread_count = chats.unread_count + excluded.unread_count,
		  has_inbound = MAX(chats.has_inbound, excluded.has_inbound),
		  last_inbound_at = MAX(chats.last_inbound_at, excluded.last_inbound_at)`,
		instanceID, jid, cu.Name, cu.IsGroup || strings.HasSuffix(jid, "@g.us"), fmtTS(at), unread, cu.Inbound, inboundAt)
	return err
}

// InsertMessage logs one message; duplicates (same chat and WhatsApp id) are ignored.
func (s *Store) InsertMessage(ctx context.Context, instanceID string, m Message, cu ChatUpdate) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	ok, err := insertMessage(ctx, tx, instanceID, m, &cu)
	if err != nil {
		return false, err
	}
	return ok, tx.Commit()
}

// InsertMessages logs a batch (e.g. one history sync conversation) in one transaction and
// returns how many were new. updates[i] applies to msgs[i].
func (s *Store) InsertMessages(ctx context.Context, instanceID string, msgs []Message, updates []ChatUpdate) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	for i, m := range msgs {
		ok, err := insertMessage(ctx, tx, instanceID, m, &updates[i])
		if err != nil {
			return 0, err
		}
		if ok {
			n++
		}
	}
	return n, tx.Commit()
}

var statusRank = map[string]int{"sent": 1, "delivered": 2, "read": 3, "played": 4}

// ApplyReceipt raises the status of our own messages (pending < sent < delivered < read < played;
// never lowers it). A receipt can beat MarkSent: the row exists as pending from MarkInFlight.
// Receipts are matched by WhatsApp id only: the receipt's chat may be a LID while the log uses the phone JID.
func (s *Store) ApplyReceipt(ctx context.Context, instanceID string, waIDs []string, status string) ([]ReceiptChange, error) {
	rank, ok := statusRank[status]
	if !ok || len(waIDs) == 0 {
		return nil, nil
	}
	args := []any{status, instanceID}
	for _, id := range waIDs {
		args = append(args, id)
	}
	args = append(args, rank)
	rows, err := s.db.QueryContext(ctx, `UPDATE messages SET status = ?
		WHERE instance_id = ? AND from_me = 1 AND wa_message_id IN (?`+strings.Repeat(",?", len(waIDs)-1)+`)
		AND (CASE status WHEN 'sent' THEN 1 WHEN 'delivered' THEN 2 WHEN 'read' THEN 3 WHEN 'played' THEN 4 ELSE 0 END) < ?
		RETURNING id, wa_message_id, chat_jid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReceiptChange
	for rows.Next() {
		var c ReceiptChange
		if err := rows.Scan(&c.ID, &c.WAMessageID, &c.ChatJID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReceiptChange is a message whose status a receipt raised.
type ReceiptChange struct {
	ID          string
	WAMessageID string
	ChatJID     string
}

func (s *Store) GetMessage(ctx context.Context, instanceID, id string) (Message, error) {
	return scanMessage(s.db.QueryRowContext(ctx,
		`SELECT `+messageCols+` FROM messages WHERE instance_id = ? AND id = ?`, instanceID, id))
}

// MessageByWAID finds a message of a chat by its WhatsApp id.
func (s *Store) MessageByWAID(ctx context.Context, instanceID, chatJID, waID string) (Message, error) {
	return scanMessage(s.db.QueryRowContext(ctx,
		`SELECT `+messageCols+` FROM messages WHERE instance_id = ? AND chat_jid = ? AND wa_message_id = ?`,
		instanceID, chatJID, waID))
}

// EditMessage replaces the text of a logged message (an edit from the API or received)
// and returns the updated row; ErrNotFound when the message is not in the log.
func (s *Store) EditMessage(ctx context.Context, instanceID, chatJID, waID, text string, at time.Time) (Message, error) {
	return scanMessage(s.db.QueryRowContext(ctx, `UPDATE messages SET text = ?, edited_at = ?
		WHERE instance_id = ? AND chat_jid = ? AND wa_message_id = ? AND revoked_at = ''
		RETURNING `+messageCols, text, fmtTS(at), instanceID, chatJID, waID))
}

// RevokeMessage marks a logged message as deleted for everyone (its text is cleared) and
// returns it; ErrNotFound when it is not in the log or was already revoked.
func (s *Store) RevokeMessage(ctx context.Context, instanceID, chatJID, waID string, at time.Time) (Message, error) {
	return scanMessage(s.db.QueryRowContext(ctx, `UPDATE messages SET text = '', revoked_at = ?
		WHERE instance_id = ? AND chat_jid = ? AND wa_message_id = ? AND revoked_at = ''
		RETURNING `+messageCols, fmtTS(at), instanceID, chatJID, waID))
}

// MessageCursor points at the last message of a page (newest-first order).
type MessageCursor struct {
	Timestamp time.Time
	Seq       int64
}

// ListMessages returns a chat's messages newest first, strictly older than before (if set),
// plus the cursor of the last returned row.
func (s *Store) ListMessages(ctx context.Context, instanceID, chatJID string, limit int, before *MessageCursor) ([]Message, MessageCursor, error) {
	q := `SELECT ` + messageCols + `, seq FROM messages WHERE instance_id = ? AND chat_jid = ?`
	args := []any{instanceID, chatJID}
	if before != nil {
		q += ` AND (timestamp < ? OR (timestamp = ? AND seq < ?))`
		ts := fmtTS(before.Timestamp)
		args = append(args, ts, ts, before.Seq)
	}
	q += ` ORDER BY timestamp DESC, seq DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, MessageCursor{}, err
	}
	defer rows.Close()
	out := []Message{}
	var cur MessageCursor
	for rows.Next() {
		m, err := scanMessage(rows, &cur.Seq)
		if err != nil {
			return nil, MessageCursor{}, err
		}
		cur.Timestamp = m.Timestamp
		out = append(out, m)
	}
	return out, cur, rows.Err()
}

// ChatCursor points at the last chat of a page (most recent first).
type ChatCursor struct {
	LastMessageAt time.Time
	JID           string
}

// ListChats returns chats ordered by last message (most recent first), after the cursor if set.
func (s *Store) ListChats(ctx context.Context, instanceID string, limit int, after *ChatCursor) ([]Chat, ChatCursor, error) {
	q := `SELECT jid, name, is_group, last_message_at, unread_count, has_inbound, ` + chatStateCols + ` FROM chats
		WHERE instance_id = ? AND (deleted_at = '' OR deleted_at < last_message_at)`
	args := []any{instanceID}
	if after != nil {
		q += ` AND (last_message_at < ? OR (last_message_at = ? AND jid < ?))`
		ts := fmtTS(after.LastMessageAt)
		args = append(args, ts, ts, after.JID)
	}
	q += ` ORDER BY last_message_at DESC, jid DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, ChatCursor{}, err
	}
	defer rows.Close()
	out := []Chat{}
	var cur ChatCursor
	for rows.Next() {
		var c Chat
		var ts string
		var st chatStateScan
		if err := rows.Scan(append([]any{&c.JID, &c.Name, &c.IsGroup, &ts, &c.UnreadCount, &c.HasInbound}, st.dest()...)...); err != nil {
			return nil, ChatCursor{}, err
		}
		c.LastMessageAt, c.ChatState = parseTS(ts), st.state()
		cur = ChatCursor{LastMessageAt: c.LastMessageAt, JID: c.JID}
		out = append(out, c)
	}
	return out, cur, rows.Err()
}

func (s *Store) GetChat(ctx context.Context, instanceID, jid string) (Chat, error) {
	return getChat(ctx, s.db, instanceID, jid)
}

// LatestInbound returns up to limit of the newest messages received in a chat (newest first).
func (s *Store) LatestInbound(ctx context.Context, instanceID, chatJID string, limit int) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+messageCols+` FROM messages
		WHERE instance_id = ? AND chat_jid = ? AND from_me = 0 AND type <> 'reaction'
		ORDER BY timestamp DESC, seq DESC LIMIT ?`, instanceID, chatJID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) ResetUnread(ctx context.Context, instanceID, chatJID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE chats SET unread_count = 0 WHERE instance_id = ? AND jid = ?`, instanceID, chatJID)
	return err
}

func (s *Store) GetMedia(ctx context.Context, instanceID, id string) (Media, error) {
	var m Media
	var exp string
	err := s.db.QueryRowContext(ctx, `SELECT id, instance_id, message_id, kind, mime, size, file_name, ref, path, expires_at
		FROM media WHERE instance_id = ? AND id = ?`, instanceID, id).
		Scan(&m.ID, &m.InstanceID, &m.MessageID, &m.Kind, &m.Mime, &m.Size, &m.FileName, &m.Ref, &m.Path, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	m.ExpiresAt = parseTS(exp)
	return m, err
}

// SetMediaFile records the local copy of a downloaded attachment.
func (s *Store) SetMediaFile(ctx context.Context, id, path string, expires time.Time) error {
	return s.exec1(ctx, `UPDATE media SET path = ?, expires_at = ? WHERE id = ?`, path, fmtTS(expires), id)
}

// MediaInfo is the JSON stored in messages.media_json (and shown by the API).
type MediaInfo struct {
	ID       string `json:"id,omitempty"` // media table id; empty for media we sent
	Kind     string `json:"kind"`
	Mime     string `json:"mime,omitempty"`
	Size     int64  `json:"size,omitempty"`
	FileName string `json:"file_name,omitempty"`
	Seconds  int    `json:"seconds,omitempty"`
	PTT      bool   `json:"ptt,omitempty"`
	ViewOnce bool   `json:"view_once,omitempty"` // received as view-once (never forwarded)
}
