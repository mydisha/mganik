# mGanik Omnichannel & Traceability

A prototype of the system foundation from the production line to the consumer: hologram
serialization and anti-counterfeit, omnichannel stock and orders with concurrency protection, an
offline-first POS, and a Supply Chain Command Center.

## Deliverables

| Deliverable | Where | Evidence |
|---|---|---|
| 1a. Hologram serial verification + anti-fraud logic | `POST /api/v1/public/verify`, `POST /api/v1/public/claims`; `service/verify_service.go`, `radar_service.go`, `loyalty_service.go` | `internal/serialtest/radar_test.go`, `loyalty_test.go`, `loyalty_review_test.go` |
| 1b. Order stock allocation with concurrency protection | `POST /api/v1/orders`; `service/inventory_service.go`, `order_service.go`, `repository/inventory_repository.go` (`ApplyBalanceEffect`) | `internal/inventorytest/` |
| 1c. Offline POS transaction reconciliation endpoint | `POST /api/v1/pos/sync`; `service/pos_sync_service.go` | `internal/postest/` (including `kasus4_test.go`) |
| 2. Architecture: components, data flows, DB schema, technology choices, concurrency strategy | [`ARCHITECTURE.md`](ARCHITECTURE.md) | Schema in `apps/api/migrations/` |
| 3. Solutions for the 4 cases, trade-offs, business metrics | [`PRODUCT_DEFENSE.md`](PRODUCT_DEFENSE.md) | — |
| 4a. Stock race condition test | `internal/inventorytest/engine_test.go` | `TestReserve_ConcurrentLastUnits`: stock 10, 100 goroutines → exactly 10 reservations |
| 4b. Hologram status lifecycle test | `internal/serialtest/lifecycle_test.go` | `TestSerialTransitions_Table` (every status pair), `TestSerial_ConcurrentSell` |
| 4c. Offline sync idempotency test | `internal/postest/sync_idempotency_test.go` | `TestPosSync_DuplicateBatch`, `TestPosSync_ConcurrentDuplicate`, `TestPosSync_SameIdDifferentPayload` |

