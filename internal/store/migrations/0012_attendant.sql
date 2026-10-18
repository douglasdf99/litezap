-- Built-in attendant ("Atendente LiteZap", spec 4.7).

-- Per-instance settings: provider, model, caps (settings_json, no secrets) and the provider
-- keys, encrypted with the data/attendant_key file (secrets_enc, AES-256-GCM, never returned).
CREATE TABLE attendant_settings (
  instance_id   TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
  settings_json TEXT NOT NULL,
  secrets_enc   TEXT NOT NULL DEFAULT '',
  updated_at    TEXT NOT NULL
);

-- Agent profiles ("treinamento"): mode, script, tone, business context, knowledge base.
CREATE TABLE attendant_agents (
  id           TEXT PRIMARY KEY,
  instance_id  TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  profile_json TEXT NOT NULL,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);
CREATE INDEX attendant_agents_instance ON attendant_agents (instance_id, created_at);

-- Daily usage per instance (UTC day): the daily call cap and cost logging.
CREATE TABLE attendant_usage (
  instance_id   TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  day           TEXT NOT NULL,             -- YYYY-MM-DD (UTC)
  calls         INTEGER NOT NULL DEFAULT 0, -- provider calls (chat, transcription, speech)
  input_tokens  INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cost_micros   INTEGER NOT NULL DEFAULT 0, -- estimated cost in millionths of USD
  PRIMARY KEY (instance_id, day)
);
