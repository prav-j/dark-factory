// Package orchestrator schedules agent runs onto the execution plane
// (specs/05, specs/09): Redis Streams queues sharded by user (per-user run
// ordering), interactive traffic prioritized over background, budget checks
// before any sandbox allocation, and idempotent webhook triggers.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	ErrBudgetExceeded = errors.New("budget exceeded: run rejected before allocation")
	ErrNoRun          = errors.New("no run available")
)

// Priority selects a queue class; interactive always drains first.
type Priority string

const (
	Interactive Priority = "interactive"
	Background  Priority = "background"
)

// RunRequest is one unit of scheduling.
type RunRequest struct {
	RunID          string
	SessionID      string
	AgentRef       string // name@version
	UserID         string
	OrgID          string
	Priority       Priority
	IdempotencyKey string // webhook triggers; empty for chat/schedule
	EnqueuedAt     time.Time
}

// BudgetChecker gates runs before any sandbox is allocated.
type BudgetChecker interface {
	CheckBudget(ctx context.Context, orgID, userID string) error
}

// Orchestrator enqueues and dequeues runs across N user-sharded stream pairs.
type Orchestrator struct {
	client  *redis.Client
	shards  int
	group   string
	budget  BudgetChecker
	idemTTL time.Duration
}

func New(client *redis.Client, shards int, budget BudgetChecker) *Orchestrator {
	if shards <= 0 {
		shards = 8
	}
	return &Orchestrator{
		client:  client,
		shards:  shards,
		group:   "workers",
		budget:  budget,
		idemTTL: 24 * time.Hour,
	}
}

func shardFor(userID string, n int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(userID))
	return int(h.Sum32() % uint32(n)) //nolint:gosec // shard count is small
}

func (o *Orchestrator) stream(shard int, p Priority) string {
	return fmt.Sprintf("orch:%d:%s", shard, p)
}

// Enqueue validates budget + idempotency, then appends to the user's shard.
// It returns the effective RunID (idempotent replays return the original).
func (o *Orchestrator) Enqueue(ctx context.Context, req RunRequest) (string, error) {
	if o.budget != nil {
		if err := o.budget.CheckBudget(ctx, req.OrgID, req.UserID); err != nil {
			return "", fmt.Errorf("%w: %v", ErrBudgetExceeded, err)
		}
	}

	if req.IdempotencyKey != "" {
		key := "orch:idem:" + req.IdempotencyKey
		ok, err := o.client.SetNX(ctx, key, req.RunID, o.idemTTL).Result()
		if err != nil {
			return "", err
		}
		if !ok {
			existing, err := o.client.Get(ctx, key).Result()
			if err != nil {
				return "", err
			}
			return existing, nil // duplicate delivery: original run stands
		}
	}

	if req.EnqueuedAt.IsZero() {
		req.EnqueuedAt = time.Now()
	}
	payload, err := jsonMarshal(req)
	if err != nil {
		return "", err
	}
	shard := shardFor(req.UserID, o.shards)
	return req.RunID, o.client.XAdd(ctx, &redis.XAddArgs{
		Stream: o.stream(shard, req.Priority),
		Values: map[string]any{"payload": payload},
	}).Err()
}

// Handler processes one dequeued run.
type Handler func(ctx context.Context, req RunRequest) error

// RunWorker drains interactive queues ahead of background ones for the given
// shard, acking messages after successful handling. Blocks until ctx ends.
func (o *Orchestrator) RunWorker(ctx context.Context, shard int, workerName string, handle Handler) error {
	interactive := o.stream(shard, Interactive)
	background := o.stream(shard, Background)
	for _, s := range []string{interactive, background} {
		if err := o.client.XGroupCreateMkStream(ctx, s, o.group, "0").Err(); err != nil {
			if !isBusyGroup(err) {
				return err
			}
		}
	}
	// go-redis v9 wants: stream1 stream2 ... id1 id2 ...
	// XREADGROUP checks streams in order and returns from the first with
	// available messages, so interactive listed first always drains ahead
	// of background. ">" = only never-delivered messages.
	streams := []string{interactive, background, ">", ">"}

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		res, err := o.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    o.group,
			Consumer: workerName,
			Streams:  streams,
			Count:    1,
			Block:    500 * time.Millisecond,
		}).Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}

		for _, stream := range res {
			for _, msg := range stream.Messages {
				raw, _ := msg.Values["payload"].(string)
				req, err := jsonUnmarshal(raw)
				if err != nil {
					_ = o.client.XAck(ctx, stream.Stream, o.group, msg.ID).Err()
					continue // poison message
				}
				if err := handle(ctx, req); err != nil {
					// Leave un-acked for redelivery; handler must be idempotent.
					continue
				}
				_ = o.client.XAck(ctx, stream.Stream, o.group, msg.ID).Err()
			}
		}
	}
}
