DROP INDEX IF EXISTS idx_config_entries_published_revision;
ALTER TABLE config_revisions DROP COLUMN reviewed_at, DROP COLUMN reviewed_by, DROP COLUMN review_comment;
ALTER TABLE config_entries DROP COLUMN reviewed_at, DROP COLUMN reviewed_by, DROP COLUMN review_comment, DROP COLUMN published_revision;
