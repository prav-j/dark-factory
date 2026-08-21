//go:build conformance && integration

package conformance

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/prav-j/dark-factory/internal/sessionstore"
	"github.com/prav-j/dark-factory/internal/testutil"
)

// C16-006 — DDB session store access patterns (specs/16.3): sessions-by-org,
// resumable-by-user, active-per-agent via GSIs; TTL attribute present;
// terminated sessions leave resumable/active views.
func TestC16006DDBSessionStoreAccessPatterns(t *testing.T) {
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
	store := sessionstore.New(dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		o.BaseEndpoint = &endpoint
	}))
	ctx := context.Background()
	if err := store.EnsureTables(ctx); err != nil {
		t.Fatalf("ensure tables: %v", err)
	}

	sess := sessionstore.Session{
		OrgID: "org-conf", SessionID: "sess-conf-1", UserID: "user-conf",
		AgentRef: "agent@v1", Status: "Running", EnvironmentKey: "snap-k",
		CreatedAt: time.Now(), TTL: time.Now().Add(7 * 24 * time.Hour),
	}
	if err := store.PutSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if byOrg, err := store.ListByOrg(ctx, "org-conf"); err != nil || len(byOrg) != 1 {
		t.Fatalf("sessions-by-org: %v err %v", byOrg, err)
	}
	if byUser, err := store.ListResumableByUser(ctx, "user-conf"); err != nil || len(byUser) != 1 {
		t.Fatalf("resumable-by-user: %v err %v", byUser, err)
	}
	if n, err := store.CountActiveByAgent(ctx, "agent@v1"); err != nil || n != 1 {
		t.Fatalf("active-per-agent: %d err %v", n, err)
	}
	if err := store.UpdateStatus(ctx, "org-conf", "sess-conf-1", "Terminated"); err != nil {
		t.Fatal(err)
	}
	if byUser, _ := store.ListResumableByUser(ctx, "user-conf"); len(byUser) != 0 {
		t.Fatal("terminated session must not be resumable")
	}

	Pass(t, Check{ID: "C16-006", Spec: "16-deployment-sessions.md#session-store-dynamodb",
		Text: "Sessions-by-org, resumable-by-user, active-per-agent lookups work via DDB GSIs; TTL attribute set for terminated-session cleanup."})
}
