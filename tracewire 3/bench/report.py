"""Turns bench/results into bench/results/summary.md."""
import json, pathlib, sys
scenario = sys.argv[1] if len(sys.argv) > 1 else "baseline"
r = pathlib.Path(__file__).parent / "results"
rows = []
for mode in ("default", "tuned"):
    lg = json.loads((r / f"loadgen-{mode}.json").read_text())
    for p in ("95", "99"):
        exact = lg[f"p{p}_ms"]
        est = float((r / f"prom-{mode}-p{p}.txt").read_text())
        rows.append((mode, f"p{p}", exact, est, est - exact, 100 * (est - exact) / exact, lg["requests"]))
out = ["| buckets | quantile | exact (client) ms | Prometheus estimate ms | error ms | error % | requests |",
       "|---|---|---|---|---|---|---|"]
out += [f"| {m} | {q} | {e:.1f} | {s:.1f} | {d:+.1f} | {pc:+.1f}% | {n} |" for m, q, e, s, d, pc, n in rows]
(r / f"summary-{scenario}.md").write_text("\n".join(out) + "\n")
print("\n".join(out))
