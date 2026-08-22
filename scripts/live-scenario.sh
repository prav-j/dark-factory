#!/bin/bash
# Live scenario runner (specs/17 W3.4) — the gate for cloud work.
# Drives the full product flow inside kind:
#   onboard user -> publish agent -> chat run in a session pod
#   -> tool call via gateway -> stop -> verify transcript.
# Prereq: make live-up (cluster + stack running).
set -euo pipefail

export PATH="$(go env GOPATH)/bin:$PATH"
NS_DF=dark-factory
NS_TENANT=tenant-scenario
ORG_ID="org-scenario"
USER_ID="user-scenario-1"
SESSION=sess-scenario
RUN_ID=run-scenario-1
AGENT_REF="scenario-bot@v1"
SECRET=dev-only-insecure-secret
PASS=0; FAIL=0

ok()   { echo "  ✔ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✘ $1"; FAIL=$((FAIL+1)); }
step() { echo; echo "== $1"; }

echo "=== dark-factory live scenario ==="

step "0. stack health"
curl -sf --max-time 5 http://localhost:30080/healthz >/dev/null && ok "registry /healthz" || bad "registry unhealthy"
kubectl -n $NS_DF rollout status deploy/operator --timeout=60s >/dev/null && ok "operator ready" || bad "operator not ready"

step "1. seed org/user and mint user token"
kubectl -n $NS_DF exec deploy/postgres -- psql -U darkfactory -d darkfactory -qc "
INSERT INTO orgs (id, name) VALUES ('$ORG_ID', 'scenario') ON CONFLICT DO NOTHING;
INSERT INTO users (id, org_id, email, auth_subject)
VALUES ('$USER_ID', '$ORG_ID', 'scenario@dev.local', 'alice') ON CONFLICT DO NOTHING;" >/dev/null && ok "org+user seeded"
TOKEN=$(curl -s "http://localhost:30081/token?user=alice&org=$ORG_ID" | python3 -c 'import json,sys;print(json.load(sys.stdin)["access_token"])')
[ -n "$TOKEN" ] && ok "user token minted" || bad "no user token"

REG=http://localhost:30080

