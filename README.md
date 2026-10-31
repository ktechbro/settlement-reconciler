# Settlement Reconciler

**Which approved agent payments actually settled—and which need investigation?**

A Go/PostgreSQL authorization-to-settlement reconciler by [ktechbro](https://github.com/ktechbro). It connects immutable authorization decisions to normalized chain movements, compares exact payment details, and preserves an auditable history of every finding.

Independent portfolio work inspired by the distinction between authorization and settlement in [Saifuro's documentation](https://docs.saifuro.com/reference/verdict-limits). Its signed demo envelope is a custom schema, **not a Saifuro production token implementation**.

Native escrow funding, seller release, and buyer refund reconciliation is implemented. See [bindings, import, evidence API, and the producer-to-consumer demo](docs/escrow.md).

## Features

- Explicit decision → payment attempt → transaction/event evidence links.
- Exact uint256 atomic quantities; no floating-point money or implicit USD/USDC conversion.
- Pinned, immutable versions of recipient mappings.
- Ed25519 signature verification with issuer/key/tenant binding and verification evidence captured at acceptance.
- Historical reconciliation that does not confuse current token expiry with validity when a payment was authorized.
- Mismatch, duplicate, denied-but-paid, ambiguity, finality, and missing-evidence cases.
- Complete declared coverage required before concluding an approval was unpaid.
- Tenant-scoped bearer authentication, idempotency keys, immutable record revisions, versioned cases, and actor-attributed resolutions.
- Serialized per-tenant reconciliation: concurrent workers cannot leave one payment matched to multiple approvals.
- A durable importer for [Project 1: Settlement Proof](https://github.com/ktechbro/settlement-proof).
- A signed 12-scenario demo, PostgreSQL integration tests, Docker setup, and CI race checks.

## Run the demo

Prerequisites: Docker and Compose. No wallet, signing account, RPC provider, or real funds required.

```sh
docker compose --profile demo run --build --rm demo
```

The command starts a real PostgreSQL database and runs the real ingestion/matching service against synthetic movements. It generates an ephemeral Ed25519 key, verifies each signed decision, then advances an explicit evaluation clock to test historical matching and unpaid deadlines.

The JSON report must return `"status":"PASS"` with these independently specified decision outcomes:

| Scenario | Expected result |
| --- | --- |
| Correct approved payment | `matched` |
| Deadline passed and complete coverage | `approved_unpaid` |
| Denied decision followed by payment | `unapproved_payment` |
| Wrong amount | `amount_mismatch` |
| Wrong recipient | `recipient_mismatch` |
| Wrong token | `asset_or_chain_mismatch` |
| Wrong payer | `payer_mismatch` |
| Two finalized payments for one approval | `duplicate_payment` |
| Two equally plausible heuristic candidates | `ambiguous` |
| Linked provisional payment | `awaiting_finality` |
| Broadcast after authorization expiry | `authorization_timing_mismatch` |
| No payment and insufficient coverage | `incomplete_evidence` |

Each demo uses a separate tenant namespace. Idempotent retries within a namespace are covered by the integration suite.

## Run the API

```sh
docker compose --profile api up --build -d
python examples/client.py
```

The example uses only Python's standard library. It creates an unsigned local fixture, links a synthetic payment, and asserts that the evidence API reports a match. Compose deliberately enables unsigned fixtures for this local example and binds both exposed ports to localhost.

- API: `http://127.0.0.1:8081`
- PostgreSQL: `127.0.0.1:55438`
- Example bearer token: `local-demo-token-change-me-0001`

**For a real deployment:** supply private bearer credentials, disable `ALLOW_FIXTURES`, configure trusted public keys, and use TLS/authenticated infrastructure. The demo token is public and must not protect remotely exposed data. All bearer principals in this MVP have write access to their own tenant; separate read/write roles are a future extension.

Direct Go execution reads environment variables, not `.env` files. See [.env.example](.env.example). `TRUSTED_KEYS` binds each key ID to one issuer and tenant. The service never fetches keys from token-provided URLs.

## API

Every `/v1` route requires `Authorization: Bearer <token>`. The credential supplies tenant and audit actor. Every POST requires `Idempotency-Key`; reusing a key with a different semantic JSON body returns 409. Same-key retries return the stored response, even after the original authorization expires.

| Route | Purpose |
| --- | --- |
| `POST /v1/mappings` | Register an immutable recipient-reference mapping/version |
| `POST /v1/decisions` | Accept a custom signed token or explicitly enabled fixture |
| `POST /v1/payment-attempts` | Link an approved intent to a chain, transaction, and optional event locator |
| `POST /v1/settlements` | Import normalized observed/finalized chain evidence |
| `POST /v1/coverage` | Register a trusted source's complete/partial scan declaration |
| `POST /v1/reconcile` | Reevaluate cases with the service clock; body `{}` |
| `GET /v1/reconciliation/cases?status=&cursor=&limit=` | List current tenant cases in key order |
| `GET /v1/decisions/{id}/evidence` | Decision, mapping, attempts, movements, raw revisions, findings/history, and resolutions |
| `POST /v1/resolutions` | Acknowledge/investigate one exact case version with an audit note |
| `POST /v1/reconciliation/cases/{key}/resolutions` | Equivalent scoped resolution route; body must name the same case key |
| `GET /health/live`, `GET /health/ready` | Process/database checks |

Records and examples are defined in [model.go](internal/reconcile/model.go) and [examples/client.py](examples/client.py). API errors: 401 unauthenticated, 404 missing tenant-scoped object, 409 immutable/idempotency/version conflict, 422 invalid input, 503 unavailable storage. Limits: 1 MiB POST body, 20-second request context, 500 cases per page.

Cases have separate `decision:`, `payment:`, and `attempt:` subjects so orphan payments and dangling links remain visible. Payment cases mirror the linked decision's current finding; attempt cases become `linked` once their decision exists. `denied` denotes a denial with no linked movement. List pages are a live view, not a frozen report snapshot; restart pagination to obtain a fresh full view after concurrent writes.

The scheduler reevaluates deadline-dependent cases every ten seconds. Every successful evidence write also reconciles its tenant in the same transaction. Repeated identical findings do not add redundant case versions.

## Connect Project 1

Export these variables for the CLI:

```sh
export DATABASE_URL='postgres://reconciler:reconciler-local@127.0.0.1:55438/reconciler?sslmode=disable'
export INDEXER_URL='http://127.0.0.1:8080'
export INDEXER_SCOPE='REPLACE_WITH_SCOPE_FROM_PROJECT1_STATUS'
export IMPORT_SOURCE_ID='base-indexer'
export IMPORT_TENANT='demo'
go run ./cmd/import-indexer
```

PowerShell uses `$env:NAME='value'` assignments. The importer consumes Project 1's scope-filtered settlement REST API. It validates the pinned scope and monotonically advancing IDs, retains source evidence, and commits imported movements plus its cursor atomically. Re-running resumes safely. Overlapping source histories reuse canonical movements. It does not connect the two databases or rely on unsafe global outbox-ID polling.

The importer does **not** synthesize coverage from a list of payments: an empty event list does not establish a complete historical scan. Register coverage separately from trusted scanner records. The CLI has direct database access and is an operator tool, not an HTTP endpoint accepting arbitrary source URLs.

## Test

```sh
docker compose up -d --wait postgres
export TEST_DATABASE_URL='postgres://reconciler:reconciler-local@127.0.0.1:55438/reconciler?sslmode=disable'
go test ./...
go vet ./...
go build ./cmd/...
```

PowerShell: `$env:TEST_DATABASE_URL='postgres://reconciler:reconciler-local@127.0.0.1:55438/reconciler?sslmode=disable'`. Go 1.27.3 is pinned in `go.mod`. Without `TEST_DATABASE_URL`, integration tests explicitly skip. CI supplies PostgreSQL and runs `go test -race -count=1 -coverprofile=coverage.out ./...` on Linux.

Tests verify exact findings/evidence, authorization timing, signature tampering, tenant binding, ambiguity, missing coverage, finality promotion, immutable records, replay after expiry, rollback, concurrent claims, case revisions, authenticated actor attribution, and Project 1 cursor recovery.

## Architecture and boundaries

```mermaid
flowchart LR
    D[Signed decisions] --> V[Acceptance verifier]
    M[Versioned recipient mappings] --> E[Pure reconciliation engine]
    V --> R[Immutable evidence records]
    P[Payment attempts] --> R
    I[Project 1 importer / fixtures] --> R
    C[Trusted coverage declarations] --> R
    R --> E
    E --> H[Versioned cases + audit trail]
    H --> A[Authenticated evidence API]
```

See [correctness and rules](docs/correctness.md), [architecture](docs/architecture.md), [operator runbook](docs/runbook.md), and [recorded validation](docs/validation.md).

This service trusts its configured signing keys and authorized evidence producers. It does not independently verify consensus, receipts, or scan completeness. It does not implement Saifuro's production token schema, make payments, hold funds, decide disputes, or generate accounting postings. Partial/split payments are exceptions under the deliberately explicit one-approval/one-full-transfer policy.

MIT license. Independent portfolio implementation, not an official Saifuro integration.
