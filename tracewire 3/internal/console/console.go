// Package console serves the browser UI at "/" and the small JSON API behind
// it: live per-second stats for the order path, a built-in traffic generator,
// and (only when CONSOLE_CONTROLS=true) a proxy to inventory's fault switch.
package console

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/Ayaanahm/tracewire/internal/httpx"
)

//go:embed static
var static embed.FS

const window = 60 // seconds of history kept

type sample struct {
	total, orders, inventory float64 // ms
	failed                   bool
	traceID                  string
	at                       time.Time
}

// Slowest is the slowest request of the last 10 seconds, split by service.
type Slowest struct {
	TraceID   string  `json:"trace_id"`
	Total     float64 `json:"total"`
	Gateway   float64 `json:"gateway"`
	Orders    float64 `json:"orders"`
	Inventory float64 `json:"inventory"`
	Failed    bool    `json:"failed"`
	AgoMS     int64   `json:"ago_ms"`
}

type second struct {
	unix    int64
	samples []sample
}

// Recorder keeps the last 60 seconds of order requests in a ring.
type Recorder struct {
	mu   sync.Mutex
	ring [window]second
	now  func() time.Time
}

// NewRecorder returns an empty recorder.
func NewRecorder() *Recorder { return &Recorder{now: time.Now} }

func (r *Recorder) add(s sample) {
	ts := r.now().Unix()
	r.mu.Lock()
	defer r.mu.Unlock()
	slot := &r.ring[ts%window]
	if slot.unix != ts {
		*slot = second{unix: ts}
	}
	slot.samples = append(slot.samples, s)
}

// Point is one second of the chart.
type Point struct {
	T     int64   `json:"t"`
	Count int     `json:"count"`
	Errs  int     `json:"errors"`
	P50   float64 `json:"p50"`
	P99   float64 `json:"p99"`
}

// Hop is a recent-latency summary for one service on the path.
type Hop struct {
	P50 float64 `json:"p50"`
	P99 float64 `json:"p99"`
}

// Stats is the response of GET /console/api/stats.
type Stats struct {
	Series    []Point        `json:"series"`
	Hops      map[string]Hop `json:"hops"` // over the last 10s
	RPS       float64        `json:"rps"`
	ErrRate   float64        `json:"error_rate"`
	Slowest   *Slowest       `json:"slowest,omitempty"`
	Traffic   int            `json:"traffic_rps"`
	Controls  bool           `json:"controls"`
	JaegerURL string         `json:"jaeger_url,omitempty"`
}

func pct(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	slices.Sort(v)
	return v[int(p*float64(len(v)-1))]
}

func (r *Recorder) snapshot() (series []Point, hops map[string]Hop, rps, errRate float64, slow *Slowest) {
	now := r.now().Unix()
	r.mu.Lock()
	defer r.mu.Unlock()
	var tot, ord, inv []float64
	recentN, recentErr := 0, 0
	for t := now - window + 1; t <= now; t++ {
		slot := r.ring[t%window]
		p := Point{T: t}
		if slot.unix == t {
			lat := make([]float64, 0, len(slot.samples))
			for _, s := range slot.samples {
				lat = append(lat, s.total)
				if s.failed {
					p.Errs++
				}
				if t > now-10 && t < now { // last 10 complete seconds
					tot, ord, inv = append(tot, s.total), append(ord, s.orders), append(inv, s.inventory)
					if slow == nil || s.total > slow.Total {
						slow = &Slowest{TraceID: s.traceID, Total: s.total, Gateway: s.total - s.orders,
							Orders: s.orders - s.inventory, Inventory: s.inventory, Failed: s.failed,
							AgoMS: r.now().Sub(s.at).Milliseconds()}
					}
					recentN++
					if s.failed {
						recentErr++
					}
				}
			}
			p.Count = len(lat)
			p.P50, p.P99 = pct(lat, .5), pct(lat, .99)
		}
		series = append(series, p)
	}
	// Gateway's own share is total minus the time spent waiting on orders.
	gw := make([]float64, len(tot))
	for i := range tot {
		gw[i] = tot[i] - ord[i]
	}
	// Orders' own share is its time minus the time spent waiting on inventory.
	own := make([]float64, len(ord))
	for i := range ord {
		own[i] = ord[i] - inv[i]
	}
	hops = map[string]Hop{
		"gateway":   {pct(gw, .5), pct(gw, .99)},
		"orders":    {pct(own, .5), pct(own, .99)},
		"inventory": {pct(inv, .5), pct(inv, .99)},
		"total":     {pct(tot, .5), pct(tot, .99)},
	}
	if recentN > 0 {
		rps = float64(recentN) / 9
		errRate = float64(recentErr) / float64(recentN)
	}
	return
}

type capture struct {
	http.ResponseWriter
	code int
}

func (c *capture) WriteHeader(code int) { c.code = code; c.ResponseWriter.WriteHeader(code) }

