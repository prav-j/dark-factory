package modelgw_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/prav-j/dark-factory/internal/modelgw"
)

// flakyProvider fails the first N calls, then succeeds.
type flakyProvider struct {
	name      string
	model     string
	failFirst int
	calls     int
}

func (f *flakyProvider) Name() string             { return f.name }
func (f *flakyProvider) ServesModel(m string) bool { return m == f.model }
func (f *flakyProvider) Complete(_ context.Context, _ modelgw.CompletionRequest) (*modelgw.CompletionResponse, error) {
	f.calls++
	if f.calls <= f.failFirst {
		return nil, fmt.Errorf("provider %s transient failure %d", f.name, f.calls)
	}
	return &modelgw.CompletionResponse{
		Content: "ok from " + f.name, StopReason: "end_turn",
		InputTokens: 100, OutputTokens: 50,
	}, nil
}

type alwaysFail struct{ name string }

func (a *alwaysFail) Name() string               { return a.name }
func (a *alwaysFail) ServesModel(string) bool    { return true }
func (a *alwaysFail) Complete(context.Context, modelgw.CompletionRequest) (*modelgw.CompletionResponse, error) {
	return nil, errors.New("hard down")
}

const testModel = "claude-sonnet-4-5"

func newTestRouter(t *testing.T, primary modelgw.Provider, fallbacks ...modelgw.Provider) (*modelgw.Router, *modelgw.MemoryMeter) {
	t.Helper()
	r, err := modelgw.NewRouter(primary, 1000 /* high qps */, fallbacks...)
	if err != nil {
		t.Fatal(err)
	}
	meter := &modelgw.MemoryMeter{}
	r.SetMeter(meter)
	r.SetRetries(3)
	r.SetBackoff(func(int) time.Duration { return 0 }) // instant in tests
	return r, meter
}

func TestRetryOnFlakyProvider(t *testing.T) {
	flaky := &flakyProvider{name: "primary", model: testModel, failFirst: 2}
	r, meter := newTestRouter(t, flaky)

	resp, err := r.Complete(context.Background(), modelgw.CompletionRequest{
		Model: testModel, RunID: "run-1", Agent: "bot",
		Messages: []modelgw.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Provider != "primary" || flaky.calls != 3 {
		t.Fatalf("resp=%+v calls=%d, want success on attempt 3", resp, flaky.calls)
	}

	usage := meter.Snapshot()
	if len(usage) != 1 {
		t.Fatalf("usage records = %d, want 1 (only successes metered)", len(usage))
	}
	u := usage[0]
	if u.RunID != "run-1" || u.InputTokens != 100 || u.OutputTokens != 50 || u.CostUSD <= 0 {
		t.Fatalf("usage record wrong: %+v", u)
	}
}

func TestFallbackWhenPrimaryHardDown(t *testing.T) {
	r, meter := newTestRouter(t,
		&alwaysFail{name: "primary"},
		&flakyProvider{name: "fallback", model: testModel, failFirst: 0},
	)
	resp, err := r.Complete(context.Background(), modelgw.CompletionRequest{
		Model: testModel, RunID: "run-2",
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Provider != "fallback" {
		t.Fatalf("served by %q, want fallback", resp.Provider)
	}
	if len(meter.Snapshot()) != 1 {
		t.Fatal("expected one usage record")
	}
}

func TestNoProviderForModel(t *testing.T) {
	r, _ := newTestRouter(t, &flakyProvider{name: "p", model: "other-model"})
	if _, err := r.Complete(context.Background(), modelgw.CompletionRequest{Model: testModel}); err == nil {
		t.Fatal("unroutable model must fail")
	}
}

func TestAllProvidersDown(t *testing.T) {
	r, meter := newTestRouter(t, &alwaysFail{name: "p1"}, &alwaysFail{name: "p2"})
	_, err := r.Complete(context.Background(), modelgw.CompletionRequest{Model: testModel})
	if !errors.Is(err, modelgw.ErrAllProviders) {
		t.Fatalf("err = %v, want ErrAllProviders", err)
	}
	if len(meter.Snapshot()) != 0 {
		t.Fatal("failed attempts must not be metered as usage")
	}
}

// TestUsageReconciliation mirrors the acceptance criterion that usage
// records reconcile with accounting: N successful completions at known token
// counts must sum exactly.
func TestUsageReconciliation(t *testing.T) {
	p := &flakyProvider{name: "p", model: testModel} // never fails
	r, meter := newTestRouter(t, p)

	const n = 10
	for i := 0; i < n; i++ {
		if _, err := r.Complete(context.Background(), modelgw.CompletionRequest{
			Model: testModel, RunID: fmt.Sprintf("run-%d", i),
		}); err != nil {
			t.Fatal(err)
		}
	}

	var in, out int
	var cost float64
	for _, u := range meter.Snapshot() {
		in += u.InputTokens
		out += u.OutputTokens
		cost += u.CostUSD
	}
	if in != n*100 || out != n*50 {
		t.Fatalf("token totals in=%d out=%d, want %d/%d", in, out, n*100, n*50)
	}
	wantCost := float64(n)*modelgw.DefaultPrices[testModel].InputPerM*100/1e6 +
		float64(n)*modelgw.DefaultPrices[testModel].OutputPerM*50/1e6
	if diff := cost - wantCost; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("cost = %f, want %f", cost, wantCost)
	}
}
