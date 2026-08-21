//go:build conformance

package conformance

import "testing"

// Spec: specs/16-deployment-sessions.md
func TestDeploymentSessions(t *testing.T) {
	t.Run("C16-001_one_session_one_sandbox", func(t *testing.T) {
		Pending(t, Check{ID: "C16-001", Spec: "16-deployment-sessions.md#operator-architecture",
			Text: "One session owns exactly one sandbox (microVM pod); a session contains one or more runs."})
	})
	t.Run("C16-002_phase_machine", func(t *testing.T) {
		Pending(t, Check{ID: "C16-002", Spec: "16-deployment-sessions.md#operator-architecture",
			Text: "Session phases transition Provisioning -> Running -> Idle -> Committing -> Terminated per spec."})
	})
	t.Run("C16-003_stop_hook_fires_on_all_end_paths", func(t *testing.T) {
		Pending(t, Check{ID: "C16-003", Spec: "16-deployment-sessions.md#the-stop-hook-commit-before-death-contract",
			Text: "Stop hook fires on idle shutdown, max lifetime, user stop, and preemption; manifest emitted before exit."})
	})
	t.Run("C16-004_uncommitted_diff_preserved", func(t *testing.T) {
		Pending(t, Check{ID: "C16-004", Spec: "16-deployment-sessions.md#the-stop-hook-commit-before-death-contract",
			Text: "If the model fails to commit in time, uncommitted=true plus a diff blob preserves work for resume."})
	})
	t.Run("C16-005_resume_from_transcript_and_git", func(t *testing.T) {
		Pending(t, Check{ID: "C16-005", Spec: "16-deployment-sessions.md#resume-flow",
			Text: "Resume needs only transcript + branch/PR refs from the manifest; fresh pod forks from environmentKey snapshot."})
	})
	t.Run("C16-006_ddb_session_store_access_patterns", func(t *testing.T) {
		Pending(t, Check{ID: "C16-006", Spec: "16-deployment-sessions.md#session-store-dynamodb",
			Text: "Sessions-by-org, resumable-by-user, active-per-agent lookups work via DDB GSIs; TTL expires terminated sessions."})
	})
}
