package testutil

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/docker/go-connections/nat"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	pgImage  = "postgres:16-alpine"
	redisImg = "redis:7-alpine"
)

// container is the shared handle for harness-managed containers.
type container struct {
	tc testcontainers.Container
}

// Terminate stops and removes the container. Safe to call from t.Cleanup.
func (c *container) Terminate() {
	if err := c.tc.Terminate(context.Background()); err != nil {
		fmt.Printf("testutil: container terminate: %v\n", err)
	}
}

func start(t *testing.T, req testcontainers.ContainerRequest) *container {
	t.Helper()
	ctx := context.Background()
	tc, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start container %s: %v", req.Image, err)
	}
	return &container{tc: tc}
}

// mappedPort returns the host port mapped to the given container port and
// verifies it is reachable.
func mappedPort(t *testing.T, c *container, port string) int {
	t.Helper()
	ctx := context.Background()
	p, err := c.tc.MappedPort(ctx, nat.Port(port))
	if err != nil {
		t.Fatalf("mapped port %s: %v", port, err)
	}
	h, err := c.tc.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	conn, err := net.Dial("tcp", fmt.Sprintf("%s:%s", h, p.Port()))
	if err != nil {
		t.Fatalf("port not reachable: %v", err)
	}
	_ = conn.Close()
	return p.Int()
}

func host(t *testing.T, c *container) string {
	t.Helper()
	h, err := c.tc.Host(context.Background())
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	return h
}

// postgresRequest builds the PG request; kept here so both Postgres and
// migration tests share image/wait config.
var postgresRequest = func(password string) testcontainers.ContainerRequest {
	return testcontainers.ContainerRequest{
		Image:        pgImage,
		Env:          map[string]string{"POSTGRES_PASSWORD": password},
		ExposedPorts: []string{"5432/tcp"},
		WaitingFor:   wait.ForListeningPort("5432/tcp"),
	}
}
