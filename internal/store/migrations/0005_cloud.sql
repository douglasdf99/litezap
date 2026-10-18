-- Managed hosting (cloud C1): usage counters, runtime engine settings, per-instance
-- operator flags and operator-owned webhooks.

-- Daily usage per instance (UTC day). No foreign key: usage outlives a deleted instance
-- (billing reads it afterwards).
CREATE TABLE usage_daily (
  instance_id        TEXT NOT NULL,
  day                TEXT NOT NULL,             -- YYYY-MM-DD
  messages_sent      INTEGER NOT NULL DEFAULT 0, -- accepted API sends that reached WhatsApp
  messages_received  INTEGER NOT NULL DEFAULT 0,
  media_bytes        INTEGER NOT NULL DEFAULT 0, -- attachments sent and received
  connected_seconds  INTEGER NOT NULL DEFAULT 0,
  webhook_deliveries INTEGER NOT NULL DEFAULT 0, -- delivered to customer webhooks
  PRIMARY KEY (instance_id, day)
);

-- Runtime settings changed by the operator (e.g. max_instances, global send suspension).
CREATE TABLE engine_kv (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE instance_flags (
  instance_id       TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
  sending_suspended INTEGER NOT NULL DEFAULT 0,
  suspend_reason    TEXT NOT NULL DEFAULT '',
  updated_at        TEXT NOT NULL
);

-- '' (admin or instance token) or 'operator': operator webhooks are invisible to others.
ALTER TABLE webhooks ADD COLUMN owner TEXT NOT NULL DEFAULT '';
