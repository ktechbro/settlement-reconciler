# Architecture

## Modules

- `internal/reconcile/model.go`: normalized records, exact-quantity and canonical-identifier validation.
- `verify.go`: custom Ed25519 envelope acceptance, configured key/issuer/tenant checks, captured verification key.
- `engine.go`: pure deterministic reconciliation with an explicit evaluation time.
- `store.go` and `schema.sql`: PostgreSQL evidence records, immutable revisions, idempotency, versioned cases, and per-tenant serialization.
- `service.go`: transactional commands, resolution auditing, scheduler ticks, and consistent evidence traces.
- `api.go`: bearer authentication, body/time limits, typed error responses, tenant-scoped routes.
- `importer.go`: Project 1 REST adapter with atomically committed cursor.
- `internal/demodata`: signed synthetic scenarios and an independent expected-status manifest.

Executables: `cmd/reconciler` (API and deadline scheduler), `cmd/demo` (signed scenarios), `cmd/import-indexer` (operator-run Project 1 import).

## Tables

| Table | Responsibility |
| --- | --- |
| `records` | Current immutable evidence projection; composite tenant/kind/ID key |
| `record_versions` | Every accepted evidence revision |
| `requests` | Tenant/route/key idempotency fingerprint and original response |
| `cases` | Current finding per decision, movement, and attempt |
| `case_versions` | Prior findings, rule version, evidence IDs, evaluation time |
| `resolutions` | Actor notes bound to a precise case version |
| `audit_events` | Committed mutation actor/action/fingerprint |
| `import_cursors` | Tenant/source scope and last imported Project 1 cursor |

JSONB retains normalized evidence and exact quantities as validated strings. The reconciler compares quantities rather than posting ledger sums. No floating-point parsing is used. Raw source records and accepted signed-envelope inputs remain in the evidence history.

The initial schema is embedded and created transactionally under a global migration lock. It is the first schema version for this new repository; future deployed changes should use explicit numbered migrations.

## Write boundary

Authentication supplies tenant and actor. After parsing and idempotency-key validation, a transaction locks that tenant, checks cached responses, validates new records, persists evidence, recomputes cases, saves the response/audit row, and commits. On any error, none of these effects commit.

The lock permits multiple service instances without a separate distributed lease. It intentionally serializes all writes within a tenant. Different tenants can proceed independently. A 64-bit database hash selects the advisory key; a hash collision would add serialization, not cross-tenant access.

## Import boundary

Project 1 exposes scope-filtered finalized settlements. The importer pins that scope so configuration changes cannot silently inherit an old cursor. It validates ascending IDs and basic finality/shape fields, maps the transaction/log locator to a canonical movement ID, and saves the page plus cursor in one tenant transaction.

Overlapping sources reuse unchanged finalized economic evidence. A conflicting movement aborts the page, including its cursor update. The first imported evidence record remains authoritative for provenance; request/audit fingerprints record subsequent imports. A full multi-provider provenance ledger would be a later extension.

The API data source, network names, credential-to-tenant mapping, and coverage source are operator trust boundaries. Using different labels for the same physical chain is not deduplicated automatically.

## Evidence reads

The trace endpoint uses a repeatable-read transaction. It links the accepted decision, pinned recipient mapping, attempts, current movement records, immutable record revisions, case history, coverage, and resolution notes from a consistent snapshot. All queries remain tenant scoped.

The current implementation scans a tenant's evidence when building traces and matching cases. It is intentionally small enough to inspect; indexed affected-case processing is the next performance step, not a claimed property of this build.
