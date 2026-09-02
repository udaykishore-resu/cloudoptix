DROP TABLE IF EXISTS schema_migrations;
DROP FUNCTION IF EXISTS cloudoptix_attach_updated_at(regclass);
DROP FUNCTION IF EXISTS cloudoptix_set_updated_at();
DROP FUNCTION IF EXISTS cloudoptix_enable_tenant_rls(regclass);
DROP FUNCTION IF EXISTS cloudoptix_system_scope();
DROP FUNCTION IF EXISTS cloudoptix_current_tenant();
-- pgcrypto is left installed: other databases in the cluster may depend on
-- it and dropping an extension is not this migration's business to undo.
