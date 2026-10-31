package reconcile

import (
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"time"
)

// Evaluate preserves direct-payment semantics and separately accounts for escrow movements.
func Evaluate(s Snapshot, asOf time.Time) []Case {
	if len(s.Escrows) == 0 && len(s.EscrowEvents) == 0 {
		return evaluateDirect(s, asOf)
	}
	s.Escrows = append([]EscrowDeal(nil), s.Escrows...)
	s.Settlements = append([]Settlement(nil), s.Settlements...)
	sort.Slice(s.Escrows, func(i, j int) bool { return s.Escrows[i].ID < s.Escrows[j].ID })
	sort.Slice(s.Settlements, func(i, j int) bool { return s.Settlements[i].ID < s.Settlements[j].ID })
	cases, decisionClaims, movementClaims := evaluateEscrows(s, asOf)
	direct := s
	direct.Decisions = nil
	direct.Attempts = nil
	direct.Settlements = nil
	for _, d := range s.Decisions {
		if !decisionClaims[d.ID] {
			direct.Decisions = append(direct.Decisions, d)
		}
	}
	for _, a := range s.Attempts {
		if !decisionClaims[a.DecisionID] {
			direct.Attempts = append(direct.Attempts, a)
		}
	}
	for _, m := range s.Settlements {
		if !movementClaims[m.ID] {
			direct.Settlements = append(direct.Settlements, m)
		}
	}
	cases = append(cases, evaluateDirect(direct, asOf)...)
	sort.Slice(cases, func(i, j int) bool { return cases[i].Key < cases[j].Key })
	return cases
}

