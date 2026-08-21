package grants

import (
	"context"
	"sync"
	"time"
)

// ScopeCache is a per-user cache of active scopes for gateways. Entries live
// at most TTL; Revoke-invalidation propagates via the version counter so a
// cached view never outlives a revocation by more than TTL (specs/04: <5s).
type ScopeCache struct {
	store   *Store
	ttl     time.Duration
	mu      sync.RWMutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	scopes   []string
	loadedAt time.Time
}

// NewScopeCache returns a cache backed by store with the given TTL.
func NewScopeCache(store *Store, ttl time.Duration) *ScopeCache {
	return &ScopeCache{store: store, ttl: ttl, entries: map[string]cacheEntry{}}
}

// ActiveScopes returns the user's active scopes, serving from cache when the
// entry is fresh.
func (c *ScopeCache) ActiveScopes(ctx context.Context, orgID, userID string) ([]string, error) {
	key := orgID + "/" + userID

	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if ok && time.Since(entry.loadedAt) < c.ttl {
		return entry.scopes, nil
	}

	scopes, err := c.store.ActiveScopes(ctx, orgID, userID)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.entries[key] = cacheEntry{scopes: scopes, loadedAt: time.Now()}
	c.mu.Unlock()
	return scopes, nil
}

// Invalidate drops the cached view for a user. Called on every revoke so the
// next read after invalidation is authoritative even within TTL.
func (c *ScopeCache) Invalidate(orgID, userID string) {
	c.mu.Lock()
	delete(c.entries, orgID+"/"+userID)
	c.mu.Unlock()
}
