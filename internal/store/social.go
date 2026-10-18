package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// StatusChat is the chat of status (story) posts: received ones are logged there and API
// posts are queued to it. It is kept out of the chat list.
const StatusChat = "status@broadcast"

// Label is a WhatsApp Business label (marco 10), as last synced from the app state.
type Label struct {
	ID           string
	Name         string
	Color        int
	PredefinedID int
	Deleted      bool
	UpdatedAt    time.Time
	Chats        []string // chats with this label (ListLabels and GetLabel only)
}

// UpsertLabel stores a label edit and reports whether anything changed (an echo of an
// edit already applied changes nothing). A deleted label loses its associations.
func (s *Store) UpsertLabel(ctx context.Context, instanceID string, l Label) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var name string
	var color, pre int
	var deleted bool
	err = tx.QueryRowContext(ctx, `SELECT name, color, predefined_id, deleted FROM labels WHERE instance_id = ? AND label_id = ?`,
		instanceID, l.ID).Scan(&name, &color, &pre, &deleted)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if l.Deleted {
			return false, nil // deleting a label we never knew
		}
	case err != nil:
		return false, err
	case name == l.Name && color == l.Color && pre == l.PredefinedID && deleted == l.Deleted:
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO labels (instance_id, label_id, name, color, predefined_id, deleted, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (instance_id, label_id) DO UPDATE SET name = excluded.name,
		color = excluded.color, predefined_id = excluded.predefined_id, deleted = excluded.deleted, updated_at = excluded.updated_at`,
		instanceID, l.ID, l.Name, l.Color, l.PredefinedID, l.Deleted, now()); err != nil {
		return false, err
	}
	if l.Deleted {
		for _, q := range []string{`DELETE FROM label_chats WHERE instance_id = ? AND label_id = ?`,
			`DELETE FROM label_messages WHERE instance_id = ? AND label_id = ?`} {
			if _, err := tx.ExecContext(ctx, q, instanceID, l.ID); err != nil {
				return false, err
			}
		}
	}
	return true, tx.Commit()
}

const labelCols = `label_id, name, color, predefined_id, deleted, updated_at`

func scanLabel(sc interface{ Scan(...any) error }) (Label, error) {
	var l Label
	var ts string
	err := sc.Scan(&l.ID, &l.Name, &l.Color, &l.PredefinedID, &l.Deleted, &ts)
	l.UpdatedAt = parseTS(ts)
	return l, err
}

// ListLabels returns the instance's labels that are not deleted, with their chats,
// ordered by id (numeric when possible).
func (s *Store) ListLabels(ctx context.Context, instanceID string) ([]Label, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+labelCols+` FROM labels WHERE instance_id = ? AND deleted = 0
		ORDER BY CAST(label_id AS INTEGER), label_id`, instanceID)
	if err != nil {
		return nil, err
	}
	out := []Label{}
	for rows.Next() {
		l, err := scanLabel(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	chats, err := s.labelChats(ctx, instanceID, "")
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Chats = chats[out[i].ID]
	}
	return out, nil
}

// GetLabel returns a label that is not deleted (ErrNotFound otherwise), with its chats.
func (s *Store) GetLabel(ctx context.Context, instanceID, id string) (Label, error) {
	l, err := scanLabel(s.db.QueryRowContext(ctx, `SELECT `+labelCols+` FROM labels
		WHERE instance_id = ? AND label_id = ? AND deleted = 0`, instanceID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	if err != nil {
		return l, err
	}
	chats, err := s.labelChats(ctx, instanceID, id)
	l.Chats = chats[id]
	return l, err
}

func (s *Store) labelChats(ctx context.Context, instanceID, id string) (map[string][]string, error) {
	q := `SELECT label_id, chat_jid FROM label_chats WHERE instance_id = ?`
	args := []any{instanceID}
	if id != "" {
		q += ` AND label_id = ?`
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY chat_jid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var l, c string
		if err := rows.Scan(&l, &c); err != nil {
			return nil, err
		}
		out[l] = append(out[l], c)
	}
	return out, rows.Err()
}

// ChatLabels returns the ids of the labels of a chat.
func (s *Store) ChatLabels(ctx context.Context, instanceID, chat string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT lc.label_id FROM label_chats lc JOIN labels l
		ON l.instance_id = lc.instance_id AND l.label_id = lc.label_id AND l.deleted = 0
		WHERE lc.instance_id = ? AND lc.chat_jid = ? ORDER BY CAST(lc.label_id AS INTEGER)`, instanceID, chat)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetLabelChat labels or unlabels a chat and reports whether it changed.
func (s *Store) SetLabelChat(ctx context.Context, instanceID, labelID, chat string, labeled bool) (bool, error) {
	q := `DELETE FROM label_chats WHERE instance_id = ? AND label_id = ? AND chat_jid = ?`
	if labeled {
		q = `INSERT INTO label_chats (instance_id, label_id, chat_jid) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`
	}
	res, err := s.db.ExecContext(ctx, q, instanceID, labelID, chat)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetLabelMessage labels or unlabels a message (WhatsApp id) and reports whether it changed.
func (s *Store) SetLabelMessage(ctx context.Context, instanceID, labelID, chat, waID string, labeled bool) (bool, error) {
	q := `DELETE FROM label_messages WHERE instance_id = ? AND label_id = ? AND chat_jid = ? AND wa_message_id = ?`
	if labeled {
		q = `INSERT INTO label_messages (instance_id, label_id, chat_jid, wa_message_id) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`
	}
	res, err := s.db.ExecContext(ctx, q, instanceID, labelID, chat, waID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// NextLabelID is the id for a new label: one more than the largest numeric id ever seen
// (deleted ones included: WhatsApp does not reuse them).
func (s *Store) NextLabelID(ctx context.Context, instanceID string) (string, error) {
	var maxID sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(CAST(label_id AS INTEGER)) FROM labels WHERE instance_id = ?`, instanceID).Scan(&maxID)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(max(maxID.Int64, 0)+1, 10), nil
}

// CountOutboundSince counts the instance's queued, in-flight and sent items to chat
// created since t (failed ones are not counted).
func (s *Store) CountOutboundSince(ctx context.Context, instanceID, chat string, t time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbound_queue WHERE instance_id = ? AND chat_jid = ?
		AND status <> 'failed' AND created_at >= ?`, instanceID, chat, fmtTS(t)).Scan(&n)
	return n, err
}