Code paths are relative to `apps/api/internal/`. The frontend (`/pos`, `/console`, `/v/<code>`)
is still a skeleton; all three flows are proven through the API and the tests. The full list of
what is not built yet is in [ARCHITECTURE.md §9](ARCHITECTURE.md#9-prototype-limitations).

### Running the three required tests

Requirements: Go 1.27 and Docker (the tests use PostgreSQL 17 through testcontainers), or your own
Postgres 17 server through `TEST_DATABASE_URL`. Run from `apps/api`.

Stock race condition:

```bash
go test -race -count=1 -run 'TestReserve_ConcurrentLastUnits' -v ./internal/inventorytest/
```

Hologram status lifecycle:

```bash
go test -race -count=1 -run 'TestSerialTransitions_Table|TestSerial_ConcurrentSell' -v ./internal/serialtest/
```

Offline POS sync idempotency:

```bash
go test -race -count=1 -run 'TestPosSync_DuplicateBatch|TestPosSync_ConcurrentDuplicate|TestPosSync_SameIdDifferentPayload' -v ./internal/postest/
```

Every test (Go and frontend): `make test`.

## Running

Requirements: Docker with Compose v2.

```bash
docker compose up --build
```

That one command starts the whole stack in order:

| Service | What it does | Address |
|---|---|---|
| `db` | PostgreSQL 17 | `localhost:55432` (user/db `mganik`) |
| `migrate` | Applies migrations, loads demo data, then exits | — |
| `api` | Go backend (Gin) | http://localhost:8089 |
| `web` | React build served by nginx, proxying `/api` to `api` | http://localhost:8088 |

Pages: `/console` (Command Center), `/pos` (cashier), `/v/<code>` (consumer verification); they are
still a skeleton for now.
Ports can be changed in `.env` (copy it from `.env.example`). `make up` does the same in the
background; `make down` stops it, and `make clean` also deletes the database data.

## Demo data

The `migrate` service runs `api migrate --seed`: migrations, then demo data. The seed is idempotent
(safe to rerun) and only runs when `SEED_DEMO=true` (the compose default; set `SEED_DEMO=false` in
`.env` to migrate only). It contains: 2 SKUs, the Bekasi central warehouse (`GDP-BKS`), the
Surabaya hub (`HUB-SBY`), the Jakarta Selatan (`TKO-JKS`) and Bandung (`TKO-BDG`) stores, the
Distributor Jatim and Distributor Sulsel warehouses with their official territories, 1 batch with
1 pallet → 4 cartons → 48 serialized units, stock balances + ledger, POS and D2C prices, one account
per role, and 1 POS device at the Jakarta Selatan store.

| Account | Role | Location scope |
|---|---|---|
| `admin@mganik.test` | ADMIN | all |
| `ops@mganik.test` | OPS_MANAGER | all |
| `produksi@mganik.test` | PRODUCTION_OPERATOR | Gudang Pusat Bekasi (`GDP-BKS`) |
| `gudang@mganik.test` | WAREHOUSE_STAFF | Gudang Pusat Bekasi (`GDP-BKS`), Hub Surabaya (`HUB-SBY`) |
| `kepala.jaksel@mganik.test` | STORE_MANAGER | Toko Jakarta Selatan (`TKO-JKS`) |
| `kasir.jaksel@mganik.test` | CASHIER | Toko Jakarta Selatan (`TKO-JKS`) |
| `marketing@mganik.test` | MARKETING_ANALYST | all |

Password for every account: the value of `SEED_DEMO_PASSWORD`, default **`mganik-demo-2026`**.
Change it in `.env` before the first seed if the stack is reachable from other networks.

Staff login: `POST /api/v1/auth/login` sets the `__Host-session` cookie (8 hours idle, 12 hours
absolute); 5 failures per email or 20 per IP within 15 minutes trigger a temporary block. Consumer
login (for point claims) uses a mock OTP: request a code through `POST /api/v1/public/otp/request`
with any Indonesian mobile number, then enter the fixed code `OTP_MOCK_CODE`, default
**`246810`**. There is no SMS sender yet; the mock OTP is refused in production unless
`SEED_DEMO_ALLOW_PRODUCTION=true`. Phone numbers are stored encrypted (`PII_ENC_KEY`) and are never
written to logs.

The session cookie uses the `__Host-` prefix and the `Secure` attribute. On `http://localhost`
(no TLS), Chrome and Firefox accept it because localhost counts as a secure context, but **Safari
does not**: use Chrome/Firefox for the local demo, or set up TLS.

The client IP used for rate limiting and auditing is taken from `X-Forwarded-For` only when the
request comes from nginx (the `web` container, static IP `COMPOSE_WEB_IP`, default `172.31.250.10`
in the `COMPOSE_SUBNET` subnet); nginx overwrites that header with the peer IP. Requests sent
directly to the API port use their own peer IP. If the subnet collides with something on the host,
change `COMPOSE_SUBNET`, `COMPOSE_WEB_IP`, and `COMPOSE_IP_RANGE` together in `.env` (see
`.env.example`).

Compose is a local demo stack: even with `APP_ENV=production`, it uses a pepper and demo accounts
whose values are public in this repository, through the `SEED_DEMO_ALLOW_PRODUCTION=true`
permission (api and migrate log a WARN at startup). For a real deployment set that permission to
`false`; the config then refuses `SEED_DEMO=true`, the demo `CLAIM_PEPPER`/`PII_ENC_KEY`, the mock
OTP, and the default demo password. `make migrate/seed/api` read the same `.env` and use the same
default `CLAIM_PEPPER` as compose, so the demo PINs verify either way. If the pepper changes, the
next `api seed` updates the PIN hashes of the demo fixtures.

Resetting demo data: `make clean`, then `make up` (deletes this project's database volume). Do not
use goose down: the lookup data migration deliberately refuses to roll back while other data still
references it.

Serial codes, claim PINs, cashier POS PINs, and POS device tokens are deterministic fixtures (not
secrets) and are never written to logs. To see them:

```bash
docker compose run --rm --no-deps migrate seed --print-demo   # through the image
make demo-data                                                # from source (needs Go)
```

## Development

Additional requirements: Go 1.27, Node 22.12+, pnpm 12, and optionally [air](https://github.com/air-verse/air) for hot reload.

```bash
make hooks     # once per clone
make db        # Postgres only
make migrate
make seed      # demo data (idempotent)
make api       # backend on :8089 (stop the compose stack first)
make web       # Vite on :5173, proxying to :8089
```

| Command | What it does |
|---|---|
| `make test` | Go tests (`-race`) and frontend tests. DB tests use Postgres 17 through testcontainers (needs Docker), or your own server through `TEST_DATABASE_URL` |
| `make lint` | gofmt, go vet, typecheck |
| `make gen` | TypeScript types from `packages/contracts/openapi.yaml` |

## Structure

```
apps/api        Go: controller → service → repository (Gin, GORM, goose)
apps/web        React + Vite + Tailwind + shadcn/ui
packages/contracts/openapi.yaml   API contract, source of the frontend types
compose.yaml    the whole stack
DESIGN.md       UI tokens and rules
```

The backend uses a layered structure in `apps/api/internal`:

| Package | Responsibility |
|---|---|
| `controller` | Gin handlers: bind and validate DTOs, call the service, write the response or problem+json |
| `service` | Use cases, transaction boundaries, business rules |
| `repository` | Database access |
| `dto` | Request/response shapes (GORM models are never bound from a request) |
| `apperror` | Domain errors → `application/problem+json` (RFC 9457) |
| `middleware`, `router`, `config`, `database` | Infrastructure |