func evaluateEscrows(s Snapshot, asOf time.Time) ([]Case, map[string]bool, map[string]bool) {
	decisions := map[string]Decision{}
	for _, d := range s.Decisions {
		decisions[d.ID] = d
	}
	mappings := map[string]Mapping{}
	for _, m := range s.Mappings {
		mappings[m.ID] = m
	}
	movements := map[string]Settlement{}
	for _, m := range s.Settlements {
		movements[m.ID] = m
	}
	decisionOwners := map[string][]string{}
	eventOwners := map[string][]string{}
	claimedDecisions := map[string]bool{}
	claimedMovements := map[string]bool{}
	usedEvents := map[string]bool{}
	cases := []Case{}
	for _, d := range s.Escrows {
		for _, id := range []string{d.FundingDecisionID, d.ReleaseDecisionID} {
			if id != "" {
				decisionOwners[id] = append(decisionOwners[id], d.ID)
				claimedDecisions[id] = true
			}
		}
		for _, e := range s.EscrowEvents {
			if e.Asset.Network == d.Asset.Network && e.Program == d.Program && e.Escrow == d.Escrow {
				eventOwners[e.SettlementID] = append(eventOwners[e.SettlementID], d.ID)
			}
		}
	}
	for _, d := range s.Escrows {
		c := newCase("escrow:"+d.ID, d.ID)
		c.RuleVersion = EscrowRuleVersion
		c.Category = "escrow"
		c.Evidence.Escrows = []string{d.ID}
		funded, released, refunded := new(big.Int), new(big.Int), new(big.Int)
		state := "created"
		var funding *EscrowEvent
		terminal := false
		events := []EscrowEvent{}
		for _, e := range s.EscrowEvents {
			if e.Asset.Network == d.Asset.Network && e.Program == d.Program && e.Escrow == d.Escrow {
				events = append(events, e)
			}
		}
		sort.Slice(events, func(i, j int) bool {
			if events[i].Slot != events[j].Slot {
				return events[i].Slot < events[j].Slot
			}
			if events[i].TransactionIndex != events[j].TransactionIndex {
				return events[i].TransactionIndex < events[j].TransactionIndex
			}
			return events[i].Outer < events[j].Outer
		})
		for _, id := range []string{d.FundingDecisionID, d.ReleaseDecisionID} {
			if id != "" {
				c.Evidence.Decisions = append(c.Evidence.Decisions, id)
				if len(decisionOwners[id]) > 1 {
					add(&c, "ambiguous", "authorization is claimed by more than one escrow action/deal")
				}
				for _, a := range s.Attempts {
					if a.DecisionID == id {
						add(&c, "ambiguous", "authorization also has a direct-payment attempt")
					}
				}
			}
		}
		for _, e := range events {
			usedEvents[e.ID] = true
			claimedMovements[e.SettlementID] = true
			c.Evidence.EscrowEvents = append(c.Evidence.EscrowEvents, e.ID)
			c.Evidence.Settlements = append(c.Evidence.Settlements, e.SettlementID)
			if len(eventOwners[e.SettlementID]) > 1 {
				add(&c, "ambiguous", "movement is attributed to multiple escrow actions/deals")
			}
			for _, attempt := range s.Attempts {
				if attempt.TransactionHash == e.TransactionHash && (attempt.EventIndex == "" || attempt.EventIndex == strconv.FormatInt((int64(e.Outer)<<32)|int64(e.Inner+1), 10)) {
					add(&c, "ambiguous", "escrow movement also claimed by a direct-payment attempt")
				}
			}
			m, ok := movements[e.SettlementID]
			if !ok {
				add(&c, "incomplete_evidence", "escrow movement evidence is missing")
				continue
			}
			if !isFinal(m, asOf) || e.BlockTime > asOf.Unix() {
				add(&c, "awaiting_finality", "escrow movement is not finalized at evaluation time")
				continue
			}
			locator := (int64(e.Outer) << 32) | int64(e.Inner+1)
			if e.Asset != d.Asset || m.Asset != d.Asset || m.TransactionHash != e.TransactionHash || m.EventIndex != strconv.FormatInt(locator, 10) || m.BlockTime.Unix() != e.BlockTime || m.Amount != e.Amount || e.Amount != d.Amount || e.Vault != d.Vault {
				add(&c, "escrow_mismatch", "event, transfer, asset, locator or principal disagrees with the pinned deal")
			}
			n, valid := new(big.Int).SetString(m.Amount, 10)
			if !valid || n.Sign() < 0 {
				add(&c, "escrow_mismatch", "invalid movement quantity")
				continue
			}
			switch e.Kind {
			case "funding":
				funded.Add(funded, n)
				if funding != nil || terminal {
					add(&c, "invalid_escrow_transition", "funding repeats or follows terminal action")
				}
				copy := e
				if funding == nil {
					funding = &copy
				}
				state = "funded"
				if e.Config != d.Config || e.NativeDealID != d.NativeDealID || e.Buyer != d.Buyer || e.Seller != d.Seller || e.RefundAt != d.RefundAt || e.BlockTime >= d.RefundAt || m.Sender != d.Buyer || m.Recipient != d.Escrow || e.RecipientAccount != d.Vault {
					add(&c, "escrow_mismatch", "funding terms or vault direction mismatch")
				}
				escrowAuthorization(&c, decisions, mappings, d.FundingDecisionID, d.Asset, d.Amount, d.Buyer, d.Seller, d.FundingBroadcastAt, e.BlockTime)
			case "release", "refund":
				if terminal {
					add(&c, "invalid_escrow_transition", "multiple terminal actions")
				}
				terminal = true
				if funding == nil {
					add(&c, "incomplete_evidence", "release/refund lacks earlier funding history")
				}
				if m.Sender != d.Escrow || e.SenderAccount != d.Vault {
					add(&c, "escrow_mismatch", "terminal movement does not leave the pinned vault")
				}
				if e.Kind == "release" {
					released.Add(released, n)
					state = "released"
					if e.Config != d.Config || e.Seller != d.Seller || m.Recipient != d.Seller || e.BlockTime >= d.RefundAt || e.ExpiresAt <= e.BlockTime || e.ExpiresAt > d.RefundAt {
						add(&c, "escrow_mismatch", "seller, release config or expiry/deadline mismatch")
					}
					if d.ReleaseDecisionID == "" {
						add(&c, "incomplete_evidence", "seller release requires an explicit release authorization link")
					} else {
						escrowAuthorization(&c, decisions, mappings, d.ReleaseDecisionID, d.Asset, d.Amount, d.Escrow, d.Seller, d.ReleaseBroadcastAt, e.BlockTime)
					}
				} else {
					refunded.Add(refunded, n)
					state = "refunded"
					if e.Buyer != d.Buyer || m.Recipient != d.Buyer || e.BlockTime < d.RefundAt {
						add(&c, "escrow_mismatch", "refund recipient or deadline mismatch")
					}
				}
			default:
				add(&c, "escrow_mismatch", "unsupported escrow action")
			}
		}
		if len(events) == 0 {
			add(&c, "incomplete_evidence", "no finalized escrow lifecycle history")
		}
		locked := new(big.Int).Sub(new(big.Int).Set(funded), released)
		locked.Sub(locked, refunded)
		if funding != nil && locked.Sign() < 0 {
			add(&c, "escrow_balance_mismatch", "recorded releases/refunds exceed funding")
		}
		lockedValue := locked.String()
		if funding == nil {
			lockedValue = "unknown"
		}
		c.Escrow = &EscrowSummary{State: state, Funded: funded.String(), Released: released.String(), Refunded: refunded.String(), Locked: lockedValue}
		// Any owner-level transfer outside the verified native actions is an exception.
		for _, movement := range s.Settlements {
			if movement.Asset != d.Asset || !isFinal(movement, asOf) || (movement.Sender != d.Escrow && movement.Recipient != d.Escrow) {
				continue
			}
			known := false
			for _, event := range events {
				if event.SettlementID == movement.ID {
					known = true
				}
			}
			if !known {
				add(&c, "escrow_balance_mismatch", "unattributed transfer involving escrow owner")
				c.Evidence.Settlements = append(c.Evidence.Settlements, movement.ID)
				claimedMovements[movement.ID] = true
				eventOwners[movement.ID] = append(eventOwners[movement.ID], d.ID)
			}
		}
		c.Status = "escrow_" + state
		for _, code := range []string{"ambiguous", "invalid_escrow_transition", "escrow_balance_mismatch", "escrow_mismatch", "unapproved_payment", "incomplete_evidence", "awaiting_finality"} {
			if has(c, code) {
				c.Status = code
				break
			}
		}
		cases = append(cases, c)
	}
	// Separate action cases preserve the economic category even when a deal is in exception.
	dealCases := map[string]Case{}
	for _, c := range cases {
		dealCases[c.Subject] = c
	}
	decisionIDs := []string{}
	for id := range claimedDecisions {
		decisionIDs = append(decisionIDs, id)
	}
	sort.Strings(decisionIDs)
	for _, id := range decisionIDs {
		owners := decisionOwners[id]
		c := dealCases[owners[0]]
		c.Key = "decision:" + id
		c.Subject = id
		c.Category = "escrow_authorization"
		if len(owners) > 1 {
			c.Status = "ambiguous"
		}
		cases = append(cases, c)
		for _, a := range s.Attempts {
			if a.DecisionID == id {
				ac := c
				ac.Key = "attempt:" + a.ID
				ac.Subject = a.ID
				ac.Status = "ambiguous"
				cases = append(cases, ac)
			}
		}
	}
	movementIDs := []string{}
	for id := range eventOwners {
		movementIDs = append(movementIDs, id)
	}
	sort.Strings(movementIDs)
	for _, id := range movementIDs {
		owners := eventOwners[id]
		c := dealCases[owners[0]]
		c.Key = "payment:" + id
		c.Subject = id
		c.Category = "escrow_movement"
		if len(owners) > 1 {
			c.Status = "ambiguous"
		}
		cases = append(cases, c)
	}
	for _, e := range s.EscrowEvents {
		if usedEvents[e.ID] {
			owners := eventOwners[e.SettlementID]
			c := dealCases[owners[0]]
			c.Key = "escrow-event:" + e.ID
			c.Subject = e.ID
			c.Category = "escrow_movement"
			cases = append(cases, c)
			continue
		}
		claimedMovements[e.SettlementID] = true
		c := newCase("escrow-event:"+e.ID, e.ID)
		c.RuleVersion = EscrowRuleVersion
		c.Category = "escrow_unbound"
		c.Evidence.EscrowEvents = []string{e.ID}
		c.Evidence.Settlements = []string{e.SettlementID}
		add(&c, "incomplete_evidence", "indexed escrow action has no explicit tenant deal binding")
		cases = append(cases, c)
		// Keep the same payment key so an earlier generic orphan finding gets superseded.
		c.Key = "payment:" + e.SettlementID
		c.Subject = e.SettlementID
		cases = append(cases, c)
	}
	return cases, claimedDecisions, claimedMovements
}
func has(c Case, code string) bool {
	for _, f := range c.Findings {
		if f.Code == code {
			return true
		}
	}
	return false
}
func escrowAuthorization(c *Case, decisions map[string]Decision, mappings map[string]Mapping, id string, asset Asset, amount, payer, payee string, broadcast time.Time, blockTime int64) {
	d, ok := decisions[id]
	if !ok {
		add(c, "incomplete_evidence", "missing linked authorization "+id)
		return
	}
	if d.Claims.Verdict != "allow" {
		add(c, "unapproved_payment", "escrow action links a denied authorization")
		return
	}
	m, ok := mappings[d.Claims.MappingID]
	if !ok {
		add(c, "incomplete_evidence", "authorization recipient mapping missing")
		return
	}
	c.Evidence.Mappings = append(c.Evidence.Mappings, m.ID)
	if d.Claims.Asset != asset || d.Claims.Amount != amount || d.Claims.Payer != payer || m.Address != payee || m.Network != asset.Network || m.Reference != d.Claims.RecipientRef {
		add(c, "escrow_mismatch", fmt.Sprint("authorization ", id, " does not bind expected payer, beneficiary, asset and principal"))
	}
	if blockTime > d.Claims.PaymentDeadline.Unix() || broadcast.IsZero() || broadcast.Before(d.Verification.AcceptedAt) || !broadcast.Before(d.Claims.ExpiresAt) || blockTime < broadcast.Unix() {
		add(c, "escrow_mismatch", "authorization-use time is outside its accepted validity window")
	}
}
