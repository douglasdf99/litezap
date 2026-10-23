package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Full-text search over the message log (CHT-11): the FTS5 table messages_fts (migration
// 0020) indexes the text (body, caption) and the document file name of every message.

// ftsBackfillKey (engine_kv) is the highest seq of the rows older than the index still
// to be indexed; absent when the index is complete.
const ftsBackfillKey = "messages_fts_backfill"

const ftsFileName = `CASE WHEN json_valid(media_json) THEN COALESCE(json_extract(media_json, '$.file_name'), '') ELSE '' END`

// SearchIndexComplete reports whether every message is indexed (false while the
// background backfill of the rows that existed before the index runs).
func (s *Store) SearchIndexComplete(ctx context.Context) (bool, error) {
	_, err := s.GetKV(ctx, ftsBackfillKey)
	if errors.Is(err, ErrNotFound) {
		return true, nil
	}
	return false, err
}

// BackfillSearchBatch indexes up to batch of the older rows still missing from the index,
// newest first, in one short transaction. It reports whether rows remain.
func (s *Store) BackfillSearchBatch(ctx context.Context, batch int64) (bool, error) {
	raw, err := s.GetKV(ctx, ftsBackfillKey)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	upto, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || upto <= 0 {
		return false, s.DeleteKV(ctx, ftsBackfillKey)
	}
	lo := max(upto-batch, 0)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	// A row edited since the migration was already re-indexed by the update trigger.
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages_fts (rowid, text, file_name)
		SELECT seq, text, `+ftsFileName+` FROM messages WHERE seq > ? AND seq <= ?
		AND seq NOT IN (SELECT rowid FROM messages_fts WHERE rowid > ? AND rowid <= ?)`, lo, upto, lo, upto); err != nil {
		return false, err
	}
	if lo == 0 {
		_, err = tx.ExecContext(ctx, `DELETE FROM engine_kv WHERE key = ?`, ftsBackfillKey)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE engine_kv SET value = ?, updated_at = ? WHERE key = ?`,
			strconv.FormatInt(lo, 10), now(), ftsBackfillKey)
	}
	if err != nil {
		return false, err
	}
	return lo > 0, tx.Commit()
}

// BackfillSearchIndex indexes the rows older than the search index in small batches
// with a pause between them, so startup and the other writers (one shared connection)
// are never held up. It returns when done or when ctx ends.
func (s *Store) BackfillSearchIndex(ctx context.Context, batch int64, pause time.Duration) error {
	for {
		more, err := s.BackfillSearchBatch(ctx, batch)
		if err != nil || !more {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(pause):
		}
	}
}

// SearchQuery filters a message search. Text is the user's query; the other fields are
// optional.
type SearchQuery struct {
	Text   string
	Chat   string    // chat JID
	Sender string    // sender JID
	FromMe *bool     // only ours (true) or only received (false)
	After  time.Time // strictly after
	Before time.Time // strictly before
	Limit  int
	Cursor *MessageCursor // continue after this row (newest-first order)
}

// SearchHit is a message matching a search, with a short snippet around the match.
type SearchHit struct {
	Message
	Snippet string
}

// MatchExpr turns free text into a safe FTS5 expression: every word must appear (as a
// prefix, accents and case ignored); operators and quotes typed by the user are taken
// literally. Empty when the text has no word.
func MatchExpr(text string) string {
	words := strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(words) > 16 {
		words = words[:16]
	}
	parts := make([]string, 0, len(words))
	for _, w := range words {
		parts = append(parts, `"`+w+`"*`)
	}
	return strings.Join(parts, " ")
}

// SearchMessages returns an instance's messages matching q, newest first, and the cursor
// of the last row. Revoked messages and reactions are left out.
func (s *Store) SearchMessages(ctx context.Context, instanceID string, q SearchQuery) ([]SearchHit, MessageCursor, error) {
	expr := MatchExpr(q.Text)
	if expr == "" {
		return []SearchHit{}, MessageCursor{}, nil
	}
	cols := strings.ReplaceAll(`m.`+messageCols, ", ", ", m.")
	sqlq := `SELECT ` + cols + `, m.seq, snippet(messages_fts, -1, '«', '»', '…', 12)
		FROM messages_fts JOIN messages m ON m.seq = messages_fts.rowid
		WHERE messages_fts MATCH ? AND m.instance_id = ? AND m.revoked_at = '' AND m.type <> 'reaction'`
	args := []any{expr, instanceID}
	if q.Chat != "" {
		sqlq += ` AND m.chat_jid = ?`
		args = append(args, q.Chat)
	}
	if q.Sender != "" {
		sqlq += ` AND m.sender_jid = ?`
		args = append(args, q.Sender)
	}
	if q.FromMe != nil {
		sqlq += ` AND m.from_me = ?`
		args = append(args, *q.FromMe)
	}
	if !q.After.IsZero() {
		sqlq += ` AND m.timestamp > ?`
		args = append(args, fmtTS(q.After))
	}
	if !q.Before.IsZero() {
		sqlq += ` AND m.timestamp < ?`
		args = append(args, fmtTS(q.Before))
	}
	if c := q.Cursor; c != nil {
		ts := fmtTS(c.Timestamp)
		sqlq += ` AND (m.timestamp < ? OR (m.timestamp = ? AND m.seq < ?))`
		args = append(args, ts, ts, c.Seq)
	}
	sqlq += ` ORDER BY m.timestamp DESC, m.seq DESC LIMIT ?`
	args = append(args, q.Limit)
	rows, err := s.db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, MessageCursor{}, err
	}
	defer rows.Close()
	out := []SearchHit{}
	var cur MessageCursor
	for rows.Next() {
		var h SearchHit
		m, err := scanMessage(rows, &cur.Seq, &h.Snippet)
		if err != nil {
			return nil, MessageCursor{}, err
		}
		h.Message, cur.Timestamp = m, m.Timestamp
		out = append(out, h)
	}
	return out, cur, rows.Err()
}
