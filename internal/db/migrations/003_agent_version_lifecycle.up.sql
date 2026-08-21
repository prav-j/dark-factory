-- 003_agent_version_lifecycle.up.sql — enforce version lifecycle at the DB
-- level (specs/03: drafts mutable, published immutable, deprecation only).
CREATE OR REPLACE FUNCTION agent_versions_lifecycle_fn() RETURNS trigger AS $$
BEGIN
    IF OLD.status = 'published' THEN
        IF NEW.spec::text IS DISTINCT FROM OLD.spec::text
           OR NEW.spec_hash IS DISTINCT FROM OLD.spec_hash
           OR NEW.version IS DISTINCT FROM OLD.version
           OR NEW.agent_id IS DISTINCT FROM OLD.agent_id THEN
            RAISE EXCEPTION 'published agent version % is immutable', OLD.id;
        END IF;
        IF NEW.status NOT IN ('published', 'deprecated') THEN
            RAISE EXCEPTION 'published version cannot transition to %', NEW.status;
        END IF;
    END IF;
    IF OLD.status = 'draft' AND NEW.status = 'published' THEN
        NEW.published_at := now();
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER agent_versions_lifecycle
    BEFORE UPDATE ON agent_versions
    FOR EACH ROW EXECUTE FUNCTION agent_versions_lifecycle_fn();
