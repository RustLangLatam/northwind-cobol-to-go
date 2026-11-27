# Observability — nwc620

Deterministically generated observability contract for this service. Every service in this deliverable implements the SAME uniform contract (feature #28): structured JSON logging, health + readiness, Prometheus metrics and OpenTelemetry tracing, all driven by environment variables.

- Transport: **gRPC** (port `50051`)
- Metrics / probe port (`METRICS_PORT`): **`9100`** (= 9100 + service index)
- Datastore: **Db2 for i (existing schema)**
- Instrumentation: the platform `pkg/log` logger, `prometheus/client_golang` (the platform metrics server), the OpenTelemetry Go SDK (`go.opentelemetry.io/otel`)

## Endpoints

| Endpoint | Purpose | Served on |
| --- | --- | --- |
| `grpc_health_v1` | Standard gRPC health service (liveness/readiness) | gRPC port `50051` |
| `/health` | Liveness (process up), plain HTTP for the k8s httpGet probe | metrics port `9100` |
| `/readyz` | Readiness (datastore reachable), plain HTTP for the k8s httpGet probe | metrics port `9100` |
| `/metrics` | Prometheus scrape | metrics port `9100` |

`/readyz` returns non-OK until the **Db2 for i (existing schema)** connection is reachable — the readiness check actually pings the datastore, so k8s stops routing traffic to a pod whose datastore is down.

## Environment variables

All observability is configured through the per-service `.env` / ConfigMap (features #29 / #27) — placeholder values only, never a real credential:

| Variable | Purpose | Value |
| --- | --- | --- |
| `LOG_LEVEL` | Log verbosity | `info` |
| `LOG_FORMAT` | Log format (JSON to stdout) | `json` |
| `METRICS_HOST` | Metrics / probe bind host | `0.0.0.0` |
| `METRICS_PORT` | Metrics / probe bind port | `9100` |
| `OTEL_SERVICE_NAME` | Trace service name | `nwc620` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP collector endpoint (empty = tracing disabled, no-op) | _(empty)_ |

## Kubernetes wiring (feature #27)

`deploy/nwc620/k8s/deployment.yaml` maps this contract to Kubernetes:

- `livenessProbe` httpGet `/health` on port `9100`.
- `readinessProbe` httpGet `/readyz` on port `9100`.
- Pod annotations `prometheus.io/scrape: "true"`, `prometheus.io/port: "9100"`, `prometheus.io/path: "/metrics"` point the Prometheus scraper at `/metrics` on the metrics port.

## Tracing

OpenTelemetry is wired at bootstrap: the service name comes from `OTEL_SERVICE_NAME` and spans are exported via OTLP to `OTEL_EXPORTER_OTLP_ENDPOINT`. When `OTEL_EXPORTER_OTLP_ENDPOINT` is empty the tracer is a no-op and the service still starts normally — a missing collector is never fatal. Spans wrap each inbound gRPC handler and each outbound datastore call, propagating context via `traceparent`.
