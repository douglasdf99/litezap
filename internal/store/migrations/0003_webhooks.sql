CREATE TABLE webhooks (
  seq         INTEGER PRIMARY KEY AUTOINCREMENT,
  id          TEXT NOT NULL UNIQUE,
  instance_id TEXT REFERENCES instances(id) ON DELETE CASCADE, -- NULL: every instance
  url         TEXT NOT NULL,
  secret      TEXT NOT NULL,                -- HMAC key of the signature; never returned after creation
  events_json TEXT NOT NULL,                -- filter patterns, e.g. ["message.*"]
  version     INTEGER NOT NULL DEFAULT 1,   -- envelope version delivered
  enabled     INTEGER NOT NULL DEFAULT 1,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

-- Outbox: one row per (webhook, event); a pending row is retried until delivered or expired.
CREATE TABLE webhook_deliveries (
  seq              INTEGER PRIMARY KEY AUTOINCREMENT,
  id               TEXT NOT NULL UNIQUE,
  webhook_id       TEXT NOT NULL REFERENCES webhooks(id) ON DELETE CASCADE,
  event_id         TEXT NOT NULL,
  event_type       TEXT NOT NULL,
  instance_id      TEXT NOT NULL DEFAULT '',
  payload          BLOB NOT NULL,           -- the envelope JSON, sent as is
  status           TEXT NOT NULL,           -- pending | delivered | failed (dead-letter)
  attempts         INTEGER NOT NULL DEFAULT 0,
  next_attempt_at  TEXT NOT NULL,
  expires_at       TEXT NOT NULL,           -- give up (failed) after this
  last_status_code INTEGER NOT NULL DEFAULT 0,
  last_error       TEXT NOT NULL DEFAULT '',
  created_at       TEXT NOT NULL,
  updated_at       TEXT NOT NULL,
  delivered_at     TEXT NOT NULL DEFAULT '',
  UNIQUE (webhook_id, event_id)
);
CREATE INDEX webhook_deliveries_due ON webhook_deliveries (next_attempt_at) WHERE status = 'pending';
CREATE INDEX webhook_deliveries_list ON webhook_deliveries (webhook_id, status, seq);
CREATE INDEX webhook_deliveries_done ON webhook_deliveries (status, updated_at) WHERE status <> 'pending';
