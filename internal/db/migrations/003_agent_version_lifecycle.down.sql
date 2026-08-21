-- 003_agent_version_lifecycle.down.sql
DROP TRIGGER IF EXISTS agent_versions_lifecycle ON agent_versions;
DROP FUNCTION IF EXISTS agent_versions_lifecycle_fn();
