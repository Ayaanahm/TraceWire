// Package inventory is the leaf service: it owns stock levels and handles
// reservations atomically.
package inventory

import (
	"encoding/json"
	"net/http"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/Ayaanahm/tracewire/internal/faults"
	"github.com/Ayaanahm/tracewire/internal/httpx"
	"github.com/Ayaanahm/tracewire/internal/telemetry"
)

// Store is an in-memory, concurrency-safe stock table.
type Store struct {
	mu    sync.Mutex
	stock map[string]int
}

// NewStore seeds a store.
func NewStore(seed map[string]int) *Store {
	s := &Store{stock: make(map[string]int, len(seed))}
	for k, v := range seed {
		s.stock[k] = v
	}
	return s
}

// Get returns stock for sku.
func (s *Store) Get(sku string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.stock[sku]
	return n, ok
}

// Reserve decrements stock if enough is available.
func (s *Store) Reserve(sku string, qty int) (remaining int, ok bool, found bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, found := s.stock[sku]
	if !found {
		return 0, false, false
	}
	if n < qty {
		return n, false, true
	}
	s.stock[sku] = n - qty
	return n - qty, true, true
}

// ReserveRequest is the body of POST /reserve.
type ReserveRequest struct {
	SKU string `json:"sku"`
	Qty int    `json:"qty"`
}

// DefaultSeed is the demo catalogue.
func DefaultSeed() map[string]int {
	return map[string]int{"sku-keyboard": 1_000_000, "sku-mouse": 1_000_000, "sku-monitor": 500_000, "sku-limited": 3}
}

// NewMux builds the inventory routes.
func NewMux(t *telemetry.Telemetry, store *Store, f *faults.Injector) *http.ServeMux {
	mux := http.NewServeMux()

	mux.Handle("GET /stock/{sku}", t.Wrap("/stock/{sku}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.Apply() {
			httpx.Error(w, http.StatusServiceUnavailable, "injected fault")
			return
		}
		sku := r.PathValue("sku")
		n, ok := store.Get(sku)
		if !ok {
			httpx.Error(w, http.StatusNotFound, "unknown sku")
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"sku": sku, "available": n})
	})))

	mux.Handle("POST /reserve", t.Wrap("/reserve", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.Apply() {
			httpx.Error(w, http.StatusServiceUnavailable, "injected fault")
			return
		}
		var req ReserveRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil || req.SKU == "" || req.Qty <= 0 {
			httpx.Error(w, http.StatusBadRequest, "sku and positive qty required")
			return
		}
		span := trace.SpanFromContext(r.Context())
		span.SetAttributes(attribute.String("inventory.sku", req.SKU), attribute.Int("inventory.qty", req.Qty))
		remaining, ok, found := store.Reserve(req.SKU, req.Qty)
		switch {
		case !found:
			httpx.Error(w, http.StatusNotFound, "unknown sku")
		case !ok:
			span.AddEvent("insufficient_stock")
			httpx.JSON(w, http.StatusConflict, map[string]any{"error": "insufficient stock", "available": remaining})
		default:
			httpx.JSON(w, http.StatusOK, map[string]any{"sku": req.SKU, "reserved": req.Qty, "remaining": remaining})
		}
	})))

	mux.Handle("/admin/faults", f.AdminHandler())
	return mux
}
