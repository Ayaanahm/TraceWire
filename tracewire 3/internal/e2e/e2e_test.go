// Package e2e runs gateway -> orders -> inventory in-process and checks the
// full request path, trace propagation and metrics.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Ayaanahm/tracewire/internal/faults"
	"github.com/Ayaanahm/tracewire/internal/gateway"
	"github.com/Ayaanahm/tracewire/internal/inventory"
	"github.com/Ayaanahm/tracewire/internal/orders"
	"github.com/Ayaanahm/tracewire/internal/telemetry"
)

type stack struct {
	gw, ord, inv *httptest.Server
	invFaults    *faults.Injector
	spans        *tracetest.SpanRecorder
	tel          map[string]*telemetry.Telemetry
}

func newStack(t *testing.T) *stack {
	t.Helper()
	ctx := context.Background()
	s := &stack{invFaults: faults.New(), tel: map[string]*telemetry.Telemetry{}}
	for _, name := range []string{"gateway", "orders", "inventory"} {
		tel, err := telemetry.New(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		s.tel[name] = tel
	}
	// Record every span from every service in one provider.
	s.spans = tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(s.spans)))
	for _, tel := range s.tel {
		tel.Tracer = otel.Tracer("test")
	}

	invMux := inventory.NewMux(s.tel["inventory"], inventory.NewStore(inventory.DefaultSeed()), s.invFaults)
	invMux.Handle("GET /metrics", s.tel["inventory"].MetricsHandler())
	s.inv = httptest.NewServer(invMux)
	svc := orders.New(s.tel["orders"], s.inv.URL, telemetry.Client(time.Second))
	s.ord = httptest.NewServer(orders.NewMux(s.tel["orders"], svc, faults.New()))
	s.gw = httptest.NewServer(gateway.NewMux(s.tel["gateway"], s.ord.URL, telemetry.Client(2*time.Second)))
	t.Cleanup(func() { s.gw.Close(); s.ord.Close(); s.inv.Close() })
	return s
}

func post(t *testing.T, url, body string) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestCreateAndFetchOrder(t *testing.T) {
	s := newStack(t)
	resp, body := post(t, s.gw.URL+"/api/orders", `{"sku":"sku-keyboard","qty":2}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: got %d %v", resp.StatusCode, body)
	}
	id, _ := body["id"].(string)
	got, err := http.Get(s.gw.URL + "/api/orders/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	if got.StatusCode != http.StatusOK {
		t.Fatalf("fetch: got %d", got.StatusCode)
	}
}

func TestErrorMapping(t *testing.T) {
	s := newStack(t)
	cases := []struct {
		name, body string
		want       int
	}{
		{"bad json", `{`, http.StatusBadRequest},
		{"zero qty", `{"sku":"sku-mouse","qty":0}`, http.StatusBadRequest},
		{"unknown sku", `{"sku":"nope","qty":1}`, http.StatusUnprocessableEntity},
		{"out of stock", `{"sku":"sku-limited","qty":99}`, http.StatusConflict},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if resp, b := post(t, s.gw.URL+"/api/orders", c.body); resp.StatusCode != c.want {
				t.Fatalf("got %d want %d (%v)", resp.StatusCode, c.want, b)
			}
		})
	}
}

func TestInventoryFaultSurfacesAs502(t *testing.T) {
	s := newStack(t)
	if err := s.invFaults.Set(faults.Config{ErrorRate: 1}); err != nil {
		t.Fatal(err)
	}
	if resp, _ := post(t, s.gw.URL+"/api/orders", `{"sku":"sku-mouse","qty":1}`); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("got %d want 502", resp.StatusCode)
	}
}

func TestConcurrentReservationsNeverOversell(t *testing.T) {
	s := newStack(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	created := 0
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, _ := post(t, s.gw.URL+"/api/orders", `{"sku":"sku-limited","qty":1}`)
			if resp.StatusCode == http.StatusCreated {
				mu.Lock()
				created++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if created != 3 {
		t.Fatalf("created %d orders for 3 units of stock", created)
	}
}

func TestOneTraceSpansAllServices(t *testing.T) {
	s := newStack(t)
	post(t, s.gw.URL+"/api/orders", `{"sku":"sku-mouse","qty":1}`)
	ended := s.spans.Ended()
	if len(ended) < 5 { // 3 server spans + 2 client spans + custom span
		t.Fatalf("expected >=5 spans, got %d", len(ended))
	}
	tid := ended[0].SpanContext().TraceID()
	names := map[string]bool{}
	for _, sp := range ended {
		if sp.SpanContext().TraceID() != tid {
			t.Fatalf("span %q in different trace", sp.Name())
		}
		names[sp.Name()] = true
	}
	for _, want := range []string{"POST /api/orders", "POST /orders", "POST /reserve", "orders.reserve_stock"} {
		if !names[want] {
			t.Errorf("missing span %q; got %v", want, names)
		}
	}
}

func TestMetricsExposeHistogramWithExemplars(t *testing.T) {
	s := newStack(t)
	post(t, s.gw.URL+"/api/orders", `{"sku":"sku-mouse","qty":1}`)
	req, _ := http.NewRequest(http.MethodGet, s.inv.URL+"/metrics", nil)
	req.Header.Set("Accept", "application/openmetrics-text; version=1.0.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	for _, want := range []string{
		`http_server_request_duration_seconds_bucket{code="200",method="POST",route="/reserve",service="inventory",le="0.25"}`,
		`# {trace_id="`,
	} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("metrics missing %q", want)
		}
	}
}
