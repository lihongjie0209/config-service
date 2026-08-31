CREATE INDEX config_outbox_retention_idx ON config_outbox_events (published_at, id) WHERE published_at IS NOT NULL;
