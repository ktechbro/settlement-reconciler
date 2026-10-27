package reconcile_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/ktechbro/settlement-reconciler/internal/demodata"
	"github.com/ktechbro/settlement-reconciler/internal/reconcile"
)

func setup(t *testing.T) (*reconcile.Service, string) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL required for real PostgreSQL tests")
	}
	s, err := reconcile.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Pool.Close)
	return &reconcile.Service{Store: s, Verifier: reconcile.Verifier{AllowFixtures: true}, Now: func() time.Time { return now }}, fmt.Sprintf("tenant-%d", time.Now().UnixNano())
}
func write(t *testing.T, s *reconcile.Service, tenant, kind, key string, v any) {
	t.Helper()
	raw, _ := json.Marshal(v)
	if _, _, err := s.Write(context.Background(), tenant, "test-actor", kind, key, raw); err != nil {
		t.Fatal(kind, key, err)
	}
}
func basic(t *testing.T, s *reconcile.Service, tenant string) {
	t.Helper()
	base := sample()
	write(t, s, tenant, "mappings", "mapping", base.Mappings[0])
	c := base.Decisions[0].Claims
	c.Tenant = tenant
	write(t, s, tenant, "decisions", "decision", reconcile.DecisionInput{Fixture: &c})
}
func status(t *testing.T, s *reconcile.Service, tenant, key string) reconcile.Version {
	t.Helper()
	cases, err := s.Store.Cases(context.Background(), tenant, "", "", 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range cases {
		if v.Case.Key == key {
			return v
		}
	}
	t.Fatal("case missing", key)
	return reconcile.Version{}
}

func TestPostgresScenarioManifestAndSignedEvidence(t *testing.T) {
	s, tenant := setup(t)
	s.Verifier.AllowFixtures = false
	expected, err := demodata.Seed(context.Background(), s, tenant)
	if err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return now.Add(time.Hour) }
	write(t, s, tenant, "reconcile", "final", struct{}{})
	for id, want := range expected {
		if got := status(t, s, tenant, "decision:"+id); got.Case.Status != want {
			t.Fatalf("%s: want %s got %s: %+v", id, want, got.Case.Status, got.Case.Findings)
		}
	}
	trace, err := s.Store.Trace(context.Background(), tenant, "matched")
	if err != nil {
		t.Fatal(err)
	}
	d := trace["decision"].(*reconcile.Decision)
	if !d.Verification.SignatureValid || d.Claims.Agent != "agent-procurement-01" {
		t.Fatal("missing verified evidence")
	}
	if len(trace["case_history"].([]reconcile.Version)) < 2 {
		t.Fatal("history was overwritten")
	}
	prior := status(t, s, tenant, "decision:matched")
	write(t, s, tenant, "reconcile", "repeat", struct{}{})
	if got := status(t, s, tenant, "decision:matched"); got.Version != prior.Version {
		t.Fatal("same result added redundant version")
	}
}

func TestPostgresIdempotencyExpiryAndAtomicRollback(t *testing.T) {
	s, tenant := setup(t)
	base := sample()
	write(t, s, tenant, "mappings", "mapping", base.Mappings[0])
	claims := base.Decisions[0].Claims
	claims.Tenant = tenant
	raw, _ := json.Marshal(reconcile.DecisionInput{Fixture: &claims})
	first, replayed, err := s.Write(context.Background(), tenant, "actor", "decisions", "stable-key", raw)
	if err != nil || replayed {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return now.Add(time.Hour) }
	second, replayed, err := s.Write(context.Background(), tenant, "actor", "decisions", "stable-key", raw)
	if err != nil || !replayed || !bytes.Equal(first, second) {
		t.Fatal("replay revalidated expired authorization", err)
	}
	altered := append([]byte(nil), raw...)
	var in reconcile.DecisionInput
	json.Unmarshal(altered, &in)
	in.Fixture.Amount = "1"
	altered, _ = json.Marshal(in)
	if _, _, err = s.Write(context.Background(), tenant, "actor", "decisions", "stable-key", altered); !errors.Is(err, reconcile.ErrConflict) {
		t.Fatal("idempotency conflict accepted", err)
	}
	if _, _, err = s.Write(context.Background(), tenant, "actor", "decisions", "new-key", raw); !errors.Is(err, reconcile.ErrInvalid) {
		t.Fatal("new intake accepted expired decision", err)
	}
	_, _, err = s.Store.Mutate(context.Background(), tenant, "actor", "test-rollback", "rollback", []byte(`{}`), func(tx pgx.Tx) (any, error) {
		m := base.Mappings[0]
		m.ID = "rollback-map"
		if err := reconcile.Put(context.Background(), tx, tenant, "mappings", m.ID, m); err != nil {
			return nil, err
		}
		return nil, errors.New("injected failure")
	})
	if err == nil {
		t.Fatal("injected transaction committed")
	}
	var count int
	s.Store.Pool.QueryRow(context.Background(), "SELECT count(*) FROM records WHERE tenant=$1 AND id='rollback-map'", tenant).Scan(&count)
	if count != 0 {
		t.Fatal("partial write survived rollback")
	}
}

