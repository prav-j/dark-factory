// Package tracing provides OpenTelemetry instrumentation for runs
// (specs/10): every LLM call, tool call, and MCP call in a run is a span
// correlated by the run_id attribute, plus executor metrics (build/restore
// time, warm-hit ratio) exposed in Prometheus text format.
package tracing

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// RunIDKey is the correlation attribute every span in a run carries.
const RunIDKey = "dark-factory.run_id"

// InfBound sentinel bucket key for +Inf.
const InfBound = int64(1) << 62

// Init configures the global tracer provider with a stdout exporter (dev).
// Tests inject their own provider; production sets OTLP via env.
func Init(ctx context.Context) (shutdown func(context.Context) error, err error) {
	exp, err := stdouttrace.New()
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp))
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// Tracer returns a named tracer from the global provider.
func Tracer(name string) trace.Tracer { return otel.Tracer(name) }

// RunSpan starts a child span correlated to a run.
func RunSpan(ctx context.Context, tracer trace.Tracer, name string, runID string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	attrs = append([]attribute.KeyValue{attribute.String(RunIDKey, runID)}, attrs...)
	return tracer.Start(ctx, name, trace.WithAttributes(attrs...))
}

// --- executor metrics (Prometheus text exposition) ---

type metricKind int

const (
	counterKind metricKind = iota
	histogramKind
)

type metric struct {
	help   string
	kind   metricKind
	value  float64          // counter total
	bounds []int64          // histogram upper bounds in ms, ascending, Inf last
	counts map[int64]uint64 // cumulative per bound
	sum    float64
	count  uint64
}

// Metrics is an in-process registry with Prometheus text output.
type Metrics struct {
	mu      sync.Mutex
	metrics map[string]*metric
}

func NewMetrics() *Metrics {
	m := &Metrics{metrics: map[string]*metric{}}
	m.Counter("executor_build_seconds_total", "environment image builds completed")
	m.Counter("executor_snapshots_restored_total", "snapshot restores completed")
	m.Histogram("executor_restore_seconds", "snapshot restore latency",
		[]float64{0.5, 1, 2, 5, 10})
	return m
}

func (m *Metrics) Counter(name, help string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.metrics[name] = &metric{help: help, kind: counterKind}
}

func (m *Metrics) Histogram(name, help string, boundsSeconds []float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var bounds []int64
	for _, s := range boundsSeconds {
		bounds = append(bounds, int64(s*1000))
	}
	bounds = append(bounds, InfBound)
	m.metrics[name] = &metric{
		help: help, kind: histogramKind,
		bounds: bounds, counts: map[int64]uint64{},
	}
}

// Observe records into counters/histograms (values in seconds).
func (m *Metrics) Observe(name string, valueSeconds float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	met, ok := m.metrics[name]
	if !ok {
		return
	}
	switch met.kind {
	case counterKind:
		met.value += valueSeconds
	case histogramKind:
		ms := valueSeconds * 1000
		met.sum += ms
		met.count++
		for _, bound := range met.bounds {
			if ms <= float64(bound) || bound == InfBound {
				met.counts[bound]++
			}
		}
	}
}

// Render outputs the Prometheus text exposition format.
func (m *Metrics) Render() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	names := make([]string, 0, len(m.metrics))
	for n := range m.metrics {
		names = append(names, n)
	}
	sort.Strings(names)

	out := ""
	for _, name := range names {
		met := m.metrics[name]
		kindLabel := "histogram"
		if met.kind == counterKind {
			kindLabel = "counter"
		}
		out += fmt.Sprintf("# HELP %s %s\n# TYPE %s %s\n", name, met.help, name, kindLabel)
		if met.kind == counterKind {
			out += fmt.Sprintf("%s %v\n", name, met.value)
			continue
		}
		for _, bound := range met.bounds {
			le := fmt.Sprintf("%.3f", float64(bound)/1000)
			if bound == InfBound {
				le = "+Inf"
			}
			out += fmt.Sprintf("%s_bucket{le=%q} %d\n", name, le, met.counts[bound])
		}
		out += fmt.Sprintf("%s_sum %v\n%s_count %d\n", name, met.sum, name, met.count)
	}
	return out
}

// WarmHitRatio computes warm-hit ratio from counters maintained by callers:
// hits / (hits + misses). Kept here so dashboards have one definition.
func WarmHitRatio(hits, misses uint64) float64 {
	total := hits + misses
	if total == 0 {
		return 1 // nothing served yet; treat as fully warm-capable
	}
	return float64(hits) / float64(total)
}
