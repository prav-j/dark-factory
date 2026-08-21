//go:build conformance

package conformance

import "testing"

// Spec: specs/08-secrets.md
func TestSecrets(t *testing.T) {
	t.Run("C08-001_envelope_encryption_per_user_org", func(t *testing.T) {
		Pending(t, Check{ID: "C08-001", Spec: "08-secrets.md",
			Text: "KMS root key per environment; DEKs per user/org; ciphertexts unreadable without the owning KEK."})
	})
	t.Run("C08-002_no_secrets_in_transcripts_or_logs", func(t *testing.T) {
		Pending(t, Check{ID: "C08-002", Spec: "08-secrets.md",
			Text: "Scrubbing middleware prevents secrets from appearing in transcripts or logs; every secret read is access-logged."})
	})
	t.Run("C08-003_jit_injection_only_at_gateways", func(t *testing.T) {
		Pending(t, Check{ID: "C08-003", Spec: "08-secrets.md",
			Text: "Credentials are resolved only inside gateways at call time and never written into snapshots or sandbox env."})
	})
}
