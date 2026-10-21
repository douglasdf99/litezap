-- LiteZap agents: who runs each agent. '' or 'provider' = the instance's AI provider (API
-- key, the default); 'codex_cli' = the user's own Codex CLI, logged in with their ChatGPT
-- plan (opt-in, see internal/agents/cliruntime.go).
ALTER TABLE agents ADD COLUMN runtime TEXT NOT NULL DEFAULT '';
