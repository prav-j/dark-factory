package testutil

import (
	"fmt"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const localImg = "localstack/localstack:3.2"

// DynamoDB starts a LocalStack container with DDB enabled and returns an
// endpoint URL.
func DynamoDB(t *testing.T) (endpoint string) {
	t.Helper()
	c := start(t, testcontainers.ContainerRequest{
		Image:        localImg,
		Env:          map[string]string{"SERVICES": "dynamodb"},
		ExposedPorts: []string{"4566/tcp"},
		WaitingFor:   wait.ForListeningPort("4566/tcp"),
	})
	t.Cleanup(c.Terminate)

	return fmt.Sprintf("http://%s:%d", host(t, c), mappedPort(t, c, "4566/tcp"))
}

// TestAWSConfig values for LocalStack clients. Credentials are static
// placeholders; LocalStack does not validate them and they are not secrets.
const (
	TestAccessKey = "test"
	TestSecretKey = "test"
	TestRegion    = "us-east-1"
)
