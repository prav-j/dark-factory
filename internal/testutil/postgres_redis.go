package testutil

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Postgres starts a throwaway PostgreSQL 16 container and returns a DSN.
// The database is empty; callers apply migrations (issue #5 adds a helper).
func Postgres(t *testing.T) (dsn string) {
	t.Helper()
	pw := randomToken(16)
	c := start(t, postgresRequest(pw))
	t.Cleanup(c.Terminate)

	port := mappedPort(t, c, "5432/tcp")
	return fmt.Sprintf("postgres://postgres:%s@%s:%d/postgres?sslmode=disable", pw, host(t, c), port)
}

// Redis starts a throwaway Redis 7 container and returns a client whose
// keyspace is flushed on cleanup.
func Redis(t *testing.T) *redis.Client {
	t.Helper()
	c := start(t, testcontainers.ContainerRequest{
		Image:        redisImg,
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForListeningPort("6379/tcp"),
	})
	t.Cleanup(c.Terminate)

	addr := fmt.Sprintf("%s:%d", host(t, c), mappedPort(t, c, "6379/tcp"))
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis ping: %v", err)
	}
	return client
}

func randomToken(n int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}
