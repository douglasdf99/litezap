-- Marco 10: WhatsApp Business labels as synced from the app state (any device).
CREATE TABLE labels (
  instance_id   TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  label_id      TEXT NOT NULL,
  name          TEXT NOT NULL DEFAULT '',
  color         INTEGER NOT NULL DEFAULT 0,
  predefined_id INTEGER NOT NULL DEFAULT 0,
  deleted       INTEGER NOT NULL DEFAULT 0,
  updated_at    TEXT NOT NULL,
  PRIMARY KEY (instance_id, label_id)
);
CREATE TABLE label_chats (
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  label_id    TEXT NOT NULL,
  chat_jid    TEXT NOT NULL,
  PRIMARY KEY (instance_id, label_id, chat_jid)
);
CREATE INDEX label_chats_by_chat ON label_chats (instance_id, chat_jid);
CREATE TABLE label_messages (
  instance_id   TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  label_id      TEXT NOT NULL,
  chat_jid      TEXT NOT NULL,
  wa_message_id TEXT NOT NULL,
  PRIMARY KEY (instance_id, label_id, chat_jid, wa_message_id)
);
