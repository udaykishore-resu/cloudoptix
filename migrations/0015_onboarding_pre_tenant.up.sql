-- 0015_onboarding_pre_tenant: let an onboarding conversation and its draft
-- specification exist before the tenant they belong to does.
--
-- internal/application/onboarding.Service.Start mints the tenant scope a
-- conversation will live under and writes the conversation, its turns, the
-- spec grouping row and the first draft spec_versions row under that scope
-- — but the tenants row itself is only created by Approve, several
-- requests later, once the specification is complete (see the Service doc
-- comment: "a real Tenant row is not created until Approve"). That is the
-- design: a company that abandons onboarding half-way never becomes a
-- tenant.
--
-- 0003 and 0012 foreign-keyed these four tables to tenants(id), which
-- contradicts that design and made every Start against Postgres fail with
--
--   insert or update on table "conversations" violates foreign key
--   constraint "conversations_tenant_id_fkey" (SQLSTATE 23503)
--
-- so the API's --seed-demo (and the first real onboarding) could never
-- complete on storage=postgres. The memstore has no such constraint, which
-- is why in-memory mode never showed it.
--
-- Tenant isolation on these tables does not depend on the foreign key: it
-- is enforced by the RLS policies cloudoptix_enable_tenant_rls attached in
-- 0003/0012, which compare tenant_id with the session's scope regardless of
-- whether a tenants row exists yet. Nothing deletes tenants rows
-- (TenantRepository has no Delete; tenants are archived by state), so the
-- ON DELETE CASCADE the constraints carried was never exercised.
ALTER TABLE conversations      DROP CONSTRAINT IF EXISTS conversations_tenant_id_fkey;
ALTER TABLE conversation_turns DROP CONSTRAINT IF EXISTS conversation_turns_tenant_id_fkey;
ALTER TABLE specs              DROP CONSTRAINT IF EXISTS specs_tenant_id_fkey;
ALTER TABLE spec_versions      DROP CONSTRAINT IF EXISTS spec_versions_tenant_id_fkey;

-- audit_logs has the same problem from the other direction. Approve writes
-- the tenants row inside its UnitOfWork transaction and then records
-- audit.ActionTenantCreated — but AuditRepository.Append opens its OWN
-- transaction by design (the per-tenant advisory lock must span exactly the
-- read-head/seal/insert sequence; see internal/adapters/postgres/audit.go),
-- so from Append's connection the new tenants row is still uncommitted and
-- audit_logs_tenant_id_fkey rejects the very first record of every tenant:
--
--   insert or update on table "audit_logs" violates foreign key constraint
--   "audit_logs_tenant_id_fkey" (SQLSTATE 23503)
--
-- The constraint was ON DELETE RESTRICT to say "a tenant with audit history
-- cannot be hard-deleted". That property survives without it: nothing in
-- the platform deletes tenants rows, and 0013's trg_audit_logs_immutable trigger
-- rejects DELETE on the log itself regardless of what happens to the tenant.
ALTER TABLE audit_logs         DROP CONSTRAINT IF EXISTS audit_logs_tenant_id_fkey;
