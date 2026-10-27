CREATE TABLE IF NOT EXISTS records (
 tenant TEXT NOT NULL, kind TEXT NOT NULL, id TEXT NOT NULL, body JSONB NOT NULL,
 fingerprint TEXT NOT NULL, revision INT NOT NULL DEFAULT 1, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant,kind,id)
);
CREATE UNIQUE INDEX IF NOT EXISTS canonical_movement ON records(tenant,(body->'asset'->>'network'),(body->>'transaction_hash'),(body->>'event_index')) WHERE kind='settlements';
CREATE TABLE IF NOT EXISTS record_versions (
 tenant TEXT NOT NULL, kind TEXT NOT NULL, id TEXT NOT NULL, revision INT NOT NULL,
 body JSONB NOT NULL, fingerprint TEXT NOT NULL, recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant,kind,id,revision)
);
CREATE TABLE IF NOT EXISTS requests (
 tenant TEXT NOT NULL, route TEXT NOT NULL, key TEXT NOT NULL, fingerprint TEXT NOT NULL,
 response JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant,route,key)
);
CREATE TABLE IF NOT EXISTS cases (
 tenant TEXT NOT NULL, key TEXT NOT NULL, version INT NOT NULL, fingerprint TEXT NOT NULL,
 body JSONB NOT NULL, as_of TIMESTAMPTZ NOT NULL, PRIMARY KEY(tenant,key)
);
CREATE TABLE IF NOT EXISTS case_versions (
 tenant TEXT NOT NULL, key TEXT NOT NULL, version INT NOT NULL, previous_version INT NOT NULL,
 fingerprint TEXT NOT NULL, body JSONB NOT NULL, as_of TIMESTAMPTZ NOT NULL,
 PRIMARY KEY(tenant,key,version)
);
CREATE TABLE IF NOT EXISTS resolutions (
 tenant TEXT NOT NULL, id TEXT NOT NULL, case_key TEXT NOT NULL, case_version INT NOT NULL,
 body JSONB NOT NULL, PRIMARY KEY(tenant,id),
 FOREIGN KEY(tenant,case_key,case_version) REFERENCES case_versions(tenant,key,version)
);
CREATE TABLE IF NOT EXISTS audit_events (
 seq BIGSERIAL PRIMARY KEY, tenant TEXT NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL,
 subject TEXT NOT NULL, fingerprint TEXT NOT NULL, recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS current_cases_status ON cases(tenant,(body->>'status'),key);
CREATE TABLE IF NOT EXISTS import_cursors (
 tenant TEXT NOT NULL, source TEXT NOT NULL, scope TEXT NOT NULL, cursor TEXT NOT NULL,
 PRIMARY KEY(tenant,source)
);
