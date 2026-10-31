package reconcile

import (
	"fmt"
	"sort"
	"time"
)

func newCase(key, subject string) Case {
	return Case{Key: key, Subject: subject, Status: "incomplete_evidence", RuleVersion: RuleVersion, Findings: []Finding{}, Candidates: []string{}, Evidence: Evidence{Decisions: []string{}, Mappings: []string{}, Attempts: []string{}, Settlements: []string{}, Coverage: []string{}}}
}
func add(c *Case, code, detail string) {
	for _, f := range c.Findings {
		if f.Code == code && f.Detail == detail {
			return
		}
	}
	c.Findings = append(c.Findings, Finding{Code: code, Detail: detail})
}

// Evaluate has no clock, database, network or signature-verification side effects.
// asOf is explicit; expiry is checked at acceptance/broadcast, never at finalization.
func evaluateDirect(s Snapshot, asOf time.Time) []Case {
	mappings := map[string]Mapping{}
	for _, m := range s.Mappings {
		mappings[m.ID] = m
	}
	decisions := map[string]Decision{}
	for _, d := range s.Decisions {
		decisions[d.ID] = d
	}
	attempts := map[string][]Attempt{}
	for _, a := range s.Attempts {
		attempts[a.DecisionID] = append(attempts[a.DecisionID], a)
	}
	results := []Case{}
	// A movement can support at most one explicitly linked decision.
	claims := map[string]map[string]bool{}
	for _, a := range s.Attempts {
		if _, ok := decisions[a.DecisionID]; !ok {
			continue
		}
		for _, v := range s.Settlements {
			if attemptSelects(a, v) {
				if claims[v.ID] == nil {
					claims[v.ID] = map[string]bool{}
				}
				claims[v.ID][a.DecisionID] = true
			}
		}
	}
	for _, d := range s.Decisions {
		c := newCase("decision:"+d.ID, d.ID)
		c.Evidence.Decisions = append(c.Evidence.Decisions, d.ID)
		m, mapped := mappings[d.Claims.MappingID]
		if mapped {
			c.Evidence.Mappings = append(c.Evidence.Mappings, m.ID)
		}
		relevant := map[string]Settlement{}
		for _, a := range attempts[d.ID] {
			c.Evidence.Attempts = append(c.Evidence.Attempts, a.ID)
			for _, v := range s.Settlements {
				if attemptSelects(a, v) {
					relevant[v.ID] = v
				}
			}
		}
		values := []Settlement{}
		for _, v := range relevant {
			values = append(values, v)
		}
		sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
		finals := 0
		observed := false
		shared := false
		for _, v := range values {
			c.Evidence.Settlements = append(c.Evidence.Settlements, v.ID)
			if isFinal(v, asOf) {
				finals++
			} else {
				observed = true
			}
			if len(claims[v.ID]) > 1 {
				shared = true
			}
		}
		if !mapped || m.Reference != d.Claims.RecipientRef || m.Network != d.Claims.Asset.Network {
			add(&c, "incomplete_evidence", "pinned recipient mapping is missing or does not bind this reference/network")
		}
		if d.Claims.Verdict != "allow" {
			c.Status = "denied"
			if len(values) > 0 {
				c.Status = "awaiting_finality"
				if finals > 0 {
					c.Status = "unapproved_payment"
				}
				add(&c, c.Status, "linked movement references a denied decision")
			}
			results = append(results, c)
			continue
		}
		for _, a := range attempts[d.ID] {
			if a.BroadcastAt.Before(d.Verification.AcceptedAt) || !a.BroadcastAt.Before(d.Claims.ExpiresAt) {
				add(&c, "authorization_timing_mismatch", "broadcast must follow acceptance and precede authorization expiry")
			}
			compare(&c, a.Asset, a.Sender, a.Recipient, a.Amount, d, m, mapped, "payment attempt "+a.ID)
			for _, v := range values {
				if attemptSelects(a, v) && v.BlockTime.Before(a.BroadcastAt) {
					add(&c, "authorization_timing_mismatch", "chain inclusion predates reported broadcast")
				}
			}
		}
		for _, v := range values {
			compare(&c, v.Asset, v.Sender, v.Recipient, v.Amount, d, m, mapped, "movement "+v.ID)
		}
		if shared {
			add(&c, "ambiguous", "one movement is claimed by multiple decisions")
		}
		if finals > 1 {
			add(&c, "duplicate_payment", "multiple finalized movements reference one single-use approval")
		}
		// An attempt without an event locator cannot disambiguate multiple movements.
		for _, a := range attempts[d.ID] {
			n := 0
			for _, v := range values {
				if attemptSelects(a, v) {
					n++
				}
			}
			if n > 1 && a.EventIndex == "" {
				add(&c, "ambiguous", "attempt "+a.ID+" requires an event locator")
			}
		}
		if len(values) == 0 {
			for _, v := range s.Settlements {
				if mapped && v.Asset == d.Claims.Asset && v.Amount == d.Claims.Amount && v.Sender == d.Claims.Payer && v.Recipient == m.Address && !v.BlockTime.After(asOf) && !v.BlockTime.Before(d.Claims.IssuedAt) && !v.BlockTime.After(d.Claims.PaymentDeadline) {
					c.Candidates = append(c.Candidates, v.ID)
				}
			}
			sort.Strings(c.Candidates)
			if len(c.Candidates) > 1 {
				add(&c, "ambiguous", "multiple heuristic candidates; no automatic match")
			}
			if len(c.Candidates) > 0 {
				add(&c, "incomplete_evidence", "candidate movements need an explicit verified payment-attempt link")
			}
			if len(c.Candidates) == 0 && len(c.Findings) == 0 {
				if asOf.Before(d.Claims.PaymentDeadline) {
					add(&c, "incomplete_evidence", "payment deadline has not passed; no linked movement")
				} else {
					complete := false
					for _, coverage := range s.Coverage {
						if coverage.Complete && coverage.Asset == d.Claims.Asset && coverage.Address == d.Claims.Payer && !coverage.From.After(d.Claims.IssuedAt) && !coverage.Through.Before(d.Claims.PaymentDeadline) {
							complete = true
							c.Evidence.Coverage = append(c.Evidence.Coverage, coverage.ID)
						}
					}
					if complete {
						add(&c, "approved_unpaid", "deadline passed with complete declared source coverage")
					} else {
						add(&c, "incomplete_evidence", "insufficient finalized coverage to conclude payment is absent")
					}
				}
			}
		}
		if len(values) > 0 && finals == 0 && len(c.Findings) == 0 {
			add(&c, "awaiting_finality", "linked movement has not finalized")
		}
		if finals == 1 && observed {
			add(&c, "awaiting_finality", "another explicitly linked movement is still provisional")
		}
		c.Status = primary(c.Findings)
		if len(c.Findings) == 0 && finals == 1 {
			c.Status = "matched"
		}
		if len(c.Findings) == 0 && finals == 0 {
			c.Status = "incomplete_evidence"
		}
		results = append(results, c)
	}
	for _, a := range s.Attempts {
		c := newCase("attempt:"+a.ID, a.ID)
		c.Evidence.Attempts = append(c.Evidence.Attempts, a.ID)
		if _, ok := decisions[a.DecisionID]; !ok {
			add(&c, "incomplete_evidence", "referenced decision is missing")
		} else {
			c.Status = "linked"
			c.Evidence.Decisions = append(c.Evidence.Decisions, a.DecisionID)
		}
		results = append(results, c)
	}
	decisionStatus := map[string]string{}
	for _, c := range results {
		decisionStatus[c.Key] = c.Status
	}
	for _, v := range s.Settlements {
		c := newCase("payment:"+v.ID, v.ID)
		c.Evidence.Settlements = append(c.Evidence.Settlements, v.ID)
		for id := range claims[v.ID] {
			c.Evidence.Decisions = append(c.Evidence.Decisions, id)
		}
		sort.Strings(c.Evidence.Decisions)
		if len(c.Evidence.Decisions) == 0 {
			c.Status = "unapproved_payment"
			if !isFinal(v, asOf) {
				c.Status = "awaiting_finality"
			}
			add(&c, c.Status, "movement has no explicit link to a known decision")
		} else if len(c.Evidence.Decisions) > 1 {
			c.Status = "ambiguous"
			add(&c, "ambiguous", "movement is claimed by multiple decisions")
		} else {
			c.Status = decisionStatus["decision:"+c.Evidence.Decisions[0]]
			add(&c, c.Status, "see linked decision case for full comparison evidence")
		}
		results = append(results, c)
	}
	for i := range results {
		sort.Strings(results[i].Evidence.Attempts)
		sort.Strings(results[i].Evidence.Coverage)
		sort.Slice(results[i].Findings, func(a, b int) bool { return fmt.Sprint(results[i].Findings[a]) < fmt.Sprint(results[i].Findings[b]) })
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Key < results[j].Key })
	return results
}

