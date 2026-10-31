# Authorization-to-escrow reconciliation

This extension keeps the original direct-payment engine and adds explicit native escrow lifecycle reconciliation. Funding, seller release and buyer refund have different meanings: funding is locked principal, release is seller settlement, refund returns buyer funds. They are not three vendor payments or duplicate spends against one approval.

## Records and API

Authenticated writes use the existing bearer tenant/actor and `Idempotency-Key` conventions:

- `POST /v1/escrow-deals`: immutable tenant-owned binding of a native deal to accepted approvals.
- `POST /v1/escrow-events`: immutable normalized native evidence, typically supplied by the indexer importer.
- `GET /v1/escrows/{id}/evidence`: deal, native events, financial movements, linked decisions and mappings, case history, and operator resolutions, isolated to the authenticated tenant.

Example deal shape (addresses and approval IDs must match your evidence):

```json
{
  "id": "deal1",
  "asset": {"network": "solana-mainnet", "address": "USDC_MINT"},
  "program": "TRUSTED_PROGRAM", "config": "PINNED_CONFIG",
  "escrow": "ESCROW_ACCOUNT", "native_deal_id": "1", "vault": "VAULT_ACCOUNT",
  "buyer": "BUYER_OWNER", "seller": "SELLER_OWNER", "amount_atomic": "1000000",
  "refund_at": 1900000105,
  "funding_decision_id": "fund1", "funding_broadcast_at": "2030-03-17T17:46:40Z",
  "release_decision_id": "release1", "release_broadcast_at": "2030-03-17T17:46:42Z"
}
```

The funding approval binds buyer, seller beneficiary, exact network/token and principal. Its transfer actually goes to the pinned escrow vault. A release requires a separate linked accepted approval binding escrow owner as payer and seller as beneficiary. A deadline-valid buyer refund requires no seller-payment approval. Broadcast times use RFC3339 timestamps with full acceptance precision; native refund/expiry/block times use Unix seconds. Release fields can be omitted for a refund or a deal that is only funded. Bind the intended release up front; bindings are immutable. Late evidence is supported; rewriting deal terms is not.

Native events retain program/config/escrow/vault identity, native action, exact amount, parties available in the instruction, transaction and outer/inner locator, slot/transaction order, times, paired settlement ID and original evidence. Funding supplies native deal ID/config/deadline. Refund instructions omit config; the deal/funding evidence establishes context.

## Matching and exceptions

The pure engine orders actions by slot, transaction position and outer instruction. It compares native actions with their exact finalized token effects and immutable deal terms, checks accepted authorization-use windows and payment deadlines, enforces one funding and one full release or refund, and compares funding with outgoing principal. Future/unfinalized evidence stays pending. Missing history stays incomplete with an unknown locked balance. Unattributed owner transfers, principal/party/asset/vault differences, early refunds, duplicate terminals, expired releases, missing approvals and reused approvals produce explicit cases.

Normal deal cases are `escrow_funded`, `escrow_released`, or `escrow_refunded`. Exception cases include `ambiguous`, `invalid_escrow_transition`, `escrow_balance_mismatch`, `escrow_mismatch`, `unapproved_payment`, `incomplete_evidence`, and `awaiting_finality`. Case categories distinguish escrow deals, authorizations, movements, and unbound evidence. Once a deal is registered, earlier unbound/orphan cases are superseded by new case versions rather than deleted. Acknowledgements retain findings and never change financial truth.

Unique tenant-scoped indexes prohibit duplicate native deal bindings, source actions and movement attribution. A checksum-tracked additive migration preserves existing records. Evidence, import cursors, case versions and audit entries commit atomically. Native actions do not change the economics of existing direct-payment settlements.

## Import from Settlement Proof

Configure the producer's `ESCROW_PROGRAMS` using its `docs/escrow.md`. Use its returned scope hash:

```powershell
$env:INDEXER_URL = 'http://127.0.0.1:8080'
$env:INDEXER_SCOPE = 'REPLACE_WITH_PRODUCER_SCOPE'
$env:IMPORT_TENANT = 'demo'
$env:IMPORT_SOURCE_ID = 'solana-indexer'
$env:IMPORT_ESCROW_EVENTS = 'true'
go run ./cmd/import-indexer
```

The CLI imports both generic financial movements and native action envelopes. Keep both streams: generic movements include unsolicited vault transfers outside native actions. Native import uses a separate `escrow:` source cursor, pins the scan scope, verifies ascending cursors and paired economic/locator/finality fields, and preserves the first proof on reimport. It never infers payment coverage from a page or transfer. Explicit tenant deal bindings are required; an observed program/config does not authorize a tenant payment. POST evidence endpoints are operator-trusted ingestion boundaries, not independent chain verification.

## Reproduce the producer-to-consumer demo

In a sibling `settlement-proof` checkout, start its PostgreSQL service and export synthetic evidence:

```powershell
New-Item -ItemType Directory -Force artifacts | Out-Null
$env:ESCROW_EXPORT_PATH = 'artifacts/escrow-fixture.json'
go run ./cmd/escrow-demo
```

Then in this checkout:

```powershell
docker compose up -d postgres
$env:ESCROW_FIXTURE_FILE = (Resolve-Path '../settlement-proof/artifacts/escrow-fixture.json').Path
go run ./cmd/escrow-demo
```

The consumer uses fresh Ed25519 demo issuer keys, verifies signed approvals, calls its actual HTTP importer against the producer export, checks cursor replay, and asserts released/refunded/funded outcomes for the three synthetic deals. Both sides use PostgreSQL. No funds move. Run `go test ./...` with `TEST_DATABASE_URL` set for the rollback, mutation, duplicate identity, tenant isolation, native import and regression tests.

## Trust and supported scope

Supported ABI: the concrete `signed-verdict-escrow-v1` single-principal Solana escrow, with top-level native calls and classic SPL Token USDC CPI effects. Partial releases, arbitrary escrow ABIs, EVM escrow contracts and nested escrow calls require new adapters/rules. Start indexing before funding for complete history; an observed transfer delta is not an account-balance attestation.

The native program's binary signed verdict and this service's custom signed decision envelope are distinct protocols. An explicit approved binding connects them for this portfolio workflow; the reconciler does not claim they are the same cryptographic signature or production Saifuro tokens. It trusts the configured indexer/RPC and deployed program to report successful native execution. Review deployment/config/upgrade authority separately. Current demos use synthetic ABI-shaped records rather than a live funded escrow or validator-generated execution proof.
