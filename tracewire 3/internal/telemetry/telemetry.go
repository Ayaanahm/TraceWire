// Package telemetry wires up Prometheus metrics and OpenTelemetry tracing
// shared by every tracewire service.
package telemetry

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// LatencyBuckets are dense between 50ms and 500ms, around the gateway SLO
// (p99 < 250ms), so the SLO boundary sits exactly on a bucket edge and alert
// thresholds never depend on interpolation. Measured against exact client-side
// percentiles, Prometheus' default buckets were just as accurate for p95/p99
// at this traffic shape (see docs/histogram-buckets.md); the extra buckets are
// kept for the exact 250ms edge, at the cost of more series per route.
var LatencyBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.125, 0.15, 0.175, 0.2,
	0.225, 0.25, 0.3, 0.35, 0.4, 0.5, 0.75, 1, 2.5, 5,
}

// Telemetry holds a service's metric registry and tracer.
type Telemetry struct {
	Service  string
	Registry *prometheus.Registry
	Tracer   trace.Tracer

	duration *prometheus.HistogramVec
	inflight *prometheus.GaugeVec
	shutdown func(context.Context) error
}

// New creates telemetry for a service. Tracing exports over OTLP/HTTP when
// OTEL_EXPORTER_OTLP_ENDPOINT is set; otherwise spans are created (so trace
// IDs still propagate and appear in logs/exemplars) but not exported.
func New(ctx context.Context, service string) (*Telemetry, error) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	t := &Telemetry{
		Service:  service,
		Registry: reg,
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:        "http_server_request_duration_seconds",
			Help:        "HTTP server request latency.",
			Buckets:     buckets(),
			ConstLabels: prometheus.Labels{"service": service},
		}, []string{"method", "route", "code"}),
		inflight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name:        "http_server_requests_in_flight",
			Help:        "Requests currently being served.",
			ConstLabels: prometheus.Labels{"service": service},
		}, []string{"route"}),
	}
	reg.MustRegister(t.duration, t.inflight)

	// Schemaless on purpose: resource.Default() carries the SDK's semconv
	// schema URL, and merging two different schema URLs is an error. Pinning
	// one here broke the build on the first dependency upgrade.
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName(service),
		semconv.ServiceVersion(Version()),
	))
	if err != nil {
		return nil, err
	}
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampleRatio()))),
	}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		exp, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, err
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}
	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	t.Tracer = tp.Tracer("github.com/Ayaanahm/tracewire/" + service)
	t.shutdown = tp.Shutdown
	return t, nil
}

// buckets returns LatencyBuckets, or Prometheus' defaults when
// LATENCY_BUCKETS=default (used by bench/ to measure the difference).
func buckets() []float64 {
	if os.Getenv("LATENCY_BUCKETS") == "default" {
		return prometheus.DefBuckets
	}
	return LatencyBuckets
}

func sampleRatio() float64 {
	if v, err := strconv.ParseFloat(os.Getenv("TRACE_SAMPLE_RATIO"), 64); err == nil && v >= 0 && v <= 1 {
		return v
	}
	return 1
}

// Version is injected at build time with -ldflags "-X ...telemetry.version=v1.2.3".
var version = "dev"

// Version returns the build version.
func Version() string { return version }

// Shutdown flushes pending spans.
func (t *Telemetry) Shutdown(ctx context.Context) error { return t.shutdown(ctx) }

// MetricsHandler serves /metrics with OpenMetrics enabled so exemplars
// (trace IDs attached to histogram samples) reach Prometheus.
func (t *Telemetry) MetricsHandler() http.Handler {
	return promhttp.HandlerFor(t.Registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

// Wrap instruments a handler for one route: a server span (continuing any
// incoming W3C traceparent), the latency histogram with a trace-ID exemplar,
// and an in-flight gauge. route must be the pattern, not the raw path, so
// label cardinality stays bounded.
func (t *Telemetry) Wrap(route string, h http.Handler) http.Handler {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g := t.inflight.WithLabelValues(route)
		g.Inc()
		defer g.Dec()

		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		start := time.Now()
		h.ServeHTTP(rec, r)
		elapsed := time.Since(start).Seconds()

		obs := t.duration.WithLabelValues(r.Method, route, strconv.Itoa(rec.code))
		sc := trace.SpanContextFromContext(r.Context())
		if eo, ok := obs.(prometheus.ExemplarObserver); ok && sc.IsSampled() {
			eo.ObserveWithExemplar(elapsed, prometheus.Labels{"trace_id": sc.TraceID().String()})
			return
		}
		obs.Observe(elapsed)
	})
	return otelhttp.NewHandler(inner, route)
}

// Client returns an HTTP client that injects trace context into outgoing
// requests, so a single trace spans gateway -> orders -> inventory.
func Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: otelhttp.NewTransport(http.DefaultTransport)}
}
