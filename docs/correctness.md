# Reconciliation rules and trust boundaries

## What a match establishes

Under the configured evidence-source assumptions, `matched` means:

1. The decision was accepted while its claims were valid. In signed mode, a configured issuer/tenant-bound Ed25519 key verified it. In fixture mode, signature validity is explicitly false.
2. The decision allows payment and pins an asset, atomic quantity, payer, recipient reference, and mapping ID.
3. An explicit payment attempt identifies the network, transaction, and, where needed, an event locator.
4. The attempt's intended asset, amount, sender, and recipient agree with the decision and pinned mapping.
5. Exactly one distinct finalized movement satisfies the link, and its actual fields agree too.
6. Reported broadcast occurred at/after acceptance and before expiry; chain inclusion did not precede broadcast.
7. No other decision claims that movement, and no second provisional movement leaves consumption unresolved.

The service preserves all findings even when one primary status is selected. Current precedence is ambiguity, duplicate payment, asset/network, payer, recipient, amount, timing, incomplete evidence, awaiting finality, then approved-unpaid. A denied decision with a linked finalized movement is unapproved-payment.

## Time semantics

Acceptance verifies `issued_at <= accepted_at < expires_at`. Broadcast must satisfy `accepted_at <= broadcast_at < expires_at`. Finalization may occur after expiry: reconciliation uses the stored acceptance evidence rather than rejecting historical approvals because time has passed.

`payment_deadline` is an operational waiting deadline, at least as late as token expiry. It is not a rule requiring finalization before expiry. A late finalized movement can supersede an earlier unpaid finding if the linked broadcast was authorized. Sources must agree on clock precision; this MVP does not add implicit skew allowances.

An event whose finalization timestamp is later than the explicit evaluation clock remains awaiting finality. The demo uses a simulated clock; the API/scheduler use server UTC time.

## Unpaid and ambiguity

To conclude `approved_unpaid`, the payment deadline must have passed and an explicit complete coverage declaration must cover the approved asset, payer, and entire interval from issuance through deadline. Missing history produces `incomplete_evidence`. The service does not infer absence from silence, an empty API page, or elapsed time alone.

Heuristic matching compares asset, quantity, payer, pinned recipient, and a bounded inclusion-time interval. One candidate still needs an explicit link. Several candidates produce ambiguity. No heuristic automatically consumes an approval. A transfer with no explicit known decision remains visible as its own payment case.

Multiple attempts pointing to the same canonical event do not count as multiple payments. Multiple distinct finalized events linked to one approval are duplicates. Without an event locator, several movements inside a transaction are ambiguous. Partial and split payments remain amount/duplicate exceptions; the service never silently adds them into one full payment.

## Immutable evidence and case history

Mappings, decisions, attempts, and coverage records cannot be edited through the API. Make a new mapping ID/version for a change. Decisions retain their pinned old mapping, original input, acceptance time, public verification key, and key ID.

Settlement evidence allows exactly one transition: observed → finalized, with unchanged event identity and economic fields. Both revisions remain stored. A unique database index prevents an alias ID from creating another copy of the same network/transaction/event identity.

Case changes append versions with a previous-version number, rule version, fingerprint, evaluation time, and evidence IDs. Unchanged results reuse the current version. Resolution notes reference a specific version and derive actor identity from authentication. They do not turn a financial exception into a match; new evidence is required for that.

Append-only behavior is enforced by service operations and constraints. A database administrator remains trusted; this is not a cryptographically signed audit-log implementation.

## Tenant and transaction boundaries

All data lookup keys include tenant. Bearer tokens resolve to configured tenant/actor principals, and signed keys also bind to a tenant. External agent IDs do not identify organizations. The same business IDs and agent names may safely exist in different tenants.

Each tenant mutation takes a PostgreSQL transaction advisory lock. Evidence writes, reconciliation, idempotency response, and actor audit event commit together. Concurrent approval consumption is reevaluated against the serialized complete snapshot, preventing two current matched decisions from claiming the same movement.

An identical idempotent retry returns its stored response before signature/time validation is rerun. A changed body under the same tenant/route/key conflicts. PostgreSQL's stored JSON representation is returned on both first success and replay.

## Evidence-source limitations

Authorized writers and the Project 1 indexer are trusted to report chain state and scan completeness. This reconciler does not independently verify consensus, token receipts, wallet ownership, or provider honesty. Public keys identify accepted issuers, not the truth of every assertion they sign.

The custom signed envelope has its own normalized atomic-unit schema. It cannot be presented as accepting Saifuro's real tokens, whose claim schema and units differ. A production integration requires an explicitly reviewed adapter and historical verification contract.

The current engine loads a tenant snapshot and recomputes it. It favors inspectability and deterministic testing over high-volume throughput. Before production-scale backfills, add indexed/incremental affected-case processing, bounded ingestion batches, distinct read/write roles, retention/archival, and load measurements. No benchmark claim is made here.
