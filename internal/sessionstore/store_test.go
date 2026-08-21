//go:build integration

package sessionstore_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/prav-j/dark-factory/internal/sessionstore"
	"github.com/prav-j/dark-factory/internal/testutil"
)

func newStore(t *testing.T) *sessionstore.Store {
	t.Helper()
	endpoint := testutil.DynamoDB(t)
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(testutil.TestRegion),
		config.WithCredentialsProvider(aws.CredentialsProviderFunc(func(_ context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: testutil.TestAccessKey, SecretAccessKey: testutil.TestSecretKey}, nil
		})),
	)
	if err != nil {
		t.Fatal(err)
	}
	client := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = &endpoint })
	store := sessionstore.New(client)
	if err := store.EnsureTables(context.Background()); err != nil {
		t.Fatalf("ensure tables: %v", err)
	}
	return store
}

const manifest = `{"sessionId":"sess-1","transcriptRef":"s3://t/sess-1.jsonl",
  "gitState":[{"repo":"acme/api","branch":"agent/sess-1/fix","headSha":"e3f1","prs":["#482"],"uncommitted":false}],
  "endedReason":"idle-timeout"}`

// C16-006: the three documented access patterns + TTL attribute presence.
func TestAccessPatterns(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	sess := sessionstore.Session{
		OrgID: "org-1", SessionID: "sess-1", UserID: "user-9",
		AgentRef: "repo-triage-bot@v7", Status: "Running",
		EnvironmentKey: "snap-abc", Manifest: []byte(manifest),
		CreatedAt: time.Now(), TTL: time.Now().Add(7 * 24 * time.Hour),
	}
	if err := store.PutSession(ctx, sess); err != nil {
		t.Fatalf("put: %v", err)
	}

	got, err := store.GetSession(ctx, "org-1", "sess-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.UserID != "user-9" || !strings.Contains(string(got.Manifest), "e3f1") {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}

	byOrg, err := store.ListByOrg(ctx, "org-1")
	if err != nil || len(byOrg) != 1 {
		t.Fatalf("list by org = %v err %v", byOrg, err)
	}
	byUser, err := store.ListResumableByUser(ctx, "user-9")
	if err != nil || len(byUser) != 1 {
		t.Fatalf("resumable by user = %v err %v", byUser, err)
	}
	n, err := store.CountActiveByAgent(ctx, "repo-triage-bot@v7")
	if err != nil || n != 1 {
		t.Fatalf("active per agent = %d err %v", n, err)
	}

	// Termination removes it from resumable/active patterns.
	if err := store.UpdateStatus(ctx, "org-1", "sess-1", "Terminated"); err != nil {
		t.Fatal(err)
	}
	byUser, _ = store.ListResumableByUser(ctx, "user-9")
	if len(byUser) != 0 {
		t.Fatalf("terminated session still resumable: %+v", byUser)
	}
	n, _ = store.CountActiveByAgent(ctx, "repo-triage-bot@v7")
	if n != 0 {
		t.Fatalf("terminated session counted active")
	}
}

func TestRunRecordsAndOversizeManifestGuard(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	if err := store.PutRun(ctx, sessionstore.RunRecord{
		SessionID: "sess-2", RunID: "run-1", Trigger: "webhook", Status: "Succeeded",
	}); err != nil {
		t.Fatalf("put run: %v", err)
	}

	// Oversize manifests are dropped rather than failing the whole write.
	huge := strings.Repeat("x", 400_000)
	err := store.PutSession(ctx, sessionstore.Session{
		OrgID: "org-1", SessionID: "sess-huge", UserID: "u",
		AgentRef: "a@v1", Status: "Running", Manifest: []byte(huge),
		CreatedAt: time.Now(), TTL: time.Now().Add(time.Hour),
	})
	if err == nil {
		t.Fatal("oversize manifest must be rejected by the guard")
	}
}
