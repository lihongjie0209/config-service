ALTER TABLE config_entries ADD COLUMN application_id TEXT NOT NULL DEFAULT '';
ALTER TABLE config_entries ADD CONSTRAINT chk_config_entries_scope CHECK(tenant_id <> '' OR application_id = '');
ALTER TABLE config_entries DROP CONSTRAINT config_entries_environment_tenant_id_service_config_key_key;
ALTER TABLE config_entries ADD CONSTRAINT config_entries_scope_key UNIQUE(environment,tenant_id,application_id,service,config_key);
DROP INDEX config_entries_resolve_idx;
CREATE INDEX config_entries_resolve_idx ON config_entries(environment,service,tenant_id,application_id,status,config_key);
