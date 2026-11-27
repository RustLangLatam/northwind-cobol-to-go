# Private CA for the Db2 for i connection

Leave DB2_CA_FILE empty to verify the IBM i certificate against the system roots.

For a private CA, put its PEM here as ca.pem and set DB2_CA_FILE=/etc/nwc620/db2-ca/ca.pem.
docker compose mounts this directory read-only at /etc/nwc620/db2-ca. On Kubernetes, create the
ConfigMap the Deployment mounts at the same path:

    kubectl create configmap nwc620-db2-ca --from-file=ca.pem=<your-ca.pem>
