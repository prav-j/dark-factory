-- 001_init.sql — control-plane schema (specs/03-data-model.md).
-- Tenant tables carry org_id and are protected by row-level security:
-- the app role must SET app.org_id = '<uuid>' per connection/transaction.
-- Migrations run as the table owner, which bypasses RLS.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE orgs (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       uuid NOT NULL REFERENCES orgs(id),
    email        text NOT NULL,
    auth_subject text NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX users_org_idx ON users(org_id);

CREATE TABLE agents (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             uuid NOT NULL REFERENCES orgs(id),
    owner_user_id      uuid NOT NULL REFERENCES users(id),
    name               text NOT NULL,
    current_version_id uuid,
    created_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, name)
);

CREATE TABLE agent_versions (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id    uuid NOT NULL REFERENCES agents(id),
    org_id      uuid NOT NULL REFERENCES orgs(id), -- denormalized for RLS
    version     int  NOT NULL,
    spec        jsonb NOT NULL,                    -- canonicalized spec
    spec_hash   text NOT NULL,
    status      text NOT NULL DEFAULT 'draft'
                CHECK (status IN ('draft', 'published', 'deprecated')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz,
    UNIQUE (agent_id, version),
    UNIQUE (id, agent_id)
);

ALTER TABLE agents
    ADD CONSTRAINT agents_current_version_fk
    FOREIGN KEY (current_version_id, id) REFERENCES agent_versions(id, agent_id);

CREATE TABLE tool_registry (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    ref             text NOT NULL UNIQUE,            -- e.g. builtin/web_search
    kind            text NOT NULL CHECK (kind IN ('builtin', 'mcp')),
    schema          jsonb,
    required_scopes text[] NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE mcp_connections (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        uuid NOT NULL REFERENCES users(id),
    org_id         uuid NOT NULL REFERENCES orgs(id),
    server_ref     text NOT NULL,
    credential_ref text NOT NULL,
    granted_scopes text[] NOT NULL DEFAULT '{}',
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, server_ref)
);

CREATE TABLE grants (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        uuid NOT NULL REFERENCES users(id),
    org_id         uuid NOT NULL REFERENCES orgs(id),
    resource       text NOT NULL,
    scope          text NOT NULL,
    expiry         timestamptz,
    consent_record jsonb NOT NULL,
    revoked_at     timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX grants_user_idx ON grants(user_id) WHERE revoked_at IS NULL;

-- Completed-run billing/audit lineage only; live state lives in DynamoDB.
CREATE TABLE run_records (
    run_id           uuid PRIMARY KEY,
    org_id           uuid NOT NULL REFERENCES orgs(id),
    user_id          uuid NOT NULL REFERENCES users(id),
    agent_version_id uuid NOT NULL REFERENCES agent_versions(id),
    trigger          text NOT NULL,
    status           text NOT NULL,
    token_usage      bigint NOT NULL DEFAULT 0,
    cost_usd         numeric(12, 6) NOT NULL DEFAULT 0,
    started_at       timestamptz NOT NULL,
    completed_at     timestamptz
);
CREATE INDEX run_records_user_idx ON run_records(user_id, started_at);

CREATE TABLE messages_index (
    run_id      uuid NOT NULL REFERENCES run_records(run_id),
    org_id      uuid NOT NULL REFERENCES orgs(id),
    seq         int  NOT NULL,
    role        text NOT NULL,
    content_ref text NOT NULL,                        -- object-store pointer
    PRIMARY KEY (run_id, seq)
);

CREATE TABLE secrets (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users(id),
    org_id      uuid NOT NULL REFERENCES orgs(id),
    ciphertext  bytea NOT NULL,
    kek_version int  NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- Append-only: UPDATE/DELETE blocked by trigger (see 002_audit_append_only.sql).
CREATE TABLE audit_events (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor       text NOT NULL,
    action      text NOT NULL,
    resource    text NOT NULL,
    decision    text NOT NULL,
    reason      text NOT NULL DEFAULT '',
    occurred_at timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- Row-level security: deny-by-default unless app.org_id matches.
-- ---------------------------------------------------------------------------

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'users', 'agents', 'agent_versions', 'mcp_connections',
        'grants', 'run_records', 'messages_index', 'secrets'
    ] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format($f$
            CREATE POLICY tenant_isolation ON %I
            USING (org_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
            WITH CHECK (org_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
        $f$, t);
    END LOOP;
END $$;

-- Non-superuser application role. Table owner bypasses RLS unless FORCEd —
-- we FORCE it, and migrations connect as a separate admin role anyway.
CREATE ROLE darkfactory_app NOLOGIN;
GRANT USAGE ON SCHEMA public TO darkfactory_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO darkfactory_app;
