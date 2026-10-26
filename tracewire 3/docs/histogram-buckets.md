# Do custom histogram buckets make p99 more accurate?

**Short answer: not here.** I expected them to, measured it, and was wrong.

## Hypothesis

`histogram_quantile()` linearly interpolates inside a bucket. Prometheus' default
buckets jump `0.1 → 0.25 → 0.5` s, so a p99 anywhere between 100 and 250 ms is
estimated from a single 150 ms-wide bucket. I expected that to put the estimate
tens of milliseconds away from the true value, and added 20 buckets dense
between 50 and 500 ms to fix it.

## Method (`bench/run.sh`)

* The compose stack runs with `LATENCY_BUCKETS=default` and then with the tuned
  buckets. Services are recreated between runs so counters start at zero.
* `cmd/loadgen` sends 150 req/s of `POST /api/orders` for 120 s (about 18,000
  requests, open-loop) and records **every** latency, giving exact percentiles.
* Prometheus' estimate: `histogram_quantile(q, sum by (le) (increase(...bucket{service="gateway"}[window])))`
  over exactly the load window.
* Two latency shapes, set with the fault injector:
  * **baseline**: inventory 20 ms lognormal median, 2% of requests +150 ms (p99 ≈ 183 ms)
  * **tail-near-100ms**: 4% of requests +85 ms, which puts p99 around 129 ms, just
    inside the wide default 100–250 ms bucket. This was meant to be the worst case.

Exact numbers are measured at the client, so they include about 1 ms of
loopback/HTTP overhead the server-side histogram doesn't see. That explains
the small, consistent positive error in every row.

## Results

baseline

| buckets | quantile | exact ms | Prometheus ms | error |
|---|---|---|---|---|
| default | p95 | 66.8 | 70.7 | +5.8% |
| default | p99 | 182.9 | 184.3 | +0.8% |
| tuned | p95 | 68.8 | 71.7 | +4.2% |
| tuned | p99 | 184.2 | 186.6 | +1.3% |

tail-near-100ms

| buckets | quantile | exact ms | Prometheus ms | error |
|---|---|---|---|---|
| default | p95 | 79.3 | 81.2 | +2.4% |
| default | p99 | 128.8 | 130.0 | +0.9% |
| tuned | p95 | 82.0 | 83.3 | +1.6% |
| tuned | p99 | 129.5 | 130.7 | +0.9% |

Raw data: `bench/results/`.

## Why the hypothesis failed

Interpolation error depends on how unevenly samples are spread *inside* the
bucket that holds the quantile, not on how wide the bucket is. With
thousands of samples and a smooth lognormal-plus-spike distribution, the
samples near the 99th percentile are spread evenly enough that linear
interpolation lands within about 1%. A wide bucket would mislead with a
sharp, narrow spike inside it, or with very few samples.

## What I kept and why

The tuned buckets stay, for a different reason: the SLO threshold (250 ms)
is an exact bucket edge, so `GatewayLatencyP99High` compares against a counted
boundary instead of an interpolated one. Cost: 21 series per
(service, route, method, code) instead of 12, about 1.75× the histogram
cardinality. On a larger system with many routes I would revert to defaults
and measure the SLO from `le="0.25"` bucket ratios.
