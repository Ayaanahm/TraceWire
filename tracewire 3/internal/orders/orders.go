// Package orders creates orders by reserving stock in the inventory service.
package orders

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/Ayaanahm/tracewire/internal/faults"
	"github.com/Ayaanahm/tracewire/internal/httpx"
	"github.com/Ayaanahm/tracewire/internal/telemetry"
)

// Order is a placed order.
type Order struct {
	ID        string    `json:"id"`
	SKU       string    `json:"sku"`
	Qty       int       `json:"qty"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateRequest is the body of POST /orders.
type CreateRequest struct {
	SKU string `json:"sku"`
	Qty int    `json:"qty"`
}

// Service holds orders and talks to inventory.
type Service struct {
	inventoryURL string
	client       *http.Client
	tel          *telemetry.Telemetry

	mu     sync.RWMutex
	orders map[string]Order
	seq    atomic.Int64
}

// New creates the orders service.
func New(t *telemetry.Telemetry, inventoryURL string, client *http.Client) *Service {
	return &Service{inventoryURL: inventoryURL, client: client, tel: t, orders: map[string]Order{}}
}

var (
	errOutOfStock = errors.New("insufficient stock")
	errUnknownSKU = errors.New("unknown sku")
)

// upstreamError means inventory was unreachable or failed.
type upstreamError struct{ err error }

func (e upstreamError) Error() string { return "inventory unavailable: " + e.err.Error() }

func (s *Service) reserve(ctx context.Context, req CreateRequest) error {
	ctx, span := s.tel.Tracer.Start(ctx, "orders.reserve_stock")
	defer span.End()
	span.SetAttributes(attribute.String("order.sku", req.SKU), attribute.Int("order.qty", req.Qty))

	body, _ := json.Marshal(req)
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.inventoryURL+"/reserve", bytes.NewReader(body))
	if err != nil {
		return err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(hreq)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "inventory request failed")
		return upstreamError{err}
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusConflict:
		return errOutOfStock
	case http.StatusNotFound:
		return errUnknownSKU
	default:
		err := fmt.Errorf("status %d", resp.StatusCode)
		span.SetStatus(codes.Error, err.Error())
		return upstreamError{err}
	}
}

// NewMux builds the orders routes.
func NewMux(t *telemetry.Telemetry, s *Service, f *faults.Injector) *http.ServeMux {
	mux := http.NewServeMux()

	mux.Handle("POST /orders", t.Wrap("/orders", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.Apply() {
			httpx.Error(w, http.StatusServiceUnavailable, "injected fault")
			return
		}
		var req CreateRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil || req.SKU == "" || req.Qty <= 0 || req.Qty > 1000 {
			httpx.Error(w, http.StatusBadRequest, "sku and qty in 1..1000 required")
			return
		}
		invStart := time.Now()
		err := s.reserve(r.Context(), req)
		// Server-Timing lets the gateway (and browser devtools) see per-hop time.
		w.Header().Set("Server-Timing", fmt.Sprintf("inventory;dur=%.2f", float64(time.Since(invStart).Microseconds())/1000))
		var ue upstreamError
		switch {
		case errors.Is(err, errOutOfStock):
			httpx.Error(w, http.StatusConflict, err.Error())
			return
		case errors.Is(err, errUnknownSKU):
			httpx.Error(w, http.StatusUnprocessableEntity, err.Error())
			return
		case errors.As(err, &ue):
			httpx.Error(w, http.StatusBadGateway, ue.Error())
			return
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
			return
		}
		o := Order{ID: "ord-" + strconv.FormatInt(s.seq.Add(1), 10), SKU: req.SKU, Qty: req.Qty, CreatedAt: time.Now().UTC()}
		s.mu.Lock()
		s.orders[o.ID] = o
		s.mu.Unlock()
		httpx.JSON(w, http.StatusCreated, o)
	})))

	mux.Handle("GET /orders/{id}", t.Wrap("/orders/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		o, ok := s.orders[r.PathValue("id")]
		s.mu.RUnlock()
		if !ok {
			httpx.Error(w, http.StatusNotFound, "order not found")
			return
		}
		httpx.JSON(w, http.StatusOK, o)
	})))

	mux.Handle("/admin/faults", f.AdminHandler())
	return mux
}
