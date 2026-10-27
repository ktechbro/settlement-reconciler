package reconcile

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string
var ErrConflict = errors.New("immutable evidence or idempotency conflict")
var ErrNotFound = errors.New("not found")
var ErrInvalid = errors.New("invalid request")

func invalid(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrInvalid, err.Error())
}

type Store struct{ Pool *pgxpool.Pool }

func Open(ctx context.Context, url string) (*Store, error) {
	p, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("invalid database configuration")
	}
	s := &Store{Pool: p}
	tx, err := p.Begin(ctx)
	if err != nil {
		p.Close()
		return nil, fmt.Errorf("database unavailable")
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(82630411)"); err == nil {
		_, err = tx.Exec(ctx, schema)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		p.Close()
		return nil, fmt.Errorf("schema initialization failed")
	}
	return s, nil
}

func lock(ctx context.Context, tx pgx.Tx, tenant string) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", tenant)
	return err
}
func (s *Store) Mutate(ctx context.Context, tenant, actor, route, key string, raw []byte, fn func(pgx.Tx) (any, error)) ([]byte, bool, error) {
	if key == "" || len(key) > 200 {
		return nil, false, invalid(fmt.Errorf("Idempotency-Key is required and must be at most 200 characters"))
	}
	var canonical any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&canonical); err != nil {
		return nil, false, fmt.Errorf("invalid request JSON")
	}
	fingerprint := Digest(canonical)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	if err = lock(ctx, tx, tenant); err != nil {
		return nil, false, err
	}
	var existing string
	var response []byte
	err = tx.QueryRow(ctx, "SELECT fingerprint,response FROM requests WHERE tenant=$1 AND route=$2 AND key=$3", tenant, route, key).Scan(&existing, &response)
	if err == nil {
		if existing != fingerprint {
			return nil, false, ErrConflict
		}
		return response, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	value, err := fn(tx)
	if err != nil {
		return nil, false, err
	}
	response, err = json.Marshal(value)
	if err != nil {
		return nil, false, err
	}
	if err = tx.QueryRow(ctx, "INSERT INTO requests(tenant,route,key,fingerprint,response) VALUES($1,$2,$3,$4,$5) RETURNING response", tenant, route, key, fingerprint, response).Scan(&response); err != nil {
		return nil, false, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(tenant,actor,action,subject,fingerprint) VALUES($1,$2,$3,$4,$5)", tenant, actor, route, key, fingerprint); err != nil {
		return nil, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return response, false, nil
}

func Put(ctx context.Context, tx pgx.Tx, tenant, kind, id string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	fingerprint := Digest(value)
	var old []byte
	var oldHash string
	var revision int
	err = tx.QueryRow(ctx, "SELECT body,fingerprint,revision FROM records WHERE tenant=$1 AND kind=$2 AND id=$3", tenant, kind, id).Scan(&old, &oldHash, &revision)
	if err == nil {
		if oldHash == fingerprint {
			return nil
		}
		if kind != "settlements" {
			return ErrConflict
		}
		var before, after Settlement
		if json.Unmarshal(old, &before) != nil || json.Unmarshal(body, &after) != nil {
			return ErrConflict
		}
		if before.Status != "observed" || after.Status != "finalized" || before.ImmutableDigest() != after.ImmutableDigest() {
			return ErrConflict
		}
		revision++
	} else if errors.Is(err, pgx.ErrNoRows) {
		revision = 1
	} else {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO records(tenant,kind,id,body,fingerprint,revision) VALUES($1,$2,$3,$4,$5,$6)
  ON CONFLICT(tenant,kind,id) DO UPDATE SET body=EXCLUDED.body,fingerprint=EXCLUDED.fingerprint,revision=EXCLUDED.revision`, tenant, kind, id, body, fingerprint, revision)
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			return ErrConflict
		}
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO record_versions(tenant,kind,id,revision,body,fingerprint) VALUES($1,$2,$3,$4,$5,$6)", tenant, kind, id, revision, body, fingerprint)
	return err
}

func load(ctx context.Context, tx pgx.Tx, tenant string) (Snapshot, error) {
	var s Snapshot
	rows, err := tx.Query(ctx, "SELECT kind,body FROM records WHERE tenant=$1 ORDER BY kind,id", tenant)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var body []byte
		if err = rows.Scan(&kind, &body); err != nil {
			return s, err
		}
		switch kind {
		case "decisions":
			var v Decision
			err = json.Unmarshal(body, &v)
			s.Decisions = append(s.Decisions, v)
		case "mappings":
			var v Mapping
			err = json.Unmarshal(body, &v)
			s.Mappings = append(s.Mappings, v)
		case "payment-attempts":
			var v Attempt
			err = json.Unmarshal(body, &v)
			s.Attempts = append(s.Attempts, v)
		case "settlements":
			var v Settlement
			err = json.Unmarshal(body, &v)
			s.Settlements = append(s.Settlements, v)
		case "coverage":
			var v Coverage
			err = json.Unmarshal(body, &v)
			s.Coverage = append(s.Coverage, v)
		}
		if err != nil {
			return s, err
		}
	}
	return s, rows.Err()
}

func Reconcile(ctx context.Context, tx pgx.Tx, tenant string, asOf time.Time) ([]Version, error) {
	snapshot, err := load(ctx, tx, tenant)
	if err != nil {
		return nil, err
	}
	results := []Version{}
	for _, c := range Evaluate(snapshot, asOf) {
		hash := Digest(c)
		var priorHash string
		var priorVersion int
		var priorAt time.Time
		err = tx.QueryRow(ctx, "SELECT version,fingerprint,as_of FROM cases WHERE tenant=$1 AND key=$2", tenant, c.Key).Scan(&priorVersion, &priorHash, &priorAt)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if priorAt.After(asOf) {
			return nil, fmt.Errorf("reconciliation clock cannot move backward")
		}
		if priorHash == hash {
			results = append(results, Version{Case: c, Version: priorVersion, PreviousVersion: priorVersion - 1, AsOf: priorAt, Fingerprint: hash})
			continue
		}
		version := priorVersion + 1
		body, _ := json.Marshal(c)
		if _, err = tx.Exec(ctx, `INSERT INTO cases(tenant,key,version,fingerprint,body,as_of) VALUES($1,$2,$3,$4,$5,$6)
   ON CONFLICT(tenant,key) DO UPDATE SET version=EXCLUDED.version,fingerprint=EXCLUDED.fingerprint,body=EXCLUDED.body,as_of=EXCLUDED.as_of`, tenant, c.Key, version, hash, body, asOf); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO case_versions(tenant,key,version,previous_version,fingerprint,body,as_of) VALUES($1,$2,$3,$4,$5,$6,$7)", tenant, c.Key, version, priorVersion, hash, body, asOf); err != nil {
			return nil, err
		}
		results = append(results, Version{Case: c, Version: version, PreviousVersion: priorVersion, AsOf: asOf, Fingerprint: hash})
	}
	return results, nil
}

func (s *Store) Cases(ctx context.Context, tenant, status, after string, limit int) ([]Version, error) {
	rows, err := s.Pool.Query(ctx, "SELECT body,version,fingerprint,as_of FROM cases WHERE tenant=$1 AND ($2='' OR body->>'status'=$2) AND key>$3 ORDER BY key LIMIT $4", tenant, status, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Version{}
	for rows.Next() {
		var v Version
		var raw []byte
		if err = rows.Scan(&raw, &v.Version, &v.Fingerprint, &v.AsOf); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &v.Case); err != nil {
			return nil, err
		}
		v.PreviousVersion = v.Version - 1
		result = append(result, v)
	}
	return result, rows.Err()
}
