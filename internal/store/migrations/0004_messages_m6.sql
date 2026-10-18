-- Marco 6: structured message content and in-place updates.
ALTER TABLE messages ADD COLUMN meta_json TEXT NOT NULL DEFAULT '';   -- poll, location, contacts, reply, mentions
ALTER TABLE messages ADD COLUMN edited_at TEXT NOT NULL DEFAULT '';   -- last edit (text replaced)
ALTER TABLE messages ADD COLUMN revoked_at TEXT NOT NULL DEFAULT '';  -- deleted for everyone (text cleared)
