package reconcile_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ktechbro/settlement-reconciler/internal/reconcile"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func escrowSample(kind string) reconcile.Snapshot {
	s := sample()
	s.Attempts = nil
	s.Settlements = nil
	d := reconcile.EscrowDeal{ID: "deal1", Asset: s.Decisions[0].Claims.Asset, Program: "program1", Config: "config1", Escrow: "escrow1", NativeDealID: "1", Vault: "vault1", Buyer: s.Decisions[0].Claims.Payer, Seller: s.Mappings[0].Address, Amount: "50000000", RefundAt: now.Add(10 * time.Minute).Unix(), FundingDecisionID: "d1", FundingBroadcastAt: now.Add(time.Second)}
	release := s.Decisions[0]
	release.ID = "d2"
	release.Claims.ID = "d2"
	release.Claims.Payer = d.Escrow
	s.Decisions = append(s.Decisions, release)
	d.ReleaseDecisionID = "d2"
	d.ReleaseBroadcastAt = now.Add(3 * time.Second)
	s.Escrows = []reconcile.EscrowDeal{d}
	final := now.Add(time.Hour)
	fund := reconcile.EscrowEvent{ID: "e1", Asset: d.Asset, Program: d.Program, Config: d.Config, Escrow: d.Escrow, NativeDealID: "1", Kind: "funding", Vault: d.Vault, Buyer: d.Buyer, Seller: d.Seller, Amount: d.Amount, RefundAt: d.RefundAt, SettlementID: "s1", TransactionHash: "tx1", Outer: 0, Inner: 0, Slot: 1, BlockTime: now.Add(2 * time.Second).Unix(), SenderAccount: "source1", RecipientAccount: d.Vault, Evidence: map[string]any{"proof": true}}
	fund.SourceEventKey = reconcile.Digest([]any{fund.Asset.Network, fund.Program, fund.TransactionHash, fund.Outer})
	money := reconcile.Settlement{ID: "s1", Asset: d.Asset, TransactionHash: fund.TransactionHash, EventIndex: "1", Sender: d.Buyer, Recipient: d.Escrow, Amount: d.Amount, BlockTime: time.Unix(fund.BlockTime, 0), FinalizedAt: &final, Status: "finalized", Evidence: json.RawMessage(`{"proof":true}`)}
	s.EscrowEvents = []reconcile.EscrowEvent{fund}
	s.Settlements = []reconcile.Settlement{money}
	if kind != "funding" {
		e := fund
		e.ID = "e2"
		e.Kind = kind
		e.SettlementID = "s2"
		e.TransactionHash = "tx2"
		e.Slot = 2
		e.SenderAccount = d.Vault
		e.RecipientAccount = "destination2"
		e.ExpiresAt = now.Add(5 * time.Minute).Unix()
		e.BlockTime = now.Add(4 * time.Second).Unix()
		m := money
		m.ID = e.SettlementID
		m.TransactionHash = e.TransactionHash
		m.Sender = d.Escrow
		m.Recipient = d.Seller
		if kind == "refund" {
			e.BlockTime = d.RefundAt + 1
			m.Recipient = d.Buyer
		}
		m.BlockTime = time.Unix(e.BlockTime, 0)
		e.SourceEventKey = reconcile.Digest([]any{e.Asset.Network, e.Program, e.TransactionHash, e.Outer})
		s.EscrowEvents = append(s.EscrowEvents, e)
		s.Settlements = append(s.Settlements, m)
	}
	return s
}
func escrowCase(t *testing.T, s reconcile.Snapshot) reconcile.Case {
	t.Helper()
	for _, c := range reconcile.Evaluate(s, now.Add(2*time.Hour)) {
		if c.Key == "escrow:deal1" {
			return c
		}
	}
	t.Fatal("missing escrow case")
	return reconcile.Case{}
}
func TestEscrowRules(t *testing.T) {
	for _, kind := range []string{"funding", "release", "refund"} {
		t.Run(kind, func(t *testing.T) {
			s := escrowSample(kind)
			c := escrowCase(t, s)
			want := map[string]string{"funding": "escrow_funded", "release": "escrow_released", "refund": "escrow_refunded"}[kind]
			if c.Status != want {
				t.Fatal(c)
			}
			if kind != "funding" && c.Escrow.Locked != "0" {
				t.Fatal(c)
			}
			for _, other := range reconcile.Evaluate(s, now.Add(2*time.Hour)) {
				if other.Status == "orphan_settlement" || other.Status == "duplicate_payment" {
					t.Fatal("misclassified escrow", other)
				}
			}
		})
	}
	tests := []struct {
		name, status string
		mutate       func(*reconcile.Snapshot)
	}{
		{"wrong seller", "escrow_mismatch", func(s *reconcile.Snapshot) { s.Settlements[1].Recipient = "attacker" }},
		{"denied release", "unapproved_payment", func(s *reconcile.Snapshot) { s.Decisions[1].Claims.Verdict = "deny" }},
		{"late release", "escrow_mismatch", func(s *reconcile.Snapshot) { s.EscrowEvents[1].ExpiresAt = s.EscrowEvents[1].BlockTime }},
		{"missing funding", "incomplete_evidence", func(s *reconcile.Snapshot) { s.EscrowEvents = s.EscrowEvents[1:]; s.Settlements = s.Settlements[1:] }},
		{"double terminal", "invalid_escrow_transition", func(s *reconcile.Snapshot) {
			e := s.EscrowEvents[1]
			e.ID = "e3"
			e.SettlementID = "s3"
			e.TransactionHash = "tx3"
			e.Slot = 3
			m := s.Settlements[1]
			m.ID = "s3"
			m.TransactionHash = "tx3"
			s.Settlements = append(s.Settlements, m)
			s.EscrowEvents = append(s.EscrowEvents, e)
		}},
		{"missing release link", "incomplete_evidence", func(s *reconcile.Snapshot) { s.Escrows[0].ReleaseDecisionID = "" }},
		{"extra transfer", "escrow_balance_mismatch", func(s *reconcile.Snapshot) {
			m := s.Settlements[0]
			m.ID = "extra"
			m.TransactionHash = "txextra"
			s.Settlements = append(s.Settlements, m)
		}},
		{"shared authorization", "ambiguous", func(s *reconcile.Snapshot) {
			d := s.Escrows[0]
			d.ID = "deal2"
			d.Escrow = "escrow2"
			s.Escrows = append(s.Escrows, d)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := escrowSample("release")
			tt.mutate(&s)
			c := escrowCase(t, s)
			if c.Status != tt.status {
				t.Fatal(c)
			}
			if tt.name == "missing funding" && c.Escrow.Locked != "unknown" {
				t.Fatal(c)
			}
		})
	}
	s := escrowSample("refund")
	s.EscrowEvents[1].BlockTime = s.Escrows[0].RefundAt - 1
	s.Settlements[1].BlockTime = time.Unix(s.EscrowEvents[1].BlockTime, 0)
	if escrowCase(t, s).Status != "escrow_mismatch" {
		t.Fatal("early refund accepted")
	}
	// Accepted-at nanoseconds must not reject a later same-second broadcast.
	s = escrowSample("release")
	s.Decisions[0].Verification.AcceptedAt = now.Add(100 * time.Millisecond)
	s.Escrows[0].FundingBroadcastAt = now.Add(200 * time.Millisecond)
	if escrowCase(t, s).Status != "escrow_released" {
		t.Fatal("fractional acceptance lost")
	}
}
func TestPostgresEscrowBindingHistoryIsolation(t *testing.T) {
	service, tenant := setup(t)
	s := escrowSample("refund")
	service.Now = func() time.Time { return now }
	write(t, service, tenant, "mappings", "m", s.Mappings[0])
	for _, d := range s.Decisions {
		c := d.Claims
		c.Tenant = tenant
		write(t, service, tenant, "decisions", d.ID, reconcile.DecisionInput{Fixture: &c})
	}
	service.Now = func() time.Time { return now.Add(2 * time.Hour) }
	for _, m := range s.Settlements {
		write(t, service, tenant, "settlements", m.ID, m)
	}
	for _, e := range s.EscrowEvents {
		write(t, service, tenant, "escrow-events", e.ID, e)
	}
	if got := status(t, service, tenant, "escrow-event:e1"); got.Case.Status != "incomplete_evidence" {
		t.Fatal(got)
	}
	write(t, service, tenant, "escrow-deals", "deal", s.Escrows[0])
	if got := status(t, service, tenant, "escrow:deal1"); got.Case.Status != "escrow_refunded" {
		t.Fatal(got)
	}
	if got := status(t, service, tenant, "escrow-event:e1"); got.Case.Status != "escrow_refunded" || got.Version < 2 {
		t.Fatal("stale unbound case", got)
	}
	if _, err := service.Store.EscrowTrace(context.Background(), tenant, "deal1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Store.EscrowTrace(context.Background(), tenant+"-other", "deal1"); err != reconcile.ErrNotFound {
		t.Fatal(err)
	}
	changed := s.Escrows[0]
	changed.Amount = "1"
	raw, _ := json.Marshal(changed)
	if _, _, err := service.Write(context.Background(), tenant, "test", "escrow-deals", "mutate", raw); err == nil {
		t.Fatal("immutable terms changed")
	}
	duplicate := s.EscrowEvents[0]
	duplicate.ID = "duplicate"
	raw, _ = json.Marshal(duplicate)
	if _, _, err := service.Write(context.Background(), tenant, "test", "escrow-events", "duplicate", raw); err == nil {
		t.Fatal("physical event duplicated")
	}
}
func TestPostgresEscrowImporterAtomicity(t *testing.T) {
	service, tenant := setup(t)
	service.Now = func() time.Time { return now.Add(2 * time.Hour) }
	sample := escrowSample("funding")
	e := sample.EscrowEvents[0]
	m := sample.Settlements[0]
	native := reconcile.IndexerEscrowEnvelope{Event: reconcile.IndexerEscrowEvent{ID: 1, EventKey: e.SourceEventKey, Chain: e.Asset.Network, Program: e.Program, Config: e.Config, Escrow: e.Escrow, DealID: e.NativeDealID, Kind: e.Kind, Mint: e.Asset.Address, Vault: e.Vault, Buyer: e.Buyer, Seller: e.Seller, Amount: e.Amount, RefundAt: e.RefundAt, Signature: e.TransactionHash, Outer: 0, Inner: 0, LogIndex: 1, Slot: 1, BlockHash: "hash1", BlockTime: e.BlockTime, FinalizedSlot: 2, FinalizedHash: "hash2", SettlementID: 7, Raw: json.RawMessage(`{"log":"native"}`)}, Settlement: reconcile.IndexerSettlement{ID: 7, Chain: e.Asset.Network, Asset: e.Asset.Address, TransactionHash: e.TransactionHash, LogIndex: 1, BlockNumber: 1, BlockHash: "hash1", BlockTime: e.BlockTime, Sender: m.Sender, Recipient: m.Recipient, SenderAccount: e.SenderAccount, RecipientAccount: e.RecipientAccount, Amount: e.Amount, FinalizedNumber: 2, FinalizedHash: "hash2", Raw: json.RawMessage(`{"cpi":"proof"}`)}}
	bad := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/escrow-events" {
			t.Error(r.URL.Path)
		}
		cursor, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
		items := []reconcile.IndexerEscrowEnvelope{}
		if cursor == 0 {
			items = append(items, native)
			if bad {
				second := native
				second.Event.ID = 2
				second.Event.Amount = "1"
				items = append(items, second)
			}
		}
		next := cursor
		if len(items) > 0 {
			next = int(items[len(items)-1].Event.ID)
		}
		json.NewEncoder(w).Encode(map[string]any{"data": items, "scope": "scope1", "next_cursor": fmt.Sprint(next)})
	}))
	defer server.Close()
	if _, err := service.ImportEscrowPage(context.Background(), tenant, "source", server.URL, "scope1"); err == nil {
		t.Fatal("bad batch accepted")
	}
	var count int
	service.Store.Pool.QueryRow(context.Background(), "SELECT count(*) FROM records WHERE tenant=$1", tenant).Scan(&count)
	if count != 0 {
		t.Fatal("partial imported evidence", count)
	}
	bad = false
	if n, err := service.ImportEscrowPage(context.Background(), tenant, "source", server.URL, "scope1"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if n, err := service.ImportEscrowPage(context.Background(), tenant, "source", server.URL, "scope1"); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if _, err := service.ImportEscrowPage(context.Background(), tenant, "source", server.URL, "changed"); err == nil {
		t.Fatal("scope changed")
	}
}