func TestPostgresPromotionImmutableHistoryAndResolution(t *testing.T) {
	s, tenant := setup(t)
	basic(t, s, tenant)
	base := sample()
	write(t, s, tenant, "payment-attempts", "attempt", base.Attempts[0])
	s.Now = func() time.Time { return now.Add(time.Hour) }
	v := base.Settlements[0]
	v.Status = "observed"
	v.FinalizedAt = nil
	write(t, s, tenant, "settlements", "observed", v)
	if got := status(t, s, tenant, "decision:d1"); got.Case.Status != "awaiting_finality" {
		t.Fatal(got)
	}
	write(t, s, tenant, "settlements", "finalized", base.Settlements[0])
	current := status(t, s, tenant, "decision:d1")
	if current.Case.Status != "matched" {
		t.Fatal(current)
	}
	changed := base.Settlements[0]
	changed.Amount = "1"
	raw, _ := json.Marshal(changed)
	if _, _, err := s.Write(context.Background(), tenant, "actor", "settlements", "rewrite", raw); !errors.Is(err, reconcile.ErrConflict) {
		t.Fatal("final evidence was rewritten", err)
	}
	alias := base.Settlements[0]
	alias.ID = "alias"
	raw, _ = json.Marshal(alias)
	if _, _, err := s.Write(context.Background(), tenant, "actor", "settlements", "alias", raw); !errors.Is(err, reconcile.ErrConflict) {
		t.Fatal("same chain event accepted twice", err)
	}
	resolution := reconcile.ResolutionInput{ID: "r1", CaseKey: current.Case.Key, Version: current.Version, Action: "acknowledge", Note: "Reviewed source evidence"}
	write(t, s, tenant, "resolutions", "resolve", resolution)
	resolution.ID = "r2"
	resolution.Version--
	raw, _ = json.Marshal(resolution)
	if _, _, err := s.Write(context.Background(), tenant, "actor", "resolutions", "stale", raw); !errors.Is(err, reconcile.ErrConflict) {
		t.Fatal("stale resolution accepted", err)
	}
	trace, err := s.Store.Trace(context.Background(), tenant, "d1")
	if err != nil {
		t.Fatal(err)
	}
	if len(trace["record_versions"].([]map[string]any)) != 3 || len(trace["resolutions"].([]json.RawMessage)) != 1 {
		t.Fatal("immutable versions or resolution missing")
	}
	if got := status(t, s, tenant, "decision:d1"); got.Case.Status != "matched" {
		t.Fatal("resolution changed financial finding")
	}
}

func TestPostgresConcurrentApprovalConsumption(t *testing.T) {
	s, tenant := setup(t)
	basic(t, s, tenant)
	base := sample()
	claims := base.Decisions[0].Claims
	claims.ID = "d2"
	claims.Tenant = tenant
	write(t, s, tenant, "decisions", "d2", reconcile.DecisionInput{Fixture: &claims})
	s.Now = func() time.Time { return now.Add(time.Hour) }
	write(t, s, tenant, "settlements", "movement", base.Settlements[0])
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for _, id := range []string{"d1", "d2"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			a := base.Attempts[0]
			a.ID = id
			a.DecisionID = id
			raw, _ := json.Marshal(a)
			_, _, err := s.Write(context.Background(), tenant, "actor", "payment-attempts", id, raw)
			failures <- err
		}(id)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"d1", "d2"} {
		if got := status(t, s, tenant, "decision:"+id); got.Case.Status != "ambiguous" {
			t.Fatal("payment consumed by two approvals", got)
		}
	}
}

