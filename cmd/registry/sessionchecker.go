package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/prav-j/dark-factory/internal/runtoken"
	"github.com/prav-j/dark-factory/internal/sessionstore"
)

func runTokenSecret() string {
	if s := os.Getenv("RUN_TOKEN_SECRET"); s != "" {
		return s
	}
	log.Println("WARNING: RUN_TOKEN_SECRET unset; using insecure dev default")
	return "dev-only-insecure-secret"
}

// newSessionChecker returns the liveness source for run-token renewal.
// With DDB_ENDPOINT configured it reads real session state (strongly
// consistent); otherwise every session is considered alive with a 4h
// deadline — acceptable only for local development.
func newSessionChecker() runtoken.SessionChecker {
	endpoint := os.Getenv("DDB_ENDPOINT")
	if endpoint == "" {
		return staticAliveChecker{}
	}
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(regionOrDefault()),
		config.WithCredentialsProvider(aws.CredentialsProviderFunc(func(_ context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
		})),
	)
	if err != nil {
		log.Fatalf("aws config: %v", err)
	}
	client := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = &endpoint })
	store := sessionstore.New(client)
	if err := store.EnsureTables(context.Background()); err != nil {
		log.Fatalf("ensure ddb tables: %v", err)
	}
	return sessionstore.NewSessionChecker(store, 4*time.Hour)
}

func regionOrDefault() string {
	if r := os.Getenv("AWS_REGION"); r != "" {
		return r
	}
	return "us-east-1"
}

type staticAliveChecker struct{}

func (staticAliveChecker) GetSession(_ context.Context, _ string) (runtoken.SessionInfo, error) {
	return runtoken.SessionInfo{
		Alive:    true,
		Deadline: time.Now().Add(4 * time.Hour),
	}, nil
}
