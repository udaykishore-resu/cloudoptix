-- Restore the tenants(id) foreign keys 0015 dropped. NOT VALID so the
-- rollback does not fail on rows an unfinished onboarding conversation left
-- behind under a tenant scope that was never approved into a tenants row;
-- the constraint still applies to every write from here on.
ALTER TABLE conversations      ADD CONSTRAINT conversations_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE NOT VALID;
ALTER TABLE conversation_turns ADD CONSTRAINT conversation_turns_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE NOT VALID;
ALTER TABLE specs              ADD CONSTRAINT specs_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE NOT VALID;
ALTER TABLE spec_versions      ADD CONSTRAINT spec_versions_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE NOT VALID;
ALTER TABLE audit_logs         ADD CONSTRAINT audit_logs_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE RESTRICT NOT VALID;
