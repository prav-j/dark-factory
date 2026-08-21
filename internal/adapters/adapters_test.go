//go:build integration

package adapters_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/prav-j/dark-factory/internal/adapters"
	"github.com/prav-j/dark-factory/internal/harness"
	"github.com/prav-j/dark-factory/internal/sessionstore"
	"github.com/prav-j/dark-factory/internal/stophook"
	"github.com/prav-j/dark-factory/internal/testutil"
)

func newDeps(t *testing.T) (*sessionstore.Store, *adapters.S3BlobStore) {
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
	store := sessionstore.New(dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = &endpoint }))
	if err := store.EnsureTables(context.Background()); err != nil {
		t.Fatalf("ensure tables: %v", err)
	}

	s3Client := s3.NewFromConfig(cfg, func(o *s3.Options) { o.BaseEndpoint = &endpoint; o.UsePathStyle = true })
	blobs := &adapters.S3BlobStore{Client: s3Client, Bucket: "dark-factory-blobs"}
	if _, err := blobs.Client.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: &blobs.Bucket}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	return store, blobs
}

func harnessRunState() *harness.RunState {
	return &harness.RunState{
		RunID:     "run-1",
		SessionID: "sess-1",
		Status:    "awaiting_approval",
		Messages:  []harness.Message{{Role: "user", Content: "hi"}},
	}
}

func TestCheckpointRoundtripThroughHarnessTypes(t *testing.T) {
	store, _ := newDeps(t)
	cp := &adapters.HarnessCheckpointer{Store: store}
	ctx := context.Background()

	in := harnessRunState()
	if err := cp.Save(ctx, "run-1", in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := cp.Load(ctx, "run-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if out.RunID != "run-1" || len(out.Messages) != 1 || out.Status != "awaiting_approval" {
		t.Fatalf("roundtrip mismatch: %+v", out)
	}
	if err := cp.Delete(ctx, "run-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := cp.Load(ctx, "run-1"); err == nil {
		t.Fatal("deleted checkpoint must not load")
	}
}

func TestManifestPersisterAndBlobStore(t *testing.T) {
	store, blobs := newDeps(t)
	ctx := context.Background()

	sess := sessionstore.Session{
		OrgID: "org-1", SessionID: "sess-m", UserID: "u",
		AgentRef: "a@v1", Status: "Committing",
		CreatedAt: time.Now(), TTL: time.Now().Add(time.Hour),
	}
	if err := store.PutSession(ctx, sess); err != nil {
		t.Fatal(err)
	}

	persister := &adapters.ManifestPersister{Store: store}
	manifest := []byte(`{"sessionId":"sess-m","endedReason":"idle-timeout"}`)
	hook := stophook.New(nil, blobs, persister, 0) // nil pusher unused here
	_ = hook

	if err := persister.SaveManifest(ctx, "org-1", "sess-m", manifest); err != nil {
		t.Fatalf("save manifest: %v", err)
	}
	got, err := store.GetSession(ctx, "org-1", "sess-m")
	if err != nil || !strings.Contains(string(got.Manifest), "idle-timeout") {
		t.Fatalf("manifest roundtrip: %+v err %v", got, err)
	}

	ref, err := blobs.Upload(ctx, "uncommitted/sess-m/x.diff", []byte("diff-content"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	data, err := blobs.Fetch(ctx, ref)
	if err != nil || string(data) != "diff-content" {
		t.Fatalf("fetch = %q err %v", data, err)
	}
}