// parseTiming reads "orders;dur=12.3, inventory;dur=4.5".
func parseTiming(h string) map[string]float64 {
	out := map[string]float64{}
	for _, part := range strings.Split(h, ",") {
		name, rest, ok := strings.Cut(strings.TrimSpace(part), ";dur=")
		if !ok {
			continue
		}
		if v, err := strconv.ParseFloat(rest, 64); err == nil {
			out[name] = v
		}
	}
	return out
}

// Record wraps the order-creation handler so every request lands in the chart.
func (r *Recorder) Record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c := &capture{ResponseWriter: w, code: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(c, req)
		t := parseTiming(w.Header().Get("Server-Timing"))
		var tid string
		if sc := trace.SpanContextFromContext(req.Context()); sc.IsSampled() {
			tid = sc.TraceID().String()
		}
		r.add(sample{
			traceID:   tid,
			at:        time.Now(),
			total:     float64(time.Since(start).Microseconds()) / 1000,
			orders:    t["orders"],
			inventory: t["inventory"],
			failed:    c.code >= 500,
		})
	})
}

// Traffic sends orders to the gateway at a steady rate, so the console works
// without a separate load generator.
type Traffic struct {
	mu     sync.Mutex
	rps    int
	stop   chan struct{}
	target string
	client *http.Client
}

// MaxTrafficRPS caps the built-in generator.
const MaxTrafficRPS = 200

// NewTraffic targets the gateway's own order endpoint.
func NewTraffic(target string) *Traffic {
	return &Traffic{target: target, client: &http.Client{Timeout: 5 * time.Second}}
}

// Rate returns the current rate.
func (t *Traffic) Rate() int { t.mu.Lock(); defer t.mu.Unlock(); return t.rps }

// Set changes the rate; 0 stops it.
func (t *Traffic) Set(rps int) {
	rps = max(0, min(rps, MaxTrafficRPS))
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stop != nil {
		close(t.stop)
		t.stop = nil
	}
	t.rps = rps
	if rps == 0 {
		return
	}
	stop := make(chan struct{})
	t.stop = stop
	go func() {
		tick := time.NewTicker(time.Second / time.Duration(rps))
		defer tick.Stop()
		skus := []string{"sku-keyboard", "sku-mouse", "sku-monitor"}
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				go func() {
					body := fmt.Sprintf(`{"sku":%q,"qty":1}`, skus[rand.IntN(len(skus))])
					resp, err := t.client.Post(t.target, "application/json", strings.NewReader(body))
					if err == nil {
						_, _ = io.Copy(io.Discard, resp.Body)
						resp.Body.Close()
					}
				}()
			}
		}
	}()
}

// Options configures the console.
type Options struct {
	Recorder *Recorder
	Traffic  *Traffic
	// FaultsURL is inventory's /admin/faults. Controls are exposed only when
	// Controls is true; never enable that on a public deployment.
	FaultsURL string
	Controls  bool
	Client    *http.Client
	// JaegerURL, if set, lets the console link the slowest request to its trace.
	JaegerURL string
}

// Mount adds the UI and its API to mux.
func Mount(mux *http.ServeMux, o Options) {
	sub, _ := fs.Sub(static, "static")
	files := http.FileServerFS(sub)
	mux.Handle("GET /", files)

	mux.HandleFunc("GET /console/api/stats", func(w http.ResponseWriter, _ *http.Request) {
		series, hops, rps, er, slow := o.Recorder.snapshot()
		httpx.JSON(w, http.StatusOK, Stats{Series: series, Hops: hops, RPS: rps, ErrRate: er, Slowest: slow, Traffic: o.Traffic.Rate(), Controls: o.Controls, JaegerURL: o.JaegerURL})
	})

	mux.HandleFunc("PUT /console/api/traffic", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RPS int `json:"rps"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256)).Decode(&body); err != nil {
			httpx.Error(w, http.StatusBadRequest, "send {\"rps\": 0-200}")
			return
		}
		o.Traffic.Set(body.RPS)
		httpx.JSON(w, http.StatusOK, map[string]int{"rps": o.Traffic.Rate()})
	})

	proxy := func(w http.ResponseWriter, r *http.Request) {
		if !o.Controls {
			httpx.Error(w, http.StatusForbidden, "fault controls are turned off on this deployment")
			return
		}
		var body io.Reader
		if r.Method == http.MethodPut {
			b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<10))
			if err != nil {
				httpx.Error(w, http.StatusBadRequest, "body too large")
				return
			}
			body = bytes.NewReader(b)
		}
		req, _ := http.NewRequestWithContext(r.Context(), r.Method, o.FaultsURL, body)
		resp, err := o.Client.Do(req)
		if err != nil {
			httpx.Error(w, http.StatusBadGateway, "inventory is not reachable")
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}
	mux.HandleFunc("GET /console/api/faults", proxy)
	mux.HandleFunc("PUT /console/api/faults", proxy)
	mux.HandleFunc("DELETE /console/api/faults", proxy)
}
