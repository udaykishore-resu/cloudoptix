-- 0012_discovery_ai: discovery job tracking, the two chat surfaces
-- (onboarding and copilot), the RAG knowledge corpus, and outbound
-- notifications.
CREATE TABLE discovery_runs (
    id                   TEXT PRIMARY KEY,
    tenant_id            TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    account_id           TEXT NOT NULL DEFAULT '',
    regions              JSONB NOT NULL DEFAULT '[]'::jsonb,
    trigger              TEXT NOT NULL DEFAULT '',
    state                TEXT NOT NULL CHECK (state IN ('running', 'completed', 'partial', 'failed')),
    started_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at          TIMESTAMPTZ,
    resources_discovered INT NOT NULL DEFAULT 0,
    resources_updated    INT NOT NULL DEFAULT 0,
    resources_removed    INT NOT NULL DEFAULT 0,
    relationships_found  INT NOT NULL DEFAULT 0,
    metrics_collected    INT NOT NULL DEFAULT 0,
    service_results      JSONB NOT NULL DEFAULT '[]'::jsonb,
    errors               JSONB NOT NULL DEFAULT '[]'::jsonb,
    coverage             DOUBLE PRECISION NOT NULL DEFAULT 0,
    duration_ms          BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX idx_discovery_runs_tenant_started ON discovery_runs (tenant_id, started_at DESC);
-- Latest(tenant, accountID): one index-only lookup for "when did we last
-- scan this account", which the connection-health screen calls constantly.
CREATE INDEX idx_discovery_runs_tenant_account_started ON discovery_runs (tenant_id, account_id, started_at DESC);
SELECT cloudoptix_enable_tenant_rls('discovery_runs');

CREATE TABLE conversations (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL CHECK (kind IN ('onboarding', 'copilot')),
    title      TEXT NOT NULL DEFAULT '',
    actor      TEXT NOT NULL DEFAULT '',
    spec_id    TEXT,
    state      TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_conversations_tenant_kind ON conversations (tenant_id, kind, updated_at DESC);
SELECT cloudoptix_attach_updated_at('conversations');
SELECT cloudoptix_enable_tenant_rls('conversations');

-- conversation_turns is ports.Turn, normalized out of Conversation.Turns:
-- AppendTurn is the hot path (one INSERT per chat message) and a JSONB
-- array on the parent would mean read-modify-write the whole conversation
-- on every message, which gets worse the longer a conversation runs — the
-- opposite of what an append-heavy workload wants.
CREATE TABLE conversation_turns (
    id               TEXT PRIMARY KEY,
    tenant_id        TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    conversation_id  TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    ordinal          BIGINT NOT NULL,
    role             TEXT NOT NULL CHECK (role IN ('system', 'user', 'assistant', 'tool')),
    content          TEXT NOT NULL DEFAULT '',
    at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    tool_calls       JSONB NOT NULL DEFAULT '[]'::jsonb,
    tool_results     JSONB NOT NULL DEFAULT '[]'::jsonb,
    retrieved        JSONB NOT NULL DEFAULT '[]'::jsonb,
    citations        JSONB NOT NULL DEFAULT '[]'::jsonb,
    spec_patch       JSONB NOT NULL DEFAULT '[]'::jsonb,
    provenance       JSONB NOT NULL DEFAULT '{}'::jsonb,
    input_tokens     INT NOT NULL DEFAULT 0,
    output_tokens    INT NOT NULL DEFAULT 0,
    latency_ms       BIGINT NOT NULL DEFAULT 0,
    model            TEXT NOT NULL DEFAULT '',
    grounded         BOOLEAN NOT NULL DEFAULT true,
    grounding_issues JSONB NOT NULL DEFAULT '[]'::jsonb,
    degraded         BOOLEAN NOT NULL DEFAULT false,
    UNIQUE (conversation_id, ordinal)
);

CREATE INDEX idx_conversation_turns_conversation ON conversation_turns (conversation_id, ordinal);
SELECT cloudoptix_enable_tenant_rls('conversation_turns');

-- knowledge_documents backs ports.KnowledgeStore/Document. tenant_id is
-- nullable — platform-wide knowledge (AWS docs, FinOps guidance, the
-- CloudOptix rule catalogue) has no owning tenant, matching the ports.Document
-- comment ("empty for platform-wide knowledge") — and the RLS policy is
-- written by hand rather than through cloudoptix_enable_tenant_rls because a
-- tenant must see its own documents *plus* every platform document, which
-- the shared single-column helper's equality check cannot express.
CREATE TABLE knowledge_documents (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT REFERENCES tenants(id) ON DELETE CASCADE,
    source     TEXT NOT NULL DEFAULT '',
    title      TEXT NOT NULL DEFAULT '',
    content    TEXT NOT NULL DEFAULT '',
    url        TEXT NOT NULL DEFAULT '',
    metadata   JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- pgvector is not in the module's approved dependency set, so the
    -- embedding is stored as a JSONB float array; retrieval ranking is
    -- performed in the Go adapter rather than pushed down as a vector
    -- distance operator. Acceptable at the corpus sizes a single-tenant RAG
    -- index reaches; revisit if/when pgvector is approved.
    embedding  JSONB NOT NULL DEFAULT '[]'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_knowledge_documents_tenant ON knowledge_documents (tenant_id);
CREATE INDEX idx_knowledge_documents_source ON knowledge_documents (source);
SELECT cloudoptix_attach_updated_at('knowledge_documents');

ALTER TABLE knowledge_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge_documents FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON knowledge_documents
    USING (tenant_id IS NULL OR tenant_id = cloudoptix_current_tenant() OR cloudoptix_system_scope())
    WITH CHECK (tenant_id IS NULL OR tenant_id = cloudoptix_current_tenant() OR cloudoptix_system_scope());

CREATE TABLE notifications (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    channel    TEXT NOT NULL,
    target     TEXT NOT NULL DEFAULT '',
    secret_ref TEXT NOT NULL DEFAULT '',
    subject    TEXT NOT NULL DEFAULT '',
    body       TEXT NOT NULL DEFAULT '',
    blocks     JSONB NOT NULL DEFAULT '{}'::jsonb,
    severity   TEXT NOT NULL DEFAULT 'INFO' CHECK (severity IN ('INFO', 'LOW', 'MEDIUM', 'HIGH', 'CRITICAL')),
    event_type TEXT NOT NULL DEFAULT '',
    link_url   TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at    TIMESTAMPTZ,
    attempts   INT NOT NULL DEFAULT 0,
    error      TEXT NOT NULL DEFAULT ''
);

-- ClaimPending's shape: unsent notifications, oldest first, is the delivery
-- worker's queue scan.
CREATE INDEX idx_notifications_pending ON notifications (created_at) WHERE sent_at IS NULL;
CREATE INDEX idx_notifications_tenant ON notifications (tenant_id, created_at DESC);
SELECT cloudoptix_enable_tenant_rls('notifications');
