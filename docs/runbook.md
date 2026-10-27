# Operator runbook

## Inspect and reconcile

Use `/health/ready` for database availability. Authenticated case-list routes show current findings; a decision's evidence route explains its exact mapping, attempted payment, chain movement, and historical results. Ingestion evaluates immediately, and deadline-only changes are reevaluated every ten seconds.

The API token sets tenant and actor. There is no tenant selector in query parameters. All configured tokens currently have full write access to their own tenant. Deploy behind TLS with private credentials, and separate trusted writer/read access before broad organizational use.

## Investigate exceptions

- Amount/payer/recipient/asset mismatch: compare the signed normalized decision, pinned mapping, attempt, and raw source movement. Do not edit old evidence to make it match.
- Duplicate payment: inspect every distinct network/transaction/event locator. Retries linked to one event are not extra payments.
- Ambiguous: provide a correct explicit attempt/event link after investigation. Reconciliation will append a new finding when the evidence supports it.
- Incomplete evidence: check missing decisions, mappings, source history, or coverage. An empty Project 1 payment page does not establish coverage.
- Approved unpaid: verify the supplied complete coverage declaration and deadline before treating it as an operational nonpayment exception.
- Awaiting finality: retain provisional evidence; promote it with unchanged economic fields when the source reports finality.

POST resolution notes with case key, current case version, action `acknowledge` or `investigate`, and a reason. Stale versions conflict. Acknowledgement does not override financial findings.

Payment attempts are immutable. Correcting a mistaken explicit link requires an audited remediation/retraction design or a separately rebuilt tenant dataset; this MVP does not provide an unaudited delete/edit endpoint. Do not add another conflicting link and assume the wrong one disappears.

## Idempotency and retries

Generate one key per intended mutation and retain it across retries. Different semantic JSON under the same tenant/route/key returns 409. If authorization has since expired, retrying the original key still returns its accepted response; attempting a new intake of an expired token fails validation.

Storage outages return generic errors without connection strings. No partial evidence/case/cursor write commits. On recovery, retry using the original key. Deadlines use the server clock; investigate clock reversals rather than silently rewriting older evaluations.

## Importer recovery

Keep `IMPORT_SOURCE_ID` and `INDEXER_SCOPE` stable for one source scan. If Project 1 changes its scope, configure a new source ID deliberately and inspect overlapping history. A changed scope cannot reuse an existing source cursor.

The importer is a bounded operator invocation with a five-minute deadline, not a hosted recurring import service. Re-run it to resume; schedule it separately if needed. It writes normalized finalized evidence only and never issues payments.

## Signing keys and backups

Configure keys with explicit issuer and tenant. Retain old key records in your operational key history; each accepted signed decision also stores the public key actually used. Never commit private signing keys or real bearer credentials. The demo generates ephemeral keys in memory.

Back up all PostgreSQL tables consistently, including idempotency rows, versions, audit events, and import cursors. Restoring only cursors or current cases can lose the evidence that justifies them. The database owner is trusted and can alter data; application append-only behavior is not protection from a malicious administrator.

## Local lifecycle

```sh
docker compose ps
docker compose logs --tail 100 api
docker compose stop api
docker compose down
```

The named database volume survives `down`. Removing it destroys local evidence and should be a deliberate reset of a disposable environment.
