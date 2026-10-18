-- Marco 9: chat state synced with the app state (archive, pin, mute, unread mark, delete).
ALTER TABLE chats ADD COLUMN archived INTEGER NOT NULL DEFAULT 0;
ALTER TABLE chats ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0;
ALTER TABLE chats ADD COLUMN muted_until TEXT NOT NULL DEFAULT '';   -- '' not muted; 9999-12-31 forever
ALTER TABLE chats ADD COLUMN marked_unread INTEGER NOT NULL DEFAULT 0;
ALTER TABLE chats ADD COLUMN deleted_at TEXT NOT NULL DEFAULT '';    -- hidden from lists until a newer message
