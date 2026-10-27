package reconcile_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ktechbro/settlement-reconciler/internal/demodata"
	"github.com/ktechbro/settlement-reconciler/internal/reconcile"
)

var now = time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)

func sample() reconcile.Snapshot {
	m := reconcile.Mapping{ID: "m1", Reference: "vendor:test", Network: demodata.Asset.Network, Address: demodata.Seller, Version: 1}
	claims := reconcile.Claims{ID: "d1", Issuer: "urn:test", Tenant: "tenant-a", Agent: "same-agent", Mandate: "m1", Verdict: "allow", Asset: demodata.Asset, Amount: "50000000", Payer: demodata.Buyer, RecipientRef: m.Reference, MappingID: m.ID, IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute), PaymentDeadline: now.Add(10 * time.Minute)}
	d := reconcile.Decision{ID: claims.ID, Claims: claims, Verification: reconcile.Verification{Mode: "ed25519", SignatureValid: true, AcceptedAt: now}}
	a := reconcile.Attempt{ID: "a1", DecisionID: d.ID, Asset: claims.Asset, TransactionHash: demodata.Hash(1), EventIndex: "0", Sender: claims.Payer, Recipient: m.Address, Amount: claims.Amount, BroadcastAt: now.Add(time.Second)}
	final := now.Add(20 * time.Minute)
	v := reconcile.Settlement{ID: "s1", Asset: claims.Asset, TransactionHash: a.TransactionHash, EventIndex: "0", Sender: a.Sender, Recipient: a.Recipient, Amount: a.Amount, BlockTime: now.Add(2 * time.Second), Status: "finalized", FinalizedAt: &final, Evidence: json.RawMessage(`{"receipt":1}`)}
	return reconcile.Snapshot{Decisions: []reconcile.Decision{d}, Mappings: []reconcile.Mapping{m}, Attempts: []reconcile.Attempt{a}, Settlements: []reconcile.Settlement{v}}
}
func decisionCase(t *testing.T, s reconcile.Snapshot, at time.Time) reconcile.Case {
	t.Helper()
	for _, v := range reconcile.Evaluate(s, at) {
		if v.Key == "decision:d1" {
			return v
		}
	}
	t.Fatal("decision case missing")
	return reconcile.Case{}
}

func TestComparisonAndFinalityRules(t *testing.T) {
	tests := []struct {
		name, status string
		change       func(*reconcile.Snapshot)
	}{
		{"historical expiry does not invalidate match", "matched", func(s *reconcile.Snapshot) {}},
		{"wrong amount", "amount_mismatch", func(s *reconcile.Snapshot) { s.Settlements[0].Amount = "1" }},
		{"wrong recipient", "recipient_mismatch", func(s *reconcile.Snapshot) { s.Settlements[0].Recipient = demodata.Other }},
		{"wrong payer", "payer_mismatch", func(s *reconcile.Snapshot) { s.Settlements[0].Sender = demodata.Other }},
		{"wrong token", "asset_or_chain_mismatch", func(s *reconcile.Snapshot) { s.Settlements[0].Asset.Address = demodata.Other }},
		{"denied but paid", "unapproved_payment", func(s *reconcile.Snapshot) { s.Decisions[0].Claims.Verdict = "deny" }},
		{"missing mapping", "incomplete_evidence", func(s *reconcile.Snapshot) { s.Mappings = nil }},
		{"pending", "awaiting_finality", func(s *reconcile.Snapshot) { s.Settlements[0].Status = "observed"; s.Settlements[0].FinalizedAt = nil }},
		{"late broadcast", "authorization_timing_mismatch", func(s *reconcile.Snapshot) {
			s.Attempts[0].BroadcastAt = now.Add(5 * time.Minute)
			s.Settlements[0].BlockTime = now.Add(6 * time.Minute)
		}},
		{"multiple payments", "duplicate_payment", func(s *reconcile.Snapshot) {
			v := s.Settlements[0]
			v.ID = "s2"
			v.TransactionHash = demodata.Hash(2)
			s.Settlements = append(s.Settlements, v)
			a := s.Attempts[0]
			a.ID = "a2"
			a.TransactionHash = v.TransactionHash
			s.Attempts = append(s.Attempts, a)
		}},
		{"shared payment", "ambiguous", func(s *reconcile.Snapshot) {
			d := s.Decisions[0]
			d.ID = "d2"
			d.Claims.ID = "d2"
			s.Decisions = append(s.Decisions, d)
			a := s.Attempts[0]
			a.ID = "a2"
			a.DecisionID = "d2"
			s.Attempts = append(s.Attempts, a)
		}},
		{"missing locator", "ambiguous", func(s *reconcile.Snapshot) {
			v := s.Settlements[0]
			v.ID = "s2"
			v.EventIndex = "1"
			s.Settlements = append(s.Settlements, v)
			s.Attempts[0].EventIndex = ""
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := sample()
			tt.change(&s)
			got := decisionCase(t, s, now.Add(time.Hour))
			if got.Status != tt.status {
				t.Fatalf("want %s got %s: %+v", tt.status, got.Status, got.Findings)
			}
		})
	}
}
func TestAbsenceRequiresCoverageAndDeadline(t *testing.T) {
	s := sample()
	s.Settlements = nil
	s.Attempts = nil
	if got := decisionCase(t, s, now.Add(time.Hour)); got.Status != "incomplete_evidence" {
		t.Fatal(got)
	}
	s.Coverage = []reconcile.Coverage{{ID: "c1", Asset: demodata.Asset, Address: demodata.Buyer, From: now.Add(-time.Minute), Through: now.Add(10 * time.Minute), Complete: true}}
	if got := decisionCase(t, s, now); got.Status != "incomplete_evidence" {
		t.Fatal(got)
	}
	if got := decisionCase(t, s, now.Add(time.Hour)); got.Status != "approved_unpaid" || len(got.Evidence.Coverage) != 1 {
		t.Fatal(got)
	}
	s.Coverage[0].Complete = false
	if got := decisionCase(t, s, now.Add(time.Hour)); got.Status != "incomplete_evidence" {
		t.Fatal(got)
	}
}
func TestHeuristicsNeverAutoMatchAndOrphansRemainVisible(t *testing.T) {
	s := sample()
	s.Attempts = nil
	if got := decisionCase(t, s, now.Add(time.Hour)); got.Status != "incomplete_evidence" || len(got.Candidates) != 1 {
		t.Fatal(got)
	}
	v := s.Settlements[0]
	v.ID = "s2"
	v.TransactionHash = demodata.Hash(2)
	s.Settlements = append(s.Settlements, v)
	if got := decisionCase(t, s, now.Add(time.Hour)); got.Status != "ambiguous" || len(got.Candidates) != 2 {
		t.Fatal(got)
	}
	count := 0
	for _, v := range reconcile.Evaluate(s, now.Add(time.Hour)) {
		if strings.HasPrefix(v.Key, "payment:") && v.Status == "unapproved_payment" {
			count++
		}
	}
	if count != 2 {
		t.Fatal("orphan payments disappeared")
	}
}
func TestFutureFinalityAndRepeatedAttempts(t *testing.T) {
	s := sample()
	if got := decisionCase(t, s, now.Add(3*time.Second)); got.Status != "awaiting_finality" {
		t.Fatal(got)
	}
	a := s.Attempts[0]
	a.ID = "retry"
	s.Attempts = append(s.Attempts, a)
	if got := decisionCase(t, s, now.Add(time.Hour)); got.Status != "matched" {
		t.Fatal("same movement counted twice", got)
	}
}

