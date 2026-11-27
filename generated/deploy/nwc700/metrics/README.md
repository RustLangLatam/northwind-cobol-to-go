# Bearer token of /metrics

The service refuses to start while METRICS_BEARER_TOKEN_FILE names a file it cannot read
or that holds fewer than 16 bytes.

Put a generated token in a file named token here (openssl rand -hex 32 > token). docker
compose mounts this directory read-only at /etc/nwc700/metrics. On Kubernetes, create the Secret
nwc700-metrics (see deploy/nwc700/metrics.secret.example.yaml). Your scraper sends the same
value as "Authorization: Bearer <token>". /health and /readyz stay open for the probes.
Token files here are git- and docker-ignored.
