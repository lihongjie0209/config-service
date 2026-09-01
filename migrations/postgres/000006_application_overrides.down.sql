DROP INDEX config_entries_resolve_idx;
CREATE INDEX config_entries_resolve_idx ON config_entries(environment,service,tenant_id,status,config_key);
ALTER TABLE config_entries DROP CONSTRAINT config_entries_scope_key;
ALTER TABLE config_entries ADD CONSTRAINT config_entries_environment_tenant_id_service_config_key_key UNIQUE(environment,tenant_id,service,config_key);
ALTER TABLE config_entries DROP CONSTRAINT chk_config_entries_scope;
ALTER TABLE config_entries DROP COLUMN application_id;
