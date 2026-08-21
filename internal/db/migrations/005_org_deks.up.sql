-- 005_org_deks.up.sql — per-org data encryption keys, wrapped by the
-- environment KEK (envelope encryption; specs/08-secrets.md).
CREATE TABLE org_deks (
    org_id     uuid NOT NULL REFERENCES orgs(id),
    version    int  NOT NULL,
    ciphertext bytea NOT NULL,               -- DEK wrapped by KEK
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, version)
);

-- secrets.kek_version actually tracks the DEK version used; rename for clarity.
ALTER TABLE secrets RENAME COLUMN kek_version TO dek_version;