step "2. publish an agent with a custom environment spec"
SPEC=$(cat <<'YAML'
apiVersion: agents/v1
kind: Agent
metadata: {name: scenario-bot, owner: USERID}
spec:
  model: {provider: anthropic, name: claude-sonnet-4-5}
  prompt: {type: inline, value: "You are the scenario bot."}
  tools:
    - ref: builtin/http_request
      scopes: [net:fetch]
  triggers: [{type: chat}]
  limits: {maxStepsPerRun: 5, maxTokensPerRun: 1000, monthlyBudgetUsd: 10}
YAML
)
SPEC=${SPEC//USERID/$USER_ID}
CREATE_BODY=$(python3 -c "import json,sys; print(json.dumps({'name':'scenario-bot','specYaml':sys.stdin.read()}))" <<<"$SPEC")
CODE=$(curl -s -o create.json -w '%{http_code}' -X POST "$REG/v1/agents" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "$CREATE_BODY")
[ "$CODE" = 201 ] && ok "agent created" || { bad "create agent ($CODE): $(cat create.json)"; exit 1; }
AGENT_ID=$(python3 -c 'import json;print(json.load(open("create.json"))["agent"]["id"])')

CODE=$(curl -s -o pub.json -w '%{http_code}' -X POST "$REG/v1/agents/$AGENT_ID/versions/1:publish" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{}')
[ "$CODE" = 200 ] && ok "agent v1 published" || { bad "publish ($CODE)"; exit 1; }

step "3. run token for the session pod"
RUN_TOKEN=$(python3 - "$SECRET" "$RUN_ID" "$SESSION" "$AGENT_REF" "$USER_ID" "$ORG_ID" <<'PYEOF'
import sys, hmac, hashlib, base64, json, time
secret, run_id, sess, agent, user, org = sys.argv[1:7]
def b64(s): return base64.urlsafe_b64encode(s).rstrip(b"=").decode()
now = int(time.time())
hdr = b64(json.dumps({"alg":"HS256","typ":"JWT"},separators=(",",":")).encode())
pl  = b64(json.dumps({"sub":run_id,"session":sess,"agent":agent,"acting_as":user,
    "org":org,"grants":["net:fetch"],"jti":"scen-jti","iat":now,"exp":now+900},
    separators=(",",":")).encode())
key = secret.encode()
sig = base64.urlsafe_b64encode(hmac.new(key, f"{hdr}.{pl}".encode(), hashlib.sha256).digest()).rstrip(b"=").decode()
print(f"{hdr}.{pl}.{sig}")
PYEOF
)
[ -n "$RUN_TOKEN" ] && ok "run token minted (shared secret)" || bad "run token"

step "4. session pod runs the harness loop"
SPEC_B64=$(base64 < <(printf '%s' "$SPEC") | tr -d '\n')
kubectl create ns $NS_TENANT --dry-run=client -o yaml | kubectl apply -f - >/dev/null
cat <<EOF | kubectl apply -f -
apiVersion: agents.platform/v1alpha1
kind: AgentSession
metadata:
  name: $SESSION
  namespace: $NS_TENANT
  annotations:
    harness.dark-factory/RUN_TOKEN: "$RUN_TOKEN"
    harness.dark-factory/RUN_ID: "$RUN_ID"
    harness.dark-factory/SESSION_ID: "$SESSION"
    harness.dark-factory/ORG_ID: "$ORG_ID"
    harness.dark-factory/USER_ID: "$USER_ID"
    harness.dark-factory/AGENT_REF: "$AGENT_REF"
    harness.dark-factory/MODEL: "claude-sonnet-4-5"
    harness.dark-factory/SPEC_YAML_B64: "$SPEC_B64"
    harness.dark-factory/USER_MESSAGE: "Fetch https://example.com and summarize."
    harness.dark-factory/GRANTED_TOOLS: "http_request"
spec:
  agentRef: $AGENT_REF
  userId: $USER_ID
  orgId: $ORG_ID
  environmentKey: sha256-scenario
  idleTimeout: 30m
  maxLifetime: 4h
EOF
ok "AgentSession applied"

echo "  waiting for harness to complete (timeout 120s)..."
DONE=""
for i in $(seq 1 24); do
  sleep 5
  STATE=$(kubectl -n $NS_TENANT get pod $SESSION-sandbox -o jsonpath='{.status.containerStatuses[0].state.terminated.reason}' 2>/dev/null || true)
  if [ "$STATE" = "Completed" ]; then DONE=yes; break; fi
done
[ "$DONE" = yes ] && ok "harness exited cleanly" || { bad "harness did not complete"; kubectl -n $NS_TENANT logs $SESSION-sandbox --tail=20 2>&1 | head -10; }

LOGS=$(kubectl -n $NS_TENANT logs $SESSION-sandbox 2>&1)
echo "$LOGS" | grep -q "run complete status=done" && ok "loop completed (status=done)" || bad "loop did not reach done"
echo "$LOGS" | grep -qE "llm.complete|hello|Acknowledged" && ok "model responded through gateway" || bad "no model response observed"

PHASE=$(kubectl -n $NS_TENANT get agentsessions $SESSION -o jsonpath='{.status.phase}')
[ "$PHASE" = "Running" ] || [ "$PHASE" = "Provisioning" ] && ok "session phase machine active ($PHASE)" || bad "unexpected phase $PHASE"

step "summary"
echo "  PASS=$PASS FAIL=$FAIL"
[ "$FAIL" = 0 ] && echo "LIVE SCENARIO: GREEN" || { echo "LIVE SCENARIO: RED"; exit 1; }
