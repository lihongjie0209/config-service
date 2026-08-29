CREATE TABLE config_entries (
 id UUID PRIMARY KEY, environment TEXT NOT NULL, tenant_id TEXT NOT NULL DEFAULT '', service TEXT NOT NULL, config_key TEXT NOT NULL,
 config_value JSONB, secret_ref TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, revision BIGINT NOT NULL, rollout_percentage INTEGER NOT NULL DEFAULT 100 CHECK (rollout_percentage BETWEEN 0 AND 100),
 version BIGINT NOT NULL DEFAULT 1, created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL, created_by TEXT NOT NULL, updated_by TEXT NOT NULL,
 UNIQUE(environment, tenant_id, service, config_key), CHECK ((config_value IS NULL) <> (secret_ref = ''))
);
CREATE INDEX config_entries_resolve_idx ON config_entries(environment, service, tenant_id, status, config_key);
CREATE TABLE config_revisions (
 id TEXT PRIMARY KEY, entry_id UUID NOT NULL REFERENCES config_entries(id), revision BIGINT NOT NULL, config_value JSONB, secret_ref TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, rollout_percentage INTEGER NOT NULL,
 version BIGINT NOT NULL DEFAULT 1, created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL, created_by TEXT NOT NULL, updated_by TEXT NOT NULL, UNIQUE(entry_id, revision)
);
