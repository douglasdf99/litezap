-- Marco 11: platform and partners.
-- Instance tags (JSON array of strings) and API IP allowlist (JSON array of CIDRs; '[]' = any).
ALTER TABLE instances ADD COLUMN tags_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE instances ADD COLUMN ip_allowlist_json TEXT NOT NULL DEFAULT '[]';

-- Scoped API keys (lzk_...), bound to one instance. Only the SHA-256 of the key is kept.
CREATE TABLE api_keys (
  id                TEXT PRIMARY KEY,
  instance_id       TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  name              TEXT NOT NULL,
  key_hash          TEXT NOT NULL UNIQUE,
  scopes            TEXT NOT NULL,              -- comma-separated: read,send
  ip_allowlist_json TEXT NOT NULL DEFAULT '[]',
  created_at        TEXT NOT NULL,
  expires_at        TEXT NOT NULL DEFAULT '',   -- '' never
  last_used_at      TEXT NOT NULL DEFAULT '',
  created_by        TEXT NOT NULL DEFAULT 'admin'  -- admin | instance (rotating the instance token revokes these)
);
CREATE INDEX api_keys_instance ON api_keys(instance_id);

-- Hosted pairing links: signed tokens point at a row, so a link can be revoked and is
-- spent by the first completed pairing.
CREATE TABLE pairing_links (
  id          TEXT PRIMARY KEY,
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  created_at  TEXT NOT NULL,
  expires_at  TEXT NOT NULL,
  used_at     TEXT NOT NULL DEFAULT '',
  revoked_at  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX pairing_links_instance ON pairing_links(instance_id);
