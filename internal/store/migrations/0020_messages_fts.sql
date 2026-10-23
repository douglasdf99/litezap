-- v0.1.1 (CHT-11): full-text search over the message log (text, captions and document
-- file names). The index is a separate FTS5 table keyed by messages.seq and kept in sync
-- by triggers, so deletes (retention, clear chat, instance removal) and edits/revokes
-- follow. Rows that existed before this migration are indexed in the background, newest
-- first, from the seq recorded in engine_kv (key messages_fts_backfill) down to 1.
CREATE VIRTUAL TABLE messages_fts USING fts5(text, file_name, tokenize = 'unicode61 remove_diacritics 2');

CREATE TRIGGER messages_fts_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts (rowid, text, file_name) VALUES (new.seq, new.text,
    CASE WHEN json_valid(new.media_json) THEN COALESCE(json_extract(new.media_json, '$.file_name'), '') ELSE '' END);
END;

CREATE TRIGGER messages_fts_ad AFTER DELETE ON messages BEGIN
  DELETE FROM messages_fts WHERE rowid = old.seq;
END;

CREATE TRIGGER messages_fts_au AFTER UPDATE OF text, media_json ON messages BEGIN
  DELETE FROM messages_fts WHERE rowid = old.seq;
  INSERT INTO messages_fts (rowid, text, file_name) VALUES (new.seq, new.text,
    CASE WHEN json_valid(new.media_json) THEN COALESCE(json_extract(new.media_json, '$.file_name'), '') ELSE '' END);
END;

INSERT INTO engine_kv (key, value, updated_at)
  SELECT 'messages_fts_backfill', CAST(top AS TEXT), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
  FROM (SELECT MAX(seq) AS top FROM messages) WHERE top IS NOT NULL;
