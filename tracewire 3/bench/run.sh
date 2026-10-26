#!/usr/bin/env bash
# Measures how far Prometheus' histogram_quantile() p95/p99 estimate lands
# from the exact client-side percentile, with default vs tuned buckets.
# Requires the compose stack. Usage: bench/run.sh [duration] [rps]
set -euo pipefail
cd "$(dirname "$0")/.."
DUR=${1:-120s}; RPS=${2:-150}
mkdir -p bench/results
go build -o bin/loadgen ./cmd/loadgen

for mode in default tuned; do
  echo "== buckets=$mode"
  LATENCY_BUCKETS=$mode docker compose up -d --no-build --force-recreate gateway orders inventory >/dev/null
  until curl -sf localhost:8080/readyz >/dev/null; do sleep 1; done
  sleep 6   # let Prometheus scrape the fresh (zeroed) counters
  start=$(date +%s)
  bin/loadgen -rps "$RPS" -duration "$DUR" -json "bench/results/loadgen-$mode.json" >/dev/null
  sleep 8   # final scrape
  win=$(( $(date +%s) - start ))
  for q in 0.95 0.99; do
    expr="histogram_quantile($q, sum by (le) (increase(http_server_request_duration_seconds_bucket{service=\"gateway\",route=\"/api/orders\"}[${win}s])))"
    curl -s localhost:9090/api/v1/query --data-urlencode "query=$expr" \
      | python3 -c "import json,sys; print(round(float(json.load(sys.stdin)['data']['result'][0]['value'][1])*1000,1))" \
      > "bench/results/prom-$mode-p${q#0.}.txt"
  done
done
python3 bench/report.py "${SCENARIO:-baseline}"
mkdir -p "bench/results/${SCENARIO:-baseline}"
mv bench/results/loadgen-*.json bench/results/prom-*.txt "bench/results/${SCENARIO:-baseline}/"