func attemptSelects(a Attempt, s Settlement) bool {
	return a.Asset.Network == s.Asset.Network && a.TransactionHash == s.TransactionHash && (a.EventIndex == "" || a.EventIndex == s.EventIndex)
}
func isFinal(s Settlement, asOf time.Time) bool {
	return s.Status == "finalized" && s.FinalizedAt != nil && !s.FinalizedAt.After(asOf)
}
func compare(c *Case, asset Asset, sender, recipient, amount string, d Decision, m Mapping, mapped bool, source string) {
	if asset != d.Claims.Asset {
		add(c, "asset_or_chain_mismatch", source+" differs from approved asset/network")
	}
	if sender != d.Claims.Payer {
		add(c, "payer_mismatch", source+" differs from approved payer")
	}
	if mapped && recipient != m.Address {
		add(c, "recipient_mismatch", source+" differs from pinned recipient")
	}
	if amount != d.Claims.Amount {
		add(c, "amount_mismatch", source+" differs from exact approved atomic quantity; partial/split payments unsupported")
	}
}
func primary(findings []Finding) string {
	for _, code := range []string{"ambiguous", "duplicate_payment", "asset_or_chain_mismatch", "payer_mismatch", "recipient_mismatch", "amount_mismatch", "authorization_timing_mismatch", "incomplete_evidence", "awaiting_finality", "approved_unpaid"} {
		for _, f := range findings {
			if f.Code == code {
				return code
			}
		}
	}
	return "incomplete_evidence"
}
