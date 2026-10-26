// Package httpx holds the server bootstrap shared by every service:
// health/readiness probes, /metrics, structured logs and graceful shutdown.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Ayaanahm/tracewire/internal/telemetry"
)

// JSON writes v with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes a JSON error body.
func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"error": msg})
}

// Getenv returns env k or def.
func Getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Run serves mux on addr with probes and metrics mounted, and shuts down
// gracefully on SIGTERM: readiness flips to 503 first so Kubernetes stops
// routing traffic, then in-flight requests drain.
func Run(t *telemetry.Telemetry, addr string, mux *http.ServeMux) error {
	var ready atomic.Bool
	ready.Store(true)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		JSON(w, http.StatusOK, map[string]string{"status": "ok", "version": telemetry.Version()})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			Error(w, http.StatusServiceUnavailable, "shutting down")
			return
		}
		JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.Handle("GET /metrics", t.MetricsHandler())

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "service", t.Service, "addr", addr, "version", telemetry.Version())
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down", "service", t.Service)
	ready.Store(false)
	time.Sleep(drainDelay())
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := srv.Shutdown(sctx)
	_ = t.Shutdown(sctx)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func drainDelay() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("SHUTDOWN_DRAIN_DELAY")); err == nil {
		return d
	}
	return 3 * time.Second
}

// InitLogger configures JSON logging to stdout.
func InitLogger(service string) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", service))
}
