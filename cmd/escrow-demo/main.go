// escrow-demo consumes a synthetic export from settlement-proof/cmd/escrow-demo.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/ktechbro/settlement-reconciler/internal/reconcile"
	"net/http"
	"net/http/httptest"
	"os"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	raw, err := os.ReadFile(os.Getenv("ESCROW_FIXTURE_FILE"))
	if err != nil {
		return fmt.Errorf("set ESCROW_FIXTURE_FILE to the synthetic settlement-proof escrow export: %w", err)
	}
	var page struct {
		Data  []reconcile.IndexerEscrowEnvelope `json:"data"`
		Next  string                            `json:"next_cursor"`
		Scope string                            `json:"scope"`
	}
	if err = json.Unmarshal(raw, &page); err != nil {
		return err
	}
	if len(page.Data) != 5 || page.Scope == "" {
		return fmt.Errorf("expected five native synthetic events")
	}
	db := os.Getenv("DATABASE_URL")
	if db == "" {
		db = "postgres://reconciler:reconciler-local@127.0.0.1:55438/reconciler?sslmode=disable"
	}
	store, err := reconcile.Open(ctx, db)
	if err != nil {
		return err
	}
	defer store.Pool.Close()
	tenant := fmt.Sprint("escrow-demo-", time.Now().UnixNano())
	clock := time.Unix(1700000099, 0).UTC()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	service := &reconcile.Service{Store: store, Now: func() time.Time { return clock }, Verifier: reconcile.Verifier{Keys: map[string]reconcile.TrustedKey{"escrow-demo": {Issuer: "urn:escrow-demo", Tenant: tenant, PublicKey: base64.RawURLEncoding.EncodeToString(public)}}}}
	write := func(kind, key string, value any) error {
		body, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, _, err = service.Write(ctx, tenant, "escrow-demo", kind, key, body)
		return err
	}
	authorize := func(id, payer string, asset reconcile.Asset, amount string, mapping reconcile.Mapping) error {
		claims := reconcile.Claims{ID: id, Issuer: "urn:escrow-demo", Tenant: tenant, Agent: "demo-agent", Mandate: "demo-mandate", Verdict: "allow", Asset: asset, Amount: amount, Payer: payer, RecipientRef: mapping.Reference, MappingID: mapping.ID, IssuedAt: clock, ExpiresAt: time.Unix(1700000200, 0).UTC(), PaymentDeadline: time.Unix(1700000300, 0).UTC()}
		token, err := reconcile.Sign(claims, "escrow-demo", private)
		if err != nil {
			return err
		}
		return write("decisions", id, reconcile.DecisionInput{Token: token})
	}
	for _, envelope := range page.Data {
		e := envelope.Event
		if e.Kind != "funding" {
			continue
		}
		asset := reconcile.Asset{Network: e.Chain, Address: e.Mint}
		id := "deal" + e.DealID
		mapping := reconcile.Mapping{ID: "seller" + e.DealID, Reference: "vendor:" + e.DealID, Network: e.Chain, Address: e.Seller, Version: 1}
		if err = write("mappings", mapping.ID, mapping); err != nil {
			return err
		}
		if err = authorize("fund"+e.DealID, e.Buyer, asset, e.Amount, mapping); err != nil {
			return err
		}
		deal := reconcile.EscrowDeal{ID: id, Asset: asset, Program: e.Program, Config: e.Config, Escrow: e.Escrow, NativeDealID: e.DealID, Vault: e.Vault, Buyer: e.Buyer, Seller: e.Seller, Amount: e.Amount, RefundAt: e.RefundAt, FundingDecisionID: "fund" + e.DealID, FundingBroadcastAt: time.Unix(e.BlockTime-1, 0).UTC()}
		if e.DealID == "1" {
			if err = authorize("release1", e.Escrow, asset, e.Amount, mapping); err != nil {
				return err
			}
			deal.ReleaseDecisionID = "release1"
			deal.ReleaseBroadcastAt = time.Unix(1700000102, 0).UTC()
		}
		if err = write("escrow-deals", id, deal); err != nil {
			return err
		}
	}
	// The local HTTP server serves the actual producer export; the consumer uses its normal importer.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("cursor") == "0" {
			_, _ = w.Write(raw)
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}, "scope": page.Scope, "next_cursor": page.Next})
		}
	}))
	defer server.Close()
	clock = time.Unix(1700000400, 0).UTC()
	if n, err := service.ImportEscrowPage(ctx, tenant, "escrow-demo", server.URL, page.Scope); err != nil || n != 5 {
		return fmt.Errorf("native import: %d %v", n, err)
	}
	if n, err := service.ImportEscrowPage(ctx, tenant, "escrow-demo", server.URL, page.Scope); err != nil || n != 0 {
		return fmt.Errorf("cursor replay: %d %v", n, err)
	}
	cases, err := store.Cases(ctx, tenant, "", "", 500)
	if err != nil {
		return err
	}
	expected := map[string]string{"escrow:deal1": "escrow_released", "escrow:deal2": "escrow_refunded", "escrow:deal3": "escrow_funded"}
	actual := map[string]string{}
	for _, v := range cases {
		if want, ok := expected[v.Case.Key]; ok {
			if v.Case.Status != want {
				return fmt.Errorf("oracle mismatch: %+v", v.Case)
			}
			actual[v.Case.Key] = v.Case.Status
		}
	}
	if len(actual) != 3 {
		return fmt.Errorf("incomplete deal cases")
	}
	for _, id := range []string{"deal1", "deal2", "deal3"} {
		if _, err = store.EscrowTrace(ctx, tenant, id); err != nil {
			return err
		}
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "PASS", "tenant": tenant, "states": actual, "native_events": 5, "cursor_replay": true, "signature_mode": "ephemeral Ed25519 custom demo issuer, not native verdict signatures", "source": "synthetic indexer export; no real funds"})
}
