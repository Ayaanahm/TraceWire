# Technical decisions and trade-offs

| Decision | Why | Trade-off / what I'd change at scale |
|---|---|---|
| **Go, standard library `net/http`** (1.22+ routing patterns) | Small static binaries, fast startup for rolling deploys, no framework to learn for reviewers | Hand-written JSON handling; at more endpoints I'd generate handlers from an OpenAPI spec |
| **One Dockerfile, `SERVICE` build-arg** | One file to harden, cache and scan; services can't drift | Every service rebuilds when shared code changes (that's correct here) |
| **distroless/static, non-root, read-only rootfs** | No shell or package manager to exploit; Trivy reports 0 OS packages with vulnerabilities | Harder to debug in-cluster: use `kubectl debug` ephemeral containers |
| **Trivy as a hard CI gate (fixable HIGH/CRITICAL)** | On its first run it found 39 HIGH/CRITICAL vulnerabilities, all in the Go binary (stdlib and stale deps), none in the OS layer. Upgrading Go 1.24 → 1.27.3 and the deps brought it to 0 | `--ignore-unfixed` means known-but-unfixed CVEs pass; they're still uploaded to GitHub code scanning as SARIF |
| **Trivy from a pinned container image, not a GitHub Action tag** | Mutable action tags are a supply-chain risk for a job that holds a registry push token | Must bump the version by hand |
| **Prometheus pull + OTel traces push** | Metrics are cheap aggregates scraped on a schedule; traces are per-request events that need batching | Two pipelines. OTel metrics could unify them, but the Prometheus client gives native exemplars today |
| **Exemplars (trace_id on histogram samples)** | A p99 spike on the dashboard links straight to a slow trace in Jaeger | Requires `--enable-feature=exemplar-storage` and OpenMetrics scraping |
| **10% trace sampling in k8s, 100% in compose** | Collector and storage cost scale with traffic | Rare errors can be missed: tail-based sampling in the collector would keep all error traces |
| **Route pattern, not raw path, as metric label** | `/orders/{id}` would otherwise create one series per order ID | You can't break latency down by individual ID (use traces for that) |
| **Fault injection behind `/admin/faults`** | Lets me prove the dashboard and alerts fire, on demand and in CI | Must never be exposed publicly: the gateway does not route `/admin` |
| **Readiness flips to 503, then serve for `SHUTDOWN_DRAIN_DELAY`, then `Shutdown()`** | kube-proxy removes a pod from endpoints asynchronously after SIGTERM. Closing the listener immediately can reset in-flight connections | Every pod takes ≥5 s to terminate; set `terminationGracePeriodSeconds` above drain + longest request |
| **`maxUnavailable: 0`, PDB `minAvailable: 1`, 2 replicas** | Rolling deploys and node drains never take a service to zero | Needs spare capacity for the surge pod |
| **Multi-window burn-rate alert (5m and 1h at 14.4×)** | Pages on fast budget burn without paging on a single bad minute (Google SRE workbook) | Slow burns need a second, ticket-level rule (6h/3d) not included yet |
| **Terraform: kind + Helm (kube-prometheus-stack) + kubectl provider** | One `terraform apply` produces cluster, monitoring and app; destroy is clean | `kubectl_manifest` stores YAML in state. For real clusters I'd let Argo CD/Flux own app manifests and keep Terraform for infrastructure |
| **In-memory stores** | The project is about the platform, not persistence | State is lost on restart, and each replica has its own stock. A real version would use Postgres with `SELECT … FOR UPDATE` |
| **Custom histogram buckets** | SLO edge (250 ms) on an exact bucket boundary | Measured: no accuracy gain over defaults for p95/p99, and 1.75× the cardinality. See [histogram-buckets.md](histogram-buckets.md) |
