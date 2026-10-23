-- v0.1.1 (CHT-9): a chat's disappearing messages timer, as last learned (our change, the
-- contact's or another device's, a group update, or the expiration carried by a message).
ALTER TABLE chats ADD COLUMN disappearing_seconds INTEGER NOT NULL DEFAULT 0; -- 0: off
ALTER TABLE chats ADD COLUMN disappearing_at TEXT NOT NULL DEFAULT '';        -- when it was set; '' unknown
