package tracing_test

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/prav-j/dark-factory/internal/tracing"
)

// C10-001: a run produces a complete span tree — every LLM call, tool call,
// and MCP call correlated by run_id.
func TestRunSpanTreeCorrelatedByRunID(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	old := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(old) })

	tracer := tracing.Tracer("test")
	ctx := context.Background()

	runCtx, runSpan := tracing.RunSpan(ctx, tracer, "agent.run", "run-42")
	_, llmSpan := tracing.RunSpan(runCtx, tracer, "llm.call", "run-42")
	llmSpan.End()
	_, toolSpan := tracing.RunSpan(runCtx, tracer, "tool.call", "run-42",
		attribute.String("tool", "web_search"))
	toolSpan.End()
	_, mcpSpan := tracing.RunSpan(runCtx, tracer, "mcp.call", "run-42",
		attribute.String("server", "github-official"))
	mcpSpan.End()
	runSpan.End()
	_ = tp.ForceFlush(ctx)

	spans := recorder.Ended()
	if len(spans) != 4 {
		t.Fatalf("spans = %d, want 4 (run + llm + tool + mcp)", len(spans))
	}

	var parentID = map[string]bool{}
	var found = map[string]string{} // name -> run_id attr
	for _, s := range spans {
		for _, a := range s.Attributes() {
			if string(a.Key) == tracing.RunIDKey {
				found[s.Name()] = a.Value.AsString()
			}
		}
		parentID[s.Name()] = true
	}
	for _, want := range []string{"agent.run", "llm.call", "tool.call", "mcp.call"} {
		if _, ok := found[want]; !ok {
			t.Fatalf("span %q missing or uncorrelated; got %v", want, found)
		}
		if found[want] != "run-42" {
			t.Fatalf("span %q has wrong run_id: %q", want, found[want])
		}
	}
}

func TestExecutorMetricsRender(t *testing.T) {
	m := tracing.NewMetrics()
	m.Observe("executor_build_seconds_total", 3)
	m.Observe("executor_snapshots_restored_total", 7)
	m.Observe("executor_restore_seconds", 0.8) // <=1s bucket
	m.Observe("executor_restore_seconds", 3)   // 5s bucket

	out := m.Render()
	for _, want := range []string{
		"# TYPE executor_build_seconds_total counter",
		"executor_build_seconds_total 3",
		"executor_snapshots_restored_total 7",
		`executor_restore_seconds_bucket{le="1.000"} 1`,
		`executor_restore_seconds_bucket{le="5.000"} 2`,
		`executor_restore_seconds_bucket{le="+Inf"} 2`,
		"executor_restore_seconds_count 2",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
}

func TestWarmHitRatio(t *testing.T) {
	if r := tracing.WarmHitRatio(0, 0); r != 1 {
		t.Fatalf("empty pool ratio = %v, want 1", r)
	}
	if r := tracing.WarmHitRatio(8, 2); r != 0.8 {
		t.Fatalf("ratio = %v, want 0.8", r)
	}
}
