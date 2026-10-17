CREATE TABLE instances (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  state        TEXT NOT NULL,
  state_reason TEXT NOT NULL DEFAULT '',
  jid          TEXT NOT NULL DEFAULT '',
  token_hash   TEXT NOT NULL UNIQUE,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);
