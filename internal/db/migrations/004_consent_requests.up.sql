-- 004_consent_requests.up.sql — pending consent flow records (specs/04).
-- A grant may only be created from an approved consent request, so every
-- row in grants carries consent evidence traceable to a decision.
CREATE TABLE consent_requests (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     uuid NOT NULL REFERENCES orgs(id),
    user_id    uuid NOT NULL REFERENCES users(id),
    resource   text NOT NULL,
    scope      text NOT NULL,
    status     text NOT NULL DEFAULT 'pending'
               CHECK (status IN ('pending', 'approved', 'denied')),
    created_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz,
    decided_by text
);

CREATE INDEX consent_requests_user_idx ON consent_requests(user_id, status);
