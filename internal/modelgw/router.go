// Package modelgw is the provider-agnostic Model Gateway (specs/02, specs/09):
// routing with retries and provider fallback, per-provider rate limits, and
// token/dollar metering tagged with run_id.
package modelgw

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

var (
	ErrNoProvider   = errors.New("no provider configured for model")
	ErrAllProviders = errors.New("all providers failed")
	ErrRateLimited  = errors.New("rate limited")
)

// Message is one conversation turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// CompletionRequest is a single completion call.
type CompletionRequest struct {
	Model     string
	Messages  []Message
	MaxTokens int
	RunID     string // metering correlation
	Agent     string
	UserID    string // budget attribution
	OrgID     string // budget attribution
}

// CompletionResponse is a successful completion.
type CompletionResponse struct {
	Content      string
	StopReason   string
	InputTokens  int
	OutputTokens int
	Provider     string // which provider served it
}

// Provider serves completions for specific models.
type Provider interface {
	Name() string
	ServesModel(model string) bool
	Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)
}

// UsageRecord is one metered completion.
type UsageRecord struct {
	RunID        string    `json:"runId"`
	Agent        string    `json:"agent"`
	UserID       string    `json:"userId"`
	OrgID        string    `json:"orgId"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	InputTokens  int       `json:"inputTokens"`
	OutputTokens int       `json:"outputTokens"`
	CostUSD      float64   `json:"costUsd"`
	At           time.Time `json:"at"`
}

// Meter receives usage records. The orchestrator's budget checker aggregates
// these (issue #27); tests collect them directly.
type Meter interface {
	Record(ctx context.Context, u UsageRecord)
}

// MemoryMeter is an in-process collector.
type MemoryMeter struct {
	mu       sync.Mutex
	Recorded []UsageRecord
}

func (m *MemoryMeter) Record(_ context.Context, u UsageRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Recorded = append(m.Recorded, u)
}

// Snapshot returns all recorded usage.
func (m *MemoryMeter) Snapshot() []UsageRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]UsageRecord, len(m.Recorded))
	copy(out, m.Recorded)
	return out
}

// PriceTable prices models in USD per million tokens.
type PriceTable map[string]struct{ InputPerM, OutputPerM float64 }

// DefaultPrices covers the launch models; extend via config later.
var DefaultPrices = PriceTable{
	"claude-sonnet-4-5": {InputPerM: 3.0, OutputPerM: 15.0},
}

func cost(model string, in, out int) float64 {
	p, ok := DefaultPrices[model]
	if !ok {
		return 0
	}
	return float64(in)/1e6*p.InputPerM + float64(out)/1e6*p.OutputPerM
}

// Router is the gateway: rate-limited primary + ordered fallbacks with retries.
type Router struct {
	primary  limitedProvider
	fallback []limitedProvider
	retries  int
	backoff  func(attempt int) time.Duration
	meter    Meter
}

type limitedProvider struct {
	p Provider
	l *rate.Limiter
}

// NewRouter builds a gateway around a primary provider and optional
// fallbacks, each with its own per-second rate limit.
func NewRouter(primary Provider, qps float64, fallbacks ...Provider) (*Router, error) {
	if primary == nil {
		return nil, ErrNoProvider
	}
	r := &Router{
		primary:  limitedProvider{p: primary, l: rate.NewLimiter(rate.Limit(qps), 10)},
		fallback: make([]limitedProvider, 0, len(fallbacks)),
		retries:  2,
		backoff:  func(int) time.Duration { return time.Second },
		meter:    nopMeter{},
	}
	for _, f := range fallbacks {
		r.fallback = append(r.fallback, limitedProvider{p: f, l: rate.NewLimiter(rate.Limit(qps), 10)})
	}
	return r, nil
}

// SetMeter attaches usage metering after construction.
func (r *Router) SetMeter(m Meter) { r.meter = m }

// SetRetries configures attempts per provider.
func (r *Router) SetRetries(n int) { r.retries = n }

// SetBackoff overrides backoff timing (tests use zero delay).
func (r *Router) SetBackoff(f func(int) time.Duration) { r.backoff = f }

type nopMeter struct{}

func (nopMeter) Record(context.Context, UsageRecord) {}

// Complete routes the request: retry the primary with backoff, then walk
// fallbacks in order. Only successful completions are metered.
func (r *Router) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	candidates := append([]limitedProvider{r.primary}, r.fallback...)

	var lastErr error
	for _, cand := range candidates {
		if !cand.p.ServesModel(req.Model) {
			continue
		}
		for attempt := 0; attempt <= r.retries; attempt++ {
			if err := cand.l.Wait(ctx); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrRateLimited, err)
			}
			resp, err := cand.p.Complete(ctx, req)
			if err == nil {
				resp.Provider = cand.p.Name()
				r.meter.Record(ctx, UsageRecord{
					RunID: req.RunID, Agent: req.Agent,
					UserID: req.UserID, OrgID: req.OrgID,
					Provider: resp.Provider, Model: req.Model,
					InputTokens: resp.InputTokens, OutputTokens: resp.OutputTokens,
					CostUSD: cost(req.Model, resp.InputTokens, resp.OutputTokens),
					At:      time.Now(),
				})
				return resp, nil
			}
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			time.Sleep(r.backoff(attempt))
		}
	}
	if lastErr == nil {
		return nil, ErrNoProvider
	}
	return nil, fmt.Errorf("%w: last error: %v", ErrAllProviders, lastErr)
}
