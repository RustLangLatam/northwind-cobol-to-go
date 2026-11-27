# northwind-ibmi-cobol-legacy-application

Generated microservices in Go over GRPC.

## Services (2)

- **nwc620** — (no owned resources recorded)
- **nwc700** — (no owned resources recorded)

### Not included (11)

The migration plan names these services too; they were not generated and are not in this tree: nwc500, nwc600, nwc520, nwc200, nwc510, nwc300, nwc100, nwc190, nwc400, nwc490, nwc610.

## Architecture

Each service follows the **hexagonal (ports & adapters)** architecture:

- `domain` — entities and business rules (no framework dependencies)
- `application` — use cases orchestrating the domain
- `ports` — interfaces (inbound/outbound boundaries)
- `infrastructure` — adapters: persistence, transport, config

**Layout:** `core/services/<service>/{domain,application,ports,infrastructure}`

**Topology:** microservices — N independent gRPC services (no api-gateway in this deliverable).

## Technologies

- **Language:** Go
- **Protocol:** gRPC
- **Framework / transport:** grpc-go
- **Database:** Db2 for i (existing schema, no database container) (driver: )
  - The customer's Db2 for i schema is used as it is: no DDL, no migrations, no seed data and no database container are shipped.
  - Connection settings are environment variables, listed with placeholders in each service's `.env.example` and in the Configuration table below.
  - `DB2_LIBRARY` is required: it is the default schema, and an unqualified table resolves in it. One library replaces the job's library list (`*LIBL`): a table in another library needs a qualified name.
  - `DB2_COMMIT_MODE` is required (`*NONE`, `*CHG`, `*CS`, `*ALL` or `*RR`). Each `.env.example` gives the mode the source implies, with its evidence, or says how to choose one. A library made with CRTLIB is not journaled: any mode above `*NONE` needs every file the service writes journaled (`CRTJRNRCV`, `CRTJRN`, `STRJRNPF`), otherwise the service refuses to start with SQL7008. `*ALL` and `*RR` on a program that only reads hold read locks until a COMMIT it never issues.
  - `DB2_CCSID` ships as `SET_ME` and the service refuses to start until it is set: the DEFAULT CCSID of your IBM i job (273 on PUB400, 37 for US English and for a job whose CCSID is 65535; ask the operator). A character column without a CCSID clause takes it, and the host stores X'3F' without an error for a character it lacks; the service refuses such text before sending, and refuses to start when the value is not the one the host reports. `0` is the explicit opt-out: only 7-bit ASCII is accepted in those columns. 65535, 1200, 1208 and 13488 are refused.
  - `DB2_CA_FILE` empty verifies the host certificate against the system roots; a private CA is a PEM file (see `deploy/<service>/db2-ca/`). `DB2_MIN_PASSWORD_LEVEL` (default 2, 2 to 4) refuses a host that announces the DES password level.
- **Build/test gate:** `go build ./...` + `go test ./...`

_Go toolchain: the `go` line of `go.mod` is the minimum Go version. The platform's `toolchain go1.27.4` patch pin is dropped from this go.mod, and the image and the gates build with `GOTOOLCHAIN=local`. A local build uses the Go you have installed (1.27 or newer); set `GOTOOLCHAIN=go1.27.4` to build with the exact patch release._

_Exact language/framework versions are pinned in the stack's manifest (go.mod / pyproject.toml / package.json / Cargo.toml / pom.xml / Gemfile / .csproj / CMakeLists.txt)._

## Deploy & Run

Build gate: `go build ./...` + `go test ./...`

```sh
go build ./...
go test ./...
cp core/cmd/nwc620-services/.env.example core/cmd/nwc620-services/.env   # fill in the placeholders
set -a; . core/cmd/nwc620-services/.env; set +a
go run ./core/cmd/nwc620-services
```

### Configuration

| Variable | Description |
|---|---|
| `DB2_HOST` | IBM i host name |
| `DB2_PORT` | database host server port (9471 is TLS, the default) |
| `DB2_LIBRARY` | library (schema) of the tables; replaces the job's `*LIBL` |
| `DB2_USER` | IBM i user profile |
| `DB2_PASSWORD` | its password (a secret: Secret / `secret.env`, never in the ConfigMap) |
| `DB2_COMMIT_MODE` | commitment control, required: `*NONE`, `*CHG`, `*CS`, `*ALL` or `*RR` |
| `DB2_CCSID` | default CCSID of the IBM i job, required (273 on PUB400, 37 for US English); `0` = 7-bit ASCII only |
| `DB2_MIN_PASSWORD_LEVEL` | lowest QPWDLVL accepted: 2, 3 or 4 |
| `DB2_CA_FILE` | PEM of a private CA; empty = the system roots |
| `DB2_SERVER_NAME` | name the certificate carries, when it is not DB2_HOST |
| `DB2_MAX_OPEN_CONNS` | pool bound per instance: IBM i host jobs = replicas x this value (k8s: 2 replicas x 5) |

### Ports

- gRPC services listen on **50051, 50052, …** (`GRPC_PORT`, one per service).

### API description

`docs/openapi.yaml` describes the HTTP mapping of the services' protos.

## Assembly notes

The assembler could not establish these facts; each one is a degraded state, not a default:

- `time-zone: service nwc620: the source system's time zone is not in the analysis facts; SERVICE_TIME_ZONE is empty and the service refuses to start until it is set`
- `time-zone: service nwc700: the source system's time zone is not in the analysis facts; SERVICE_TIME_ZONE is empty and the service refuses to start until it is set`

---

_Generated deterministically at download time._
