package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type Service struct {
	Store    *Store
	Verifier Verifier
	Now      func() time.Time
}

func (s *Service) Clock() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) Write(ctx context.Context, tenant, actor, kind, key string, raw []byte) ([]byte, bool, error) {
	return s.Store.Mutate(ctx, tenant, actor, kind, key, raw, func(tx pgx.Tx) (any, error) {
		var id string
		var value any
		switch kind {
		case "decisions":
			v, err := s.Verifier.Accept(tenant, raw, s.Clock())
			if err != nil {
				return nil, invalid(err)
			}
			id = v.ID
			value = v
		case "mappings":
			var v Mapping
			if err := strictDecode(raw, &v); err != nil {
				return nil, invalid(err)
			}
			id = v.ID
			value = v
		case "payment-attempts":
			var v Attempt
			if err := strictDecode(raw, &v); err != nil {
				return nil, invalid(err)
			}
			id = v.ID
			value = v
		case "settlements":
			var v Settlement
			if err := strictDecode(raw, &v); err != nil {
				return nil, invalid(err)
			}
			id = v.ID
			value = v
		case "coverage":
			var v Coverage
			if err := strictDecode(raw, &v); err != nil {
				return nil, invalid(err)
			}
			id = v.ID
			value = v
		case "reconcile":
			var input struct{}
			if err := strictDecode(raw, &input); err != nil {
				return nil, invalid(err)
			}
			return Reconcile(ctx, tx, tenant, s.Clock())
		case "resolutions":
			return s.resolve(ctx, tx, tenant, actor, raw)
		default:
			return nil, fmt.Errorf("unknown record kind")
		}
		if kind != "decisions" {
			if err := Validate(kind, value); err != nil {
				return nil, invalid(err)
			}
		}
		if err := Put(ctx, tx, tenant, kind, id, value); err != nil {
			return nil, err
		}
		if _, err := Reconcile(ctx, tx, tenant, s.Clock()); err != nil {
			return nil, err
		}
		return map[string]string{"id": id, "kind": kind}, nil
	})
}

func (s *Service) resolve(ctx context.Context, tx pgx.Tx, tenant, actor string, raw []byte) (any, error) {
	var in ResolutionInput
	if err := strictDecode(raw, &in); err != nil {
		return nil, invalid(err)
	}
	if !identifier.MatchString(in.ID) || in.Note == "" || len(in.Note) > 4000 || (in.Action != "acknowledge" && in.Action != "investigate") {
		return nil, invalid(fmt.Errorf("invalid resolution; acknowledgement cannot override financial evidence"))
	}
	var version int
	err := tx.QueryRow(ctx, "SELECT version FROM cases WHERE tenant=$1 AND key=$2", tenant, in.CaseKey).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if version != in.Version {
		return nil, ErrConflict
	}
	v := Resolution{ID: in.ID, CaseKey: in.CaseKey, Version: version, Action: in.Action, Note: in.Note, Actor: actor, At: s.Clock()}
	body, _ := json.Marshal(v)
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM resolutions WHERE tenant=$1 AND id=$2)", tenant, in.ID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrConflict
	}
	_, err = tx.Exec(ctx, "INSERT INTO resolutions(tenant,id,case_key,case_version,body) VALUES($1,$2,$3,$4,$5)", tenant, in.ID, in.CaseKey, version, body)
	return v, err
}

// Tick reevaluates deadline-dependent cases. Evidence ingestion evaluates immediately.
func (s *Service) Tick(ctx context.Context, tenants []string) error {
	for _, tenant := range tenants {
		key := "tick-" + s.Clock().Truncate(10*time.Second).Format(time.RFC3339)
		if _, _, err := s.Write(ctx, tenant, "scheduler", "reconcile", key, []byte(`{}`)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Trace(ctx context.Context, tenant, id string) (map[string]any, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	snapshot, err := load(ctx, tx, tenant)
	if err != nil {
		return nil, err
	}
	var decision *Decision
	for i := range snapshot.Decisions {
		if snapshot.Decisions[i].ID == id {
			decision = &snapshot.Decisions[i]
			break
		}
	}
	if decision == nil {
		return nil, ErrNotFound
	}
	versions, err := history(ctx, tx, tenant, "decision:"+id)
	if err != nil {
		return nil, err
	}
	evidenceIDs := map[string]bool{}
	coverageIDs := map[string]bool{}
	for _, v := range versions {
		for _, sid := range v.Case.Evidence.Settlements {
			evidenceIDs[sid] = true
		}
		for _, sid := range v.Case.Candidates {
			evidenceIDs[sid] = true
		}
		for _, cid := range v.Case.Evidence.Coverage {
			coverageIDs[cid] = true
		}
	}
	attempts := []Attempt{}
	for _, a := range snapshot.Attempts {
		if a.DecisionID == id {
			attempts = append(attempts, a)
		}
	}
	settlements := []Settlement{}
	for _, v := range snapshot.Settlements {
		if evidenceIDs[v.ID] {
			settlements = append(settlements, v)
		}
	}
	mappings := []Mapping{}
	for _, m := range snapshot.Mappings {
		if m.ID == decision.Claims.MappingID {
			mappings = append(mappings, m)
		}
	}
	coverage := []Coverage{}
	for _, c := range snapshot.Coverage {
		if coverageIDs[c.ID] {
			coverage = append(coverage, c)
		}
	}
	resolutions := []json.RawMessage{}
	rows, err := tx.Query(ctx, "SELECT body FROM resolutions WHERE tenant=$1 AND case_key=$2 ORDER BY id", tenant, "decision:"+id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v json.RawMessage
		if err = rows.Scan(&v); err != nil {
			rows.Close()
			return nil, err
		}
		resolutions = append(resolutions, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rawVersions := []map[string]any{}
	rows, err = tx.Query(ctx, "SELECT kind,id,revision,body,fingerprint FROM record_versions WHERE tenant=$1 ORDER BY kind,id,revision", tenant)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var kind, rid, hash string
		var revision int
		var body json.RawMessage
		if err = rows.Scan(&kind, &rid, &revision, &body, &hash); err != nil {
			rows.Close()
			return nil, err
		}
		if (kind == "decisions" && rid == id) || (kind == "settlements" && evidenceIDs[rid]) {
			rawVersions = append(rawVersions, map[string]any{"kind": kind, "id": rid, "revision": revision, "body": body, "fingerprint": hash})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return map[string]any{"decision": decision, "mappings": mappings, "payment_attempts": attempts, "movements": settlements, "coverage": coverage, "case_history": versions, "record_versions": rawVersions, "resolutions": resolutions}, tx.Commit(ctx)
}

func history(ctx context.Context, tx pgx.Tx, tenant, key string) ([]Version, error) {
	rows, err := tx.Query(ctx, "SELECT body,version,previous_version,fingerprint,as_of FROM case_versions WHERE tenant=$1 AND key=$2 ORDER BY version", tenant, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Version{}
	for rows.Next() {
		var v Version
		var body []byte
		if err = rows.Scan(&body, &v.Version, &v.PreviousVersion, &v.Fingerprint, &v.AsOf); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(body, &v.Case); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}