func TestDeniedProvisionalMovementStillWaitsForFinality(t *testing.T) {
	s := sample()
	s.Decisions[0].Claims.Verdict = "deny"
	if got := decisionCase(t, s, now.Add(3*time.Second)); got.Status != "awaiting_finality" {
		t.Fatal(got)
	}
	if got := decisionCase(t, s, now.Add(time.Hour)); got.Status != "unapproved_payment" {
		t.Fatal(got)
	}
}
func TestSignatureTrustTimeAndTenantBinding(t *testing.T) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier := reconcile.Verifier{Keys: map[string]reconcile.TrustedKey{"test": {Issuer: "urn:test", Tenant: "tenant-a", PublicKey: base64.RawURLEncoding.EncodeToString(pub)}}}
	claims := sample().Decisions[0].Claims
	token, err := reconcile.Sign(claims, "test", private)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(reconcile.DecisionInput{Token: token})
	d, err := verifier.Accept("tenant-a", raw, now)
	if err != nil || !d.Verification.SignatureValid {
		t.Fatalf("valid token rejected: %v", err)
	}
	if _, err = verifier.Accept("tenant-b", raw, now); err == nil {
		t.Fatal("cross-tenant accepted")
	}
	if _, err = verifier.Accept("tenant-a", raw, now.Add(time.Hour)); err == nil {
		t.Fatal("expired token accepted")
	}
	parts := strings.Split(token, ".")
	parts[1] = base64.RawURLEncoding.EncodeToString([]byte(`{}`))
	raw, _ = json.Marshal(reconcile.DecisionInput{Token: strings.Join(parts, ".")})
	if _, err = verifier.Accept("tenant-a", raw, now); err == nil {
		t.Fatal("tampered token accepted")
	}
	raw, _ = json.Marshal(reconcile.DecisionInput{Fixture: &claims})
	if _, err = verifier.Accept("tenant-a", raw, now); err == nil {
		t.Fatal("fixture enabled implicitly")
	}
}
func TestExactQuantityAndEventIdentity(t *testing.T) {
	v := sample().Settlements[0]
	v.Amount = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)).String()
	if err := reconcile.Validate("settlements", v); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"1.5", "01", "-1"} {
		v.Amount = bad
		if reconcile.Validate("settlements", v) == nil {
			t.Fatal("invalid quantity accepted", bad)
		}
	}
	a := sample().Settlements[0]
	b := a
	b.EventIndex = "1"
	if a.EventKey() == b.EventKey() {
		t.Fatal("movement identity collided")
	}
}

func TestCanonicalEVMIdentityRejectsAliases(t *testing.T) {
	for _, change := range []func(*reconcile.Settlement){
		func(v *reconcile.Settlement) { v.TransactionHash = "0X" + v.TransactionHash[2:] },
		func(v *reconcile.Settlement) { v.Asset.Address = "0X" + v.Asset.Address[2:] },
		func(v *reconcile.Settlement) { v.EventIndex = "00" },
	} {
		v := sample().Settlements[0]
		change(&v)
		if reconcile.Validate("settlements", v) == nil {
			t.Fatal("canonical event alias was accepted")
		}
	}
}
