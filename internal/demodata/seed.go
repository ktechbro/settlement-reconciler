// Package demodata supplies synthetic scenarios with an independent expected-status manifest.
package demodata

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ktechbro/settlement-reconciler/internal/reconcile"
)

const Buyer = "0x2222222222222222222222222222222222222222"
const Seller = "0x3333333333333333333333333333333333333333"
const Other = "0x4444444444444444444444444444444444444444"

var Asset = reconcile.Asset{Network: "base-demo", Address: "0x1111111111111111111111111111111111111111"}

func Hash(n int) string { return fmt.Sprintf("0x%064x", n) }

func Seed(ctx context.Context, s *reconcile.Service, tenant string) (map[string]string, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if s.Verifier.Keys == nil {
		s.Verifier.Keys = map[string]reconcile.TrustedKey{}
	}
	kid := "demo-" + tenant
	s.Verifier.Keys[kid] = reconcile.TrustedKey{Issuer: "urn:settlement-reconciler:demo", Tenant: tenant, PublicKey: base64.RawURLEncoding.EncodeToString(public)}
	now := s.Clock()
	write := func(kind, id string, v any) error {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, _, err = s.Write(ctx, tenant, "demo", kind, kind+"-"+id, raw)
		return err
	}
	mapping := reconcile.Mapping{ID: "vendor-v1", Reference: "vendor:demo", Network: Asset.Network, Address: Seller, Version: 1}
	if err = write("mappings", mapping.ID, mapping); err != nil {
		return nil, err
	}
	expected := map[string]string{
		"matched": "matched", "unpaid": "approved_unpaid", "denied": "unapproved_payment", "wrong-amount": "amount_mismatch",
		"wrong-recipient": "recipient_mismatch", "wrong-asset": "asset_or_chain_mismatch", "wrong-payer": "payer_mismatch",
		"duplicate": "duplicate_payment", "ambiguous": "ambiguous", "pending": "awaiting_finality",
		"late-broadcast": "authorization_timing_mismatch", "missing-coverage": "incomplete_evidence",
	}
	labels := []string{"matched", "unpaid", "denied", "wrong-amount", "wrong-recipient", "wrong-asset", "wrong-payer", "duplicate", "ambiguous", "pending", "late-broadcast", "missing-coverage"}
	for i, label := range labels {
		amount := fmt.Sprint(1000000 + i)
		claims := reconcile.Claims{ID: label, Issuer: "urn:settlement-reconciler:demo", Tenant: tenant, Agent: "agent-procurement-01", Mandate: "mandate-01", Verdict: "allow", Asset: Asset, Amount: amount, Payer: Buyer, RecipientRef: mapping.Reference, MappingID: mapping.ID, IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(5 * time.Minute), PaymentDeadline: now.Add(10 * time.Minute)}
		if label == "denied" {
			claims.Verdict = "deny"
		}
		if label == "missing-coverage" {
			claims.Payer = Other
		}
		token, err := reconcile.Sign(claims, kid, private)
		if err != nil {
			return nil, err
		}
		if err = write("decisions", label, reconcile.DecisionInput{Token: token}); err != nil {
			return nil, err
		}
		if label == "missing-coverage" {
			continue
		}
		if label == "unpaid" {
			coverage := reconcile.Coverage{ID: "coverage-unpaid", Asset: Asset, Address: Buyer, From: claims.IssuedAt, Through: claims.PaymentDeadline, Complete: true, Evidence: json.RawMessage(`{"source":"synthetic-complete-scan"}`)}
			if err = write("coverage", coverage.ID, coverage); err != nil {
				return nil, err
			}
			continue
		}
		count := 1
		if label == "duplicate" || label == "ambiguous" {
			count = 2
		}
		for j := 0; j < count; j++ {
			id := fmt.Sprintf("%s-%d", label, j)
			broadcast := now.Add(time.Second)
			block := now.Add(2 * time.Second)
			final := now.Add(20 * time.Minute)
			if label == "late-broadcast" {
				broadcast = now.Add(6 * time.Minute)
				block = now.Add(7 * time.Minute)
			}
			movement := reconcile.Settlement{ID: id, Asset: Asset, TransactionHash: Hash(i*10 + j + 1), EventIndex: "0", Sender: Buyer, Recipient: Seller, Amount: amount, BlockTime: block, Status: "finalized", FinalizedAt: &final, Evidence: json.RawMessage(`{"source":"synthetic-chain-fixture","receipt_status":1}`)}
			if label == "wrong-amount" {
				movement.Amount = "1"
			}
			if label == "wrong-recipient" {
				movement.Recipient = Other
			}
			if label == "wrong-asset" {
				movement.Asset.Address = Other
			}
			if label == "wrong-payer" {
				movement.Sender = Other
			}
			if label == "pending" {
				movement.Status = "observed"
				movement.FinalizedAt = nil
			}
			if label != "ambiguous" {
				attempt := reconcile.Attempt{ID: id, DecisionID: label, Asset: Asset, TransactionHash: movement.TransactionHash, EventIndex: movement.EventIndex, Sender: Buyer, Recipient: Seller, Amount: amount, BroadcastAt: broadcast}
				if err = write("payment-attempts", attempt.ID, attempt); err != nil {
					return nil, err
				}
			}
			if err = write("settlements", movement.ID, movement); err != nil {
				return nil, err
			}
		}
	}
	return expected, nil
}
