package runtoken

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisRevocations stores revoked jtis in Redis with TTLs matching each
// token's remaining validity, so the set self-cleans.
type RedisRevocations struct {
	Client *redis.Client
}

// Revoke marks a jti revoked for ttl.
func (r *RedisRevocations) Revoke(ctx context.Context, jti string, ttl time.Duration) error {
	return r.Client.Set(ctx, revocationKey(jti), 1, ttl).Err()
}

// IsRevoked reports whether a jti is on the revocation list.
func (r *RedisRevocations) IsRevoked(ctx context.Context, jti string) (bool, error) {
	n, err := r.Client.Exists(ctx, revocationKey(jti)).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func revocationKey(jti string) string { return fmt.Sprintf("runtoken:revoked:%s", jti) }
