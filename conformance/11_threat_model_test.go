//go:build conformance

package conformance

// Spec: specs/11-threat-model.md — all four mitigations are verified by the
// container-backed security pack in 11_security_pack_test.go:
//
//	C11-001 cross-tenant isolation (RLS + secret tenancy)
//	C11-002 prompt-injection egress blocking
//	C11-003 runaway-agent guards (step caps + budgets)
//	C11-004 stolen run token revocation
