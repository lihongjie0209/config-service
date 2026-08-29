ALTER TABLE config_entries
    ADD COLUMN published_revision BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN review_comment TEXT NOT NULL DEFAULT '',
    ADD COLUMN reviewed_by TEXT NOT NULL DEFAULT '',
    ADD COLUMN reviewed_at TIMESTAMPTZ NULL;

ALTER TABLE config_revisions
    ADD COLUMN review_comment TEXT NOT NULL DEFAULT '',
    ADD COLUMN reviewed_by TEXT NOT NULL DEFAULT '',
    ADD COLUMN reviewed_at TIMESTAMPTZ NULL;

CREATE INDEX idx_config_entries_published_revision
    ON config_entries (environment, tenant_id, service, published_revision)
    WHERE published_revision > 0;
