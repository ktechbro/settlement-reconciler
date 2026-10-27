# Validation record

Validated on 27 October 2026. This is a record of correctness checks, not a performance benchmark or independent audit.

## Checks exercised

- Go unit tests for exact findings, multiple movements per transaction, candidate ambiguity, payer/asset/recipient/amount checks, deadlines, and finality.
- Ed25519 acceptance, tampered-payload rejection, issuer/key/tenant binding, expiry at intake, and explicitly disabled unsigned fixtures.
- Real PostgreSQL tests for rollback, immutable histories, finality promotion, same-key replay after expiry, and conflicting-body rejection.
- Concurrent claims on one movement: both current decision cases become ambiguous; they cannot remain two matches.
- HTTP tenant isolation, identical external business IDs across tenants, and audit actors derived from credentials.
- Project 1-style HTTP imports, pinned source scope, replay recovery, overlapping sources, and a contradictory page that must roll back evidence and cursor together.
- `go vet ./...` and `go build ./cmd/...`.
- Docker multi-stage build and the complete signed 12-scenario demo.
- The Python standard-library client against the authenticated Docker API: PASS, with a matched synthetic payment and retrievable evidence trace.

The signed demo returned every status in its independent manifest, including approved-unpaid with coverage, denied-but-paid, duplicate payment, ambiguity, pending finality, late broadcast, and missing coverage. Its signing key was generated ephemerally in memory; no real wallet or Saifuro credential was involved.

The [GitHub workflow](https://github.com/ktechbro/settlement-reconciler/actions/workflows/ci.yml) repeats the PostgreSQL tests with Linux race detection, static checks, a build, and the signed demo on each push. Consult the run for the commit you are reviewing; local results do not substitute for a later commit's CI outcome.

No real-chain authorization or live Saifuro API integration is claimed. Project 1 has separate live-chain verification; this repository tests the normalization boundary using synthetic source responses and a runnable import adapter.
