-- 002_audit_append_only.down.sql — remove the append-only trigger.
DROP TRIGGER IF EXISTS audit_events_append_only ON audit_events;
DROP FUNCTION IF EXISTS audit_append_only();
