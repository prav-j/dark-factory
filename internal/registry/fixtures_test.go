//go:build integration

package registry_test

const (
	orgID  = "11111111-1111-1111-1111-111111111111"
	userID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
)

const specV1 = `
apiVersion: agents/v1
kind: Agent
metadata: {name: bot, owner: user-1234}
spec:
  model: {provider: anthropic, name: claude-sonnet-4-5}
  prompt: {type: inline, value: "v1"}
  triggers: [{type: chat}]
  limits: {maxStepsPerRun: 5, maxTokensPerRun: 1000, monthlyBudgetUsd: 10}
`

const specV2 = `
apiVersion: agents/v1
kind: Agent
metadata: {name: bot, owner: user-1234}
spec:
  model: {provider: anthropic, name: claude-sonnet-4-5}
  prompt: {type: inline, value: "v2"}
  triggers: [{type: chat}]
  limits: {maxStepsPerRun: 10, maxTokensPerRun: 2000, monthlyBudgetUsd: 20}
`
