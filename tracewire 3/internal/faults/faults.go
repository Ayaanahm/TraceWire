// Package faults injects latency and errors so dashboards and alerts can be
// exercised on demand. Faults can be set from env at startup and changed at
// runtime via PUT /admin/faults.
package faults

import (
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// Config describes injected behaviour.
type Config struct {
	// BaseLatencyMS is the median simulated work time (lognormal).
	BaseLatencyMS float64 `json:"base_latency_ms"`
	// SlowRate is the fraction of requests that take SlowLatencyMS extra.
	SlowRate      float64 `json:"slow_rate"`
	SlowLatencyMS float64 `json:"slow_latency_ms"`
	// ErrorRate is the fraction of requests that fail with 503.
	ErrorRate float64 `json:"error_rate"`
}

// Injector applies a Config. It is safe for concurrent use.
type Injector struct {
	mu    sync.RWMutex
	cfg   Config
	start Config // startup config, restored by DELETE
	sleep func(time.Duration)
	rand  func() float64
}

// New builds an Injector from env vars FAULT_BASE_LATENCY_MS, FAULT_SLOW_RATE,
// FAULT_SLOW_LATENCY_MS and FAULT_ERROR_RATE.
func New() *Injector {
	c := Config{
		BaseLatencyMS: envFloat("FAULT_BASE_LATENCY_MS", 0),
		SlowRate:      envFloat("FAULT_SLOW_RATE", 0),
		SlowLatencyMS: envFloat("FAULT_SLOW_LATENCY_MS", 0),
		ErrorRate:     envFloat("FAULT_ERROR_RATE", 0),
	}
	return &Injector{cfg: c, start: c, sleep: time.Sleep, rand: rand.Float64}
}

// Reset restores the config the service started with.
func (i *Injector) Reset() {
	i.mu.Lock()
	i.cfg = i.start
	i.mu.Unlock()
}

func envFloat(k string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(k), 64); err == nil {
		return v
	}
	return def
}

// Get returns the current config.
func (i *Injector) Get() Config {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.cfg
}

// Set replaces the config after validating it.
func (i *Injector) Set(c Config) error {
	if c.ErrorRate < 0 || c.ErrorRate > 1 || c.SlowRate < 0 || c.SlowRate > 1 ||
		c.BaseLatencyMS < 0 || c.SlowLatencyMS < 0 {
		return errInvalid
	}
	i.mu.Lock()
	i.cfg = c
	i.mu.Unlock()
	return nil
}

type faultErr string

func (e faultErr) Error() string { return string(e) }

const errInvalid = faultErr("rates must be in [0,1] and latencies >= 0")

// Apply sleeps for the simulated work time and reports whether the request
// should fail.
func (i *Injector) Apply() (fail bool) {
	c := i.Get()
	d := 0.0
	if c.BaseLatencyMS > 0 {
		// lognormal around the median: realistic right-skewed latencies
		d = c.BaseLatencyMS * lognormal(i.rand)
	}
	if c.SlowRate > 0 && i.rand() < c.SlowRate {
		d += c.SlowLatencyMS
	}
	if d > 0 {
		i.sleep(time.Duration(d * float64(time.Millisecond)))
	}
	return c.ErrorRate > 0 && i.rand() < c.ErrorRate
}

// AdminHandler serves GET/PUT for the fault config; DELETE restores the
// startup config.
func (i *Injector) AdminHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
		case http.MethodDelete:
			i.Reset()
		case http.MethodPut:
			var c Config
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&c); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			if err := i.Set(c); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		default:
			w.Header().Set("Allow", "GET, PUT, DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(i.Get())
	})
}
