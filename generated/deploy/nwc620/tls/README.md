# TLS for the gRPC server

The service refuses to start without a certificate: TLS is required.

Put the server certificate and key here as tls.crt and tls.key (and, for mutual TLS
with GRPC_AUTH_MODE=mtls, the client CA as ca.crt, then set GRPC_TLS_CLIENT_CA_FILE=/etc/nwc620/tls/ca.crt).
docker compose mounts this directory read-only at /etc/nwc620/tls. On Kubernetes, create the
Secret nwc620-tls (see deploy/nwc620/tls.secret.example.yaml). Key files here are git- and docker-ignored.