func TestPostgresHTTPIsolationAndActorAudit(t *testing.T) {
	s, tenant := setup(t)
	basic(t, s, tenant)
	const tokenA = "test-token-for-tenant-a-000001"
	const tokenB = "test-token-for-tenant-b-000002"
	auth, err := reconcile.NewAuth(map[string]reconcile.Principal{tokenA: {Tenant: tenant, Actor: "alice"}, tokenB: {Tenant: tenant + "-b", Actor: "bob"}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(reconcile.Handler(s, auth))
	defer server.Close()
	get := func(path, token string) int {
		request, _ := http.NewRequest("GET", server.URL+path, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		resp, e := server.Client().Do(request)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if get("/v1/decisions/d1/evidence", tokenA) != 200 || get("/v1/decisions/d1/evidence", tokenB) != 404 || get("/v1/reconciliation/cases", "") != 401 || get("/v1/reconciliation/cases?limit=0", tokenA) != 400 {
		t.Fatal("HTTP isolation or validation failed")
	}
	// Identical business IDs and agent names remain scoped by the credential's tenant.
	basic(t, s, tenant+"-b")
	if get("/v1/decisions/d1/evidence", tokenB) != 200 {
		t.Fatal("second tenant could not use same external ID")
	}
	request, _ := http.NewRequest("POST", server.URL+"/v1/reconcile", bytes.NewBufferString(`{}`))
	request.Header.Set("Authorization", "Bearer "+tokenA)
	request.Header.Set("Idempotency-Key", "http-reconcile")
	resp, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatal(resp.StatusCode)
	}
	var actor string
	if err = s.Store.Pool.QueryRow(context.Background(), "SELECT actor FROM audit_events WHERE tenant=$1 AND subject='http-reconcile'", tenant).Scan(&actor); err != nil || actor != "alice" {
		t.Fatal("actor was not derived from credential", err)
	}
	current := status(t, s, tenant, "decision:d1")
	resolution := reconcile.ResolutionInput{ID: "http-resolution", CaseKey: current.Case.Key, Version: current.Version, Action: "investigate", Note: "Inspect missing evidence"}
	for _, want := range []int{201, 422} {
		raw, _ := json.Marshal(resolution)
		request, _ := http.NewRequest("POST", server.URL+"/v1/reconciliation/cases/decision:d1/resolutions", bytes.NewReader(raw))
		request.Header.Set("Authorization", "Bearer "+tokenA)
		request.Header.Set("Idempotency-Key", fmt.Sprint("resolution-", want))
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("resolution route: want %d got %d", want, response.StatusCode)
		}
		resolution.CaseKey = "decision:d2"
	}
}

func TestPostgresProject1ImportCursorAndSourceScope(t *testing.T) {
	s, tenant := setup(t)
	page := []reconcile.IndexerSettlement{{ID: 1, Chain: "base-mainnet", Asset: demodata.Asset.Address, TransactionHash: demodata.Hash(100), LogIndex: 0, BlockNumber: 100, BlockHash: demodata.Hash(200), BlockTime: now.Add(-time.Hour).Unix(), Sender: demodata.Buyer, Recipient: demodata.Seller, Amount: "50000000", FinalizedNumber: 101, FinalizedHash: demodata.Hash(201), Raw: json.RawMessage(`{"receipt":1}`)}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := page
		next := "1"
		if r.URL.Query().Get("cursor") == "1" {
			data = []reconcile.IndexerSettlement{}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data, "next_cursor": next, "scope": "pinned-scope"})
	}))
	defer server.Close()
	for i, want := range []int{1, 0} {
		n, err := s.ImportPage(context.Background(), tenant, "source", server.URL, "pinned-scope")
		if err != nil || n != want {
			t.Fatalf("page %d: %d %v", i, n, err)
		}
	}
	if _, err := s.ImportPage(context.Background(), tenant, "source", server.URL, "different-scope"); !errors.Is(err, reconcile.ErrConflict) {
		t.Fatal("scope change reused cursor", err)
	}
	// A second importer overlaps existing history without duplicating a canonical event.
	if n, err := s.ImportPage(context.Background(), tenant, "second-source", server.URL, "pinned-scope"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	var count int
	err := s.Store.Pool.QueryRow(context.Background(), "SELECT count(*) FROM records WHERE tenant=$1 AND kind='settlements'", tenant).Scan(&count)
	if err != nil || count != 1 {
		t.Fatal("duplicate imported effect", err)
	}
	s.Store.Pool.QueryRow(context.Background(), "SELECT count(*) FROM records WHERE tenant=$1 AND kind='coverage'", tenant).Scan(&count)
	if count != 0 {
		t.Fatal("import inferred absence coverage")
	}
	// A contradictory second event aborts the first insert and the import cursor.
	first := page[0]
	first.ID = 10
	first.TransactionHash = demodata.Hash(300)
	second := first
	second.ID = 11
	second.Amount = "1"
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": []reconcile.IndexerSettlement{first, second}, "next_cursor": "11", "scope": "pinned-scope"})
	}))
	defer bad.Close()
	if _, err = s.ImportPage(context.Background(), tenant, "bad-source", bad.URL, "pinned-scope"); !errors.Is(err, reconcile.ErrConflict) {
		t.Fatal("contradictory page accepted", err)
	}
	if err = s.Store.Pool.QueryRow(context.Background(), "SELECT count(*) FROM records WHERE tenant=$1 AND kind='settlements'", tenant).Scan(&count); err != nil || count != 1 {
		t.Fatal("partial import survived rollback", err)
	}
	if err = s.Store.Pool.QueryRow(context.Background(), "SELECT count(*) FROM import_cursors WHERE tenant=$1 AND source='bad-source'", tenant).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed page advanced cursor", err)
	}
}
