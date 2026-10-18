-- Marco 5: agent hook (spec 4.3). chats.agent_enabled / agent_paused_until exist since 0002.

CREATE TABLE agent_hooks (
  instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
  config_json TEXT NOT NULL,   -- agenthook.Config, secret included (never returned by the API)
  updated_at  TEXT NOT NULL
);

-- Recent hook calls (dashboard) and the per-chat reply counter of the loop guard.
CREATE TABLE agent_calls (
  seq          INTEGER PRIMARY KEY AUTOINCREMENT,
  id           TEXT NOT NULL UNIQUE,
  instance_id  TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  chat_jid     TEXT NOT NULL,
  message_id   TEXT NOT NULL DEFAULT '', -- LiteZap id of the message that triggered it
  status       TEXT NOT NULL,            -- ok | failed | skipped | discarded | paused
  reason       TEXT NOT NULL DEFAULT '', -- failure/skip code (timeout, http_error, loop_guard...)
  detail       TEXT NOT NULL DEFAULT '',
  http_status  INTEGER NOT NULL DEFAULT 0,
  duration_ms  INTEGER NOT NULL DEFAULT 0,
  replied      INTEGER NOT NULL DEFAULT 0, -- a reply was queued
  reply_id     TEXT NOT NULL DEFAULT '',   -- queue id of the reply
  actions      TEXT NOT NULL DEFAULT '',   -- executed action types, comma separated
  created_at   TEXT NOT NULL
);
CREATE INDEX agent_calls_recent ON agent_calls (instance_id, seq DESC);
CREATE INDEX agent_calls_replies ON agent_calls (instance_id, chat_jid, created_at) WHERE replied = 1;
