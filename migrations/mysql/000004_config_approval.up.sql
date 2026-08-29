ALTER TABLE config_entries
    ADD COLUMN published_revision BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN review_comment TEXT NULL,
    ADD COLUMN reviewed_by TEXT NULL,
    ADD COLUMN reviewed_at DATETIME(6) NULL;

UPDATE config_entries SET review_comment = '', reviewed_by = '';
ALTER TABLE config_entries
    MODIFY review_comment TEXT NOT NULL,
    MODIFY reviewed_by TEXT NOT NULL,
    ADD INDEX idx_config_entries_published_revision (environment, tenant_id(128), service(128), published_revision);

ALTER TABLE config_revisions
    ADD COLUMN review_comment TEXT NULL,
    ADD COLUMN reviewed_by TEXT NULL,
    ADD COLUMN reviewed_at DATETIME(6) NULL;

UPDATE config_revisions SET review_comment = '', reviewed_by = '';
ALTER TABLE config_revisions
    MODIFY review_comment TEXT NOT NULL,
    MODIFY reviewed_by TEXT NOT NULL;
