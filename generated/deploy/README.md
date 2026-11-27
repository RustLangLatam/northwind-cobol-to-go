# Deployment

Deterministically generated deployment artifacts for this deliverable.

- Transport: **gRPC**
- Database: **Db2 for i (existing schema)** (your own, not part of this deployment)

## Database

This deliverable keeps the customer's EXISTING Db2 for i schema: it creates no database container, no tables, no migrations and no seed data. Set the `DB2_*` values (host, library, user, password, commit mode, CCSID, CA) from your own IBM i; the password is a placeholder and belongs in a secret store. The connection is TLS (port 9471) with a verified certificate; a plaintext connection needs an explicit `DB2_ALLOW_PLAINTEXT=true`.

## docker compose

Fill in the secret values first, then build and run from the archive root (the database is yours; set the connection values in `docker-compose.yaml`):

```sh
cp deploy/nwc620/secret.env.example deploy/nwc620/secret.env   # then edit it
cp deploy/nwc700/secret.env.example deploy/nwc700/secret.env   # then edit it
docker compose up --build
```

### Runtime configuration (gRPC services)

Every gRPC service reads its runtime configuration from the environment (`.env.example` lists each variable with its default; the ConfigMap and compose set them). These have no default that could run, so the service refuses to start until you set them:

- **TLS is required.** Put `tls.crt` and `tls.key` in `deploy/<service>/tls/` (compose) or create the Secret `<service>-tls` from `deploy/<service>/tls.secret.example.yaml` (Kubernetes). Plaintext needs `GRPC_ALLOW_PLAINTEXT=true`, for local development only.
- **Token authentication.** `AUTH_TOKEN_PUBLIC_KEY` is the Ed25519 public key (64 hex characters, not a secret) of your token issuer; `AUTH_TOKEN_AUDIENCE` defaults to the service name and `AUTH_TOKEN_ISSUER` is optional. `GRPC_AUTH_MODE=mtls` replaces tokens with client certificates.
- **Metrics token.** `/metrics` requires the bearer token in `deploy/<service>/metrics/token` (compose) or the Secret `<service>-metrics` (Kubernetes, from `deploy/<service>/metrics.secret.example.yaml`); your scraper sends it as `Authorization: Bearer <token>`. `/health` and `/readyz` stay open, so the probes need no credential.
- **Mutual TLS.** `GRPC_AUTH_MODE=mtls` needs `GRPC_TLS_CLIENT_CA_FILE` and `GRPC_MTLS_ALLOWED_CLIENTS` as `dns:NAME` or `uri:URI` entries (`cn:NAME` is deprecated). Tokens older than `AUTH_TOKEN_MAX_LIFETIME` (24h) are refused; raise it only to your issuer's access token lifetime.
- **Time zone.** `SERVICE_TIME_ZONE` is the IANA zone of the source system (its dates are local dates). It is empty when the analysis did not state it; the README lists it under Assembly notes.

Each service's gRPC port is published on `127.0.0.1` only, and its metrics port is not published. To reach a service from another host, change its `ports:` entry to `"0.0.0.0:<port>:<port>"` deliberately, behind your own TLS and authentication.

## Kubernetes

Build and push each image, create each service's Secret from your secret store (the template `deploy/<service>/secret.example.yaml` is not in `k8s/` and is never applied as it is), then apply the manifests:

```sh
docker build -f deploy/nwc620/Dockerfile -t nwc620:1.0.0-dev .
docker build -f deploy/nwc700/Dockerfile -t nwc700:1.0.0-dev .
kubectl create secret generic nwc620-secret --from-literal=...  # the keys of deploy/nwc620/secret.example.yaml
kubectl create secret generic nwc700-secret --from-literal=...  # the keys of deploy/nwc700/secret.example.yaml
kubectl apply -f deploy/nwc620/k8s/
kubectl apply -f deploy/nwc700/k8s/
```

Each Deployment runs two replicas under a PodDisruptionBudget with requests and limits, its own ServiceAccount without an API token, and a NetworkPolicy that admits the app port from the namespace and the metrics port only from pods labelled `metrics-scraper: "true"` in the `monitoring` namespace, and lets egress go only to DNS, the datastore port and OTLP (every `cidr` marked `NARROW ME` is open to any address on its port: set it to your database and collector addresses, or regenerate with the network-policy options; an ingress controller in another namespace needs the commented rule in `networkpolicy.yaml`, and NodeLocal DNS (169.254.20.10) is already allowed). Ephemeral volumes and ephemeral storage are size-limited. Go containers run as uid 65532 on a read-only root filesystem, with no capability, no privilege escalation and the RuntimeDefault seccomp profile; `/tmp` and every `*_DIR` the service writes are `emptyDir` volumes, so their content does not survive the pod: mount a PersistentVolumeClaim there for output you keep.

## Ports

| Service | gRPC | Metrics | Health | Readiness |
| --- | --- | --- | --- | --- |
| nwc620 | 50051 | 9100 | `/health` | `/readyz` |
| nwc700 | 50052 | 9101 | `/health` | `/readyz` |

Metrics are scraped from `/metrics` on the metrics port, directly on the pod (the Service exposes the app port only).
