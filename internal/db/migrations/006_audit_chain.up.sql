-- 006_audit_chain.up.sql — hash-chained append-only audit log (specs/10).
-- Each entry commits to its predecessor; any tampering (update, delete,
-- re-insert) breaks the chain and is detected by VerifyChain.
ALTER TABLE audit_events ADD COLUMN prev_hash text NOT NULL DEFAULT '';
ALTER TABLE audit_events ADD COLUMN entry_hash text NOT NULL DEFAULT '';

CREATE OR REPLACE FUNCTION audit_chain_fn() RETURNS trigger AS $$
DECLARE ph text := '';
BEGIN
    SELECT entry_hash INTO ph FROM audit_events ORDER BY id DESC LIMIT 1;
    IF NOT FOUND THEN ph := ''; END IF;
    NEW.prev_hash := ph;
    NEW.entry_hash := encode(sha256(convert_to(
        ph || '|' || NEW.id::text || '|' || NEW.actor || '|' || NEW.action ||
        '|' || NEW.resource || '|' || NEW.decision || '|' || NEW.reason,
        'UTF8')), 'hex');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_events_chain
    BEFORE INSERT ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_chain_fn();
