//go:build integration

package secrets_test

import (
	"context"
	"crypto/rand"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/prav-j/dark-factory/internal/secrets"
	"github.com/prav-j/dark-factory/internal/testutil"
)

// KMSRootKeys decrypts per-version root keys through LocalStack KMS; the
// secrets.Manager consumes them transparently.
func TestKMSRootKeyProvider(t *testing.T) {
	endpoint := testutil.DynamoDB(t) // same LocalStack container now serves kms too
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(testutil.TestRegion),
		config.WithCredentialsProvider(aws.CredentialsProviderFunc(func(_ context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: testutil.TestAccessKey, SecretAccessKey: testutil.TestSecretKey}, nil
		})),
	)
	if err != nil {
		t.Fatal(err)
	}
	client := kms.NewFromConfig(cfg, func(o *kms.Options) { o.BaseEndpoint = &endpoint })

	keyOut, err := client.CreateKey(context.Background(), &kms.CreateKeyInput{})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	root := make([]byte, 32)
	if _, err := rand.Read(root); err != nil {
		t.Fatal(err)
	}
	enc, err := client.Encrypt(context.Background(), &kms.EncryptInput{
		KeyId:     keyOut.KeyMetadata.KeyId,
		Plaintext: root,
	})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	provider := secrets.NewKMSRootKeys(client, map[int][]byte{1: enc.CiphertextBlob})
	got, err := provider.RootKey(1)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(got) != string(root) {
		t.Fatal("decrypted root key mismatch")
	}

	// Missing version errors.
	if _, err := provider.RootKey(9); err == nil {
		t.Fatal("unknown KEK version must error")
	}
}
