# Contributing

Use `gofmt`, `go vet ./...`, and tests with real PostgreSQL. CI runs the race detector on Linux. Keep matching rules in the pure engine, with explicit time and evidence inputs.

For correctness changes, assert exact case keys, findings, and evidence identities. Test one-to-many payment cases, multi-tenant duplicate identifiers, and transaction rollback where relevant. Never weaken unknown/ambiguous states into matches merely to make a demo green.

Any new rule must preserve historic evidence and have a deliberate rule-version policy. Changes to deployed database schemas require numbered migrations. Do not put private keys, production tokens, or sensitive transaction payloads in committed fixtures.
