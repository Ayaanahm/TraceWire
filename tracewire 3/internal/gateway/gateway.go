// Package gateway is the public edge: it forwards /api/* to backend services,
// propagating trace context and mapping upstream failures.
package gateway

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Ayaanahm/tracewire/internal/httpx"
	"github.com/Ayaanahm/tracewire/internal/telemetry"
)

// NewMux builds the gateway routes. onCreate, if given, wraps order creation
// (the console uses it to record live stats).
func NewMux(t *telemetry.Telemetry, ordersURL string, client *http.Client, onCreate ...func(http.Handler) http.Handler) *http.ServeMux {
	mux := http.NewServeMux()

	forward := func(target func(*http.Request) string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			req, err := http.NewRequestWithContext(r.Context(), r.Method, target(r), http.MaxBytesReader(w, r.Body, 1<<14))
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "bad upstream request")
				return
			}
			req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
			start := time.Now()
			resp, err := client.Do(req)
			if err != nil {
				httpx.Error(w, http.StatusBadGateway, "orders service unavailable")
				return
			}
			defer resp.Body.Close()
			w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
			timing := fmt.Sprintf("orders;dur=%.2f", float64(time.Since(start).Microseconds())/1000)
			if up := resp.Header.Get("Server-Timing"); up != "" {
				timing += ", " + up
			}
			w.Header().Set("Server-Timing", timing)
			w.WriteHeader(resp.StatusCode)
			_, _ = io.Copy(w, resp.Body)
		})
	}

	var create http.Handler = forward(func(*http.Request) string { return ordersURL + "/orders" })
	for _, wrap := range onCreate {
		create = wrap(create)
	}
	mux.Handle("POST /api/orders", t.Wrap("/api/orders", create))
	mux.Handle("GET /api/orders/{id}", t.Wrap("/api/orders/{id}", forward(func(r *http.Request) string {
		return ordersURL + "/orders/" + r.PathValue("id")
	})))
	return mux
}
