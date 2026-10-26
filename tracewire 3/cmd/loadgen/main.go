// loadgen drives POST /api/orders at a fixed rate and reports exact
// client-side latency percentiles, so they can be compared with what
// Prometheus estimates from histogram buckets.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

func main() {
	target := flag.String("url", "http://localhost:8080/api/orders", "endpoint")
	rps := flag.Int("rps", 100, "requests per second")
	dur := flag.Duration("duration", 60*time.Second, "test duration")
	workers := flag.Int("workers", 64, "concurrent workers")
	out := flag.String("json", "", "write results JSON here")
	flag.Parse()

	skus := []string{"sku-keyboard", "sku-mouse", "sku-monitor"}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: *workers}}

	jobs := make(chan struct{}, *workers)
	var mu sync.Mutex
	var lat []float64
	codes := map[int]int{}
	var wg sync.WaitGroup
	for range *workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				body := fmt.Sprintf(`{"sku":%q,"qty":1}`, skus[rand.IntN(len(skus))])
				start := time.Now()
				resp, err := client.Post(*target, "application/json", strings.NewReader(body))
				ms := float64(time.Since(start).Microseconds()) / 1000
				code := 0
				if err == nil {
					code = resp.StatusCode
					resp.Body.Close()
				}
				mu.Lock()
				lat = append(lat, ms)
				codes[code]++
				mu.Unlock()
			}
		}()
	}

	tick := time.NewTicker(time.Second / time.Duration(*rps))
	deadline := time.After(*dur)
	began := time.Now()
loop:
	for {
		select {
		case <-deadline:
			break loop
		case <-tick.C:
			select {
			case jobs <- struct{}{}:
			default: // workers saturated; drop rather than queue (open-loop)
				mu.Lock()
				codes[-1]++
				mu.Unlock()
			}
		}
	}
	close(jobs)
	wg.Wait()
	elapsed := time.Since(began)

	slices.Sort(lat)
	pct := func(p float64) float64 {
		if len(lat) == 0 {
			return 0
		}
		return lat[int(p*float64(len(lat)-1))]
	}
	res := map[string]any{
		"requests": len(lat), "duration_s": elapsed.Seconds(),
		"achieved_rps": float64(len(lat)) / elapsed.Seconds(),
		"p50_ms":       pct(0.5), "p95_ms": pct(0.95), "p99_ms": pct(0.99), "max_ms": pct(1),
		"status_codes": codes,
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(b))
	if *out != "" {
		_ = os.WriteFile(*out, b, 0o644)
	}
}
