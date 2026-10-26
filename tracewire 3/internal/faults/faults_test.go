package faults

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestApplyErrorRateAndSlowPath(t *testing.T) {
	var slept time.Duration
	seq := []float64{0.05, 0.5} // slow check hits (<0.1), error check misses (>=0.2)
	i := 0
	inj := &Injector{
		cfg:   Config{SlowRate: 0.1, SlowLatencyMS: 300, ErrorRate: 0.2},
		sleep: func(d time.Duration) { slept += d },
		rand:  func() float64 { v := seq[i%len(seq)]; i++; return v },
	}
	if inj.Apply() {
		t.Fatal("should not fail")
	}
	if slept != 300*time.Millisecond {
		t.Fatalf("slept %v", slept)
	}
}

func TestSetValidates(t *testing.T) {
	inj := New()
	for _, c := range []Config{{ErrorRate: 1.5}, {SlowRate: -1}, {BaseLatencyMS: -2}} {
		if inj.Set(c) == nil {
			t.Errorf("accepted invalid %+v", c)
		}
	}
}

func TestAdminHandler(t *testing.T) {
	inj := New()
	h := inj.AdminHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/admin/faults", strings.NewReader(`{"error_rate":0.25}`)))
	if rec.Code != 200 || inj.Get().ErrorRate != 0.25 {
		t.Fatalf("put failed: %d %+v", rec.Code, inj.Get())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/admin/faults", strings.NewReader(`{"error_rate":2}`)))
	if rec.Code != 400 {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/faults", nil))
	if rec.Code != 405 {
		t.Fatalf("want 405, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/admin/faults", nil))
	if rec.Code != 200 || inj.Get().ErrorRate != 0 {
		t.Fatalf("delete should restore startup config: %d %+v", rec.Code, inj.Get())
	}
}

func TestLognormalMedianNearOne(t *testing.T) {
	// deterministic LCG
	x := uint64(42)
	u := func() float64 { x = x*6364136223846793005 + 1442695040888963407; return float64(x>>11) / (1 << 53) }
	below := 0
	for range 10000 {
		if lognormal(u) < 1 {
			below++
		}
	}
	if below < 4800 || below > 5200 {
		t.Fatalf("median off: %d/10000 below 1", below)
	}
}
