-- LiteZap agents (spec 2026-10-20, slice A1): Claude agents that run a tool-use loop over
-- LiteZap's own WhatsApp tools.

-- Agents of an instance: mode (aprovacao | autonomo | sugerir), the operator's
-- instructions, the permission groups (tools_json), the trigger (trigger_json) and the
-- limits (limits_json).
CREATE TABLE agents (
  id           TEXT PRIMARY KEY,
  instance_id  TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  enabled      INTEGER NOT NULL DEFAULT 0,
  mode         TEXT NOT NULL,
  instructions TEXT NOT NULL DEFAULT '',
  tools_json   TEXT NOT NULL DEFAULT '[]',
  trigger_json TEXT NOT NULL DEFAULT '{}',
  limits_json  TEXT NOT NULL DEFAULT '{}',
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);
CREATE INDEX agents_instance ON agents (instance_id, created_at);

-- One execution of an agent: trigger, the steps (tool, summarized arguments, outcome;
-- truncated), cost and outcome. Kept 30 days.
CREATE TABLE agent_runs (
  seq           INTEGER PRIMARY KEY AUTOINCREMENT,
  id            TEXT NOT NULL UNIQUE,
  instance_id   TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  agent_id      TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  agent_name    TEXT NOT NULL DEFAULT '',
  trigger       TEXT NOT NULL,             -- message | manual
  chat_jid      TEXT NOT NULL DEFAULT '',  -- the trigger chat ('' for a manual run without one)
  message_id    TEXT NOT NULL DEFAULT '',  -- the triggering message
  mode          TEXT NOT NULL,
  status        TEXT NOT NULL,             -- running | completed | step_limit | failed | discarded
  steps_json    TEXT NOT NULL DEFAULT '[]',
  output        TEXT NOT NULL DEFAULT '',  -- the model's final text
  model         TEXT NOT NULL DEFAULT '',
  model_calls   INTEGER NOT NULL DEFAULT 0,
  input_tokens  INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cost_micros   INTEGER NOT NULL DEFAULT 0,
  sent          INTEGER NOT NULL DEFAULT 0, -- writes executed
  drafts        INTEGER NOT NULL DEFAULT 0, -- writes waiting for approval
  error         TEXT NOT NULL DEFAULT '',
  started_at    TEXT NOT NULL,
  ended_at      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX agent_runs_agent ON agent_runs (instance_id, agent_id, seq);
CREATE INDEX agent_runs_started ON agent_runs (started_at);

-- A write tool call of an agent in mode aprovacao, waiting for a human: approval executes
-- exactly the stored call (tool + args_json).
CREATE TABLE agent_drafts (
  seq         INTEGER PRIMARY KEY AUTOINCREMENT,
  id          TEXT NOT NULL UNIQUE,
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  agent_id    TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  run_id      TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
  tool        TEXT NOT NULL,
  args_json   TEXT NOT NULL,
  chat_jid    TEXT NOT NULL DEFAULT '',   -- recipient (sends) or chat the call acts on
  status      TEXT NOT NULL,              -- pending | approving | approved | rejected | failed | expired
  decided_by  TEXT NOT NULL DEFAULT '',
  decided_at  TEXT NOT NULL DEFAULT '',
  result      TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL
);
CREATE INDEX agent_drafts_status ON agent_drafts (instance_id, status, seq);
