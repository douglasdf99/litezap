ALTER TABLE instances ADD COLUMN settings_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE instances ADD COLUMN first_connected_at TEXT NOT NULL DEFAULT '';

CREATE TABLE chats (
  instance_id        TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  jid                TEXT NOT NULL,
  name               TEXT NOT NULL DEFAULT '',
  is_group           INTEGER NOT NULL DEFAULT 0,
  last_message_at    TEXT NOT NULL DEFAULT '',
  unread_count       INTEGER NOT NULL DEFAULT 0,
  has_inbound        INTEGER NOT NULL DEFAULT 0, -- received at least one message (ramp: not a new recipient)
  last_inbound_at    TEXT NOT NULL DEFAULT '',   -- newest received message (send window: active conversation)
  agent_enabled      INTEGER,                    -- marco 5 (agent hook)
  agent_paused_until TEXT NOT NULL DEFAULT '',   -- marco 5 (agent hook)
  PRIMARY KEY (instance_id, jid)
);
CREATE INDEX chats_recent ON chats (instance_id, last_message_at DESC, jid DESC);

CREATE TABLE messages (
  seq           INTEGER PRIMARY KEY AUTOINCREMENT,
  id            TEXT NOT NULL UNIQUE,
  instance_id   TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  wa_message_id TEXT NOT NULL,
  chat_jid      TEXT NOT NULL,
  sender_jid    TEXT NOT NULL DEFAULT '',
  from_me       INTEGER NOT NULL,
  source        TEXT NOT NULL,              -- contact | api | device | history
  type          TEXT NOT NULL,
  text          TEXT NOT NULL DEFAULT '',
  media_json    TEXT NOT NULL DEFAULT '',
  reaction_to   TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL,              -- received | pending | sent | delivered | read
  timestamp     TEXT NOT NULL,
  UNIQUE (instance_id, chat_jid, wa_message_id)
);
CREATE INDEX messages_chat ON messages (instance_id, chat_jid, timestamp DESC, seq DESC);
CREATE INDEX messages_wa ON messages (instance_id, wa_message_id);

CREATE TABLE outbound_queue (
  seq               INTEGER PRIMARY KEY AUTOINCREMENT,
  id                TEXT NOT NULL UNIQUE,
  instance_id       TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  client_message_id TEXT,                    -- NULL when absent; UNIQUE ignores NULLs
  chat_jid          TEXT NOT NULL,
  wa_message_id     TEXT NOT NULL,           -- chosen at enqueue, reused on every attempt
  payload_json      TEXT NOT NULL,
  status            TEXT NOT NULL,           -- queued | in_flight | sent | failed
  attempts          INTEGER NOT NULL DEFAULT 0,
  next_attempt_at   TEXT NOT NULL,
  last_error        TEXT NOT NULL DEFAULT '',
  new_recipient     INTEGER NOT NULL DEFAULT 0,
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL,
  sent_at           TEXT NOT NULL DEFAULT '',
  expires_at        TEXT NOT NULL DEFAULT '', -- not sent by then: failed with "expired"
  request_hash      TEXT NOT NULL DEFAULT '', -- client_message_id reuse with another body is a conflict
  lookup_failures   INTEGER NOT NULL DEFAULT 0, -- consecutive failed number lookups (5 = failed)
  UNIQUE (instance_id, client_message_id)
);
CREATE INDEX outbound_active ON outbound_queue (instance_id, chat_jid, seq) WHERE status IN ('queued', 'in_flight');
CREATE INDEX outbound_queued ON outbound_queue (instance_id, seq) WHERE status = 'queued';
CREATE INDEX outbound_wa ON outbound_queue (instance_id, wa_message_id);
CREATE INDEX outbound_ramp ON outbound_queue (instance_id, sent_at) WHERE new_recipient = 1;

CREATE TABLE media (
  id          TEXT PRIMARY KEY,
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  message_id  TEXT NOT NULL,
  kind        TEXT NOT NULL,                 -- image | video | audio | document | sticker
  mime        TEXT NOT NULL DEFAULT '',
  size        INTEGER NOT NULL DEFAULT 0,
  file_name   TEXT NOT NULL DEFAULT '',
  ref         BLOB NOT NULL,                 -- opaque download reference from the worker
  path        TEXT NOT NULL DEFAULT '',      -- local copy, once downloaded
  expires_at  TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL
);

-- Phone number -> WhatsApp JID lookups (IsOnWhatsApp), cached to avoid repeated queries.
CREATE TABLE number_cache (
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  phone       TEXT NOT NULL,
  found       INTEGER NOT NULL,
  jid         TEXT NOT NULL DEFAULT '',
  checked_at  TEXT NOT NULL,
  PRIMARY KEY (instance_id, phone)
);
