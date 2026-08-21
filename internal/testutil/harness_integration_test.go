//go:build integration

package testutil_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/redis/go-redis/v9"

	"github.com/prav-j/dark-factory/internal/testutil"
)

// ExampleIntegration exercises all three container helpers so the harness
// itself is validated on every CI run (issue #2 acceptance).
func TestHarnessContainers(t *testing.T) {
	ctx := context.Background()

	dsn := testutil.Postgres(t)
	if dsn == "" {
		t.Fatal("empty postgres DSN")
	}

	rdb := testutil.Redis(t)
	if err := rdb.Set(ctx, "harness", "ok", 0).Err(); err != nil {
		t.Fatalf("redis set: %v", err)
	}
	got, err := rdb.Get(ctx, "harness").Result()
	if err != nil || got != "ok" {
		t.Fatalf("redis roundtrip: got %q err %v", got, err)
	}
	var _ *redis.Client = rdb

	endpoint := testutil.DynamoDB(t)
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(testutil.TestRegion),
		config.WithCredentialsProvider(aws.CredentialsProviderFunc(func(_ context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: testutil.TestAccessKey, SecretAccessKey: testutil.TestSecretKey}, nil
		})),
	)
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	client := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = &endpoint })
	if _, err := client.ListTables(ctx, &dynamodb.ListTablesInput{}); err != nil {
		t.Fatalf("ddb list tables: %v", err)
	}
}
