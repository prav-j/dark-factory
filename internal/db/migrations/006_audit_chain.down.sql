-- 006_audit_chain.down.sql
DROP TRIGGER IF EXISTS audit_events_chain ON audit_events;
DROP FUNCTION IF EXISTS audit_chain_fn();
ALTER TABLE audit_events DROP COLUMN IF EXISTS entry_hash;
ALTER TABLE audit_events DROP COLUMN IF EXISTS prev_hash;
