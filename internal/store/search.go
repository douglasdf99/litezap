package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
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

// maxBackfillBackoff bounds the wait between retries of a failed backfill batch.
const maxBackfillBackoff = time.Minute

// BackfillSearchIndex indexes the rows older than the search index in small batches
// with a pause between them, so startup and the other writers (one shared connection)
// are never held up. A failed batch is retried with a growing wait (pause doubled each
// time, up to a minute); onErr, when set, sees each failure. It returns when done or when
// ctx ends.
func (s *Store) BackfillSearchIndex(ctx context.Context, batch int64, pause time.Duration, onErr func(error)) {
	wait := pause
	for {
		more, err := s.BackfillSearchBatch(ctx, batch)
		switch {
		case err != nil && ctx.Err() != nil:
			return
		case err != nil:
			if onErr != nil {
				onErr(err)
			}
			wait = min(max(wait*2, pause), maxBackfillBackoff)
		case !more:
			return
		default:
			wait = pause
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
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
	// IncludeDeleted also searches messages of chats the user deleted (by default the
	// messages up to a chat's deletion are left out, as GET /chats hides the chat).
	IncludeDeleted bool
}

// SearchHit is a message matching a search, with a short snippet around the match.
type SearchHit struct {
	Message
	Snippet string
}

// MatchExpr turns free text into a safe FTS5 expression: every word must appear (accents
// and case ignored); words of 2 or more characters match as a prefix, a single character
// only as a whole word (a one-letter prefix would match most of the log). Operators and
// quotes typed by the user are taken literally; combining marks (decomposed accents) stay
// inside their word. Empty when the text has no word.
func MatchExpr(text string) string {
	words := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r)
	})
	if len(words) > 16 {
		words = words[:16]
	}
	parts := make([]string, 0, len(words))
	for _, w := range words {
		p := `"` + w + `"`
		if utf8.RuneCountInString(strings.TrimFunc(w, unicode.IsMark)) >= 2 {
			p += "*"
		}
		parts = append(parts, p)
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
	if !q.IncludeDeleted {
		sqlq += ` AND NOT EXISTS (SELECT 1 FROM chats c WHERE c.instance_id = m.instance_id AND c.jid = m.chat_jid
			AND c.deleted_at <> '' AND m.timestamp <= c.deleted_at)`
	}
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
