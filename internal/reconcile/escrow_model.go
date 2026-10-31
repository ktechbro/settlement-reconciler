package reconcile

import (
	"fmt"
	"math"
	"strconv"
	"time"
)

const EscrowRuleVersion = "single-full-escrow-v1"

// EscrowDeal is an explicit tenant-owned binding, not an inference from a token transfer.
type EscrowDeal struct {
	ID                 string    `json:"id"`
	Asset              Asset     `json:"asset"`
	Program            string    `json:"program"`
	Config             string    `json:"config"`
	Escrow             string    `json:"escrow"`
	NativeDealID       string    `json:"native_deal_id"`
	Vault              string    `json:"vault"`
	Buyer              string    `json:"buyer"`
	Seller             string    `json:"seller"`
	Amount             string    `json:"amount_atomic"`
	RefundAt           int64     `json:"refund_at"`
	FundingDecisionID  string    `json:"funding_decision_id"`
	FundingBroadcastAt time.Time `json:"funding_broadcast_at"`
	ReleaseDecisionID  string    `json:"release_decision_id,omitempty"`
	ReleaseBroadcastAt time.Time `json:"release_broadcast_at,omitempty"`
}
type EscrowEvent struct {
	ID               string         `json:"id"`
	SourceEventKey   string         `json:"source_event_key"`
	Asset            Asset          `json:"asset"`
	Program          string         `json:"program"`
	Config           string         `json:"config,omitempty"`
	Escrow           string         `json:"escrow"`
	NativeDealID     string         `json:"native_deal_id,omitempty"`
	Kind             string         `json:"kind"`
	Vault            string         `json:"vault"`
	Buyer            string         `json:"buyer,omitempty"`
	Seller           string         `json:"seller,omitempty"`
	Amount           string         `json:"amount_atomic"`
	RefundAt         int64          `json:"refund_at,omitempty"`
	ExpiresAt        int64          `json:"expires_at,omitempty"`
	SettlementID     string         `json:"settlement_id"`
	TransactionHash  string         `json:"transaction_hash"`
	Outer            int            `json:"outer_instruction"`
	Inner            int            `json:"inner_instruction"`
	Slot             int64          `json:"slot"`
	TransactionIndex int            `json:"transaction_index"`
	BlockTime        int64          `json:"block_time"`
	SenderAccount    string         `json:"sender_account"`
	RecipientAccount string         `json:"recipient_account"`
	Evidence         map[string]any `json:"evidence"`
}
type EscrowSummary struct {
	State    string `json:"state"`
	Funded   string `json:"funded_atomic"`
	Released string `json:"released_atomic"`
	Refunded string `json:"refunded_atomic"`
	Locked   string `json:"locked_atomic"`
}

func validateEscrow(v any) error {
	bad := fmt.Errorf("invalid escrow record")
	switch x := v.(type) {
	case EscrowDeal:
		nativeID, err := strconv.ParseUint(x.NativeDealID, 10, 64)
		if !identifier.MatchString(x.ID) || !assetValid(x.Asset) || !addressValid(x.Program) || !addressValid(x.Config) || !addressValid(x.Escrow) || !addressValid(x.Vault) || !addressValid(x.Buyer) || !addressValid(x.Seller) || !amountValid(x.Amount) || x.Amount == "0" || x.RefundAt <= 0 || !identifier.MatchString(x.FundingDecisionID) || x.FundingBroadcastAt.IsZero() || err != nil || strconv.FormatUint(nativeID, 10) != x.NativeDealID {
			return bad
		}
		if (x.ReleaseDecisionID == "") != x.ReleaseBroadcastAt.IsZero() || x.ReleaseDecisionID != "" && !identifier.MatchString(x.ReleaseDecisionID) {
			return bad
		}
	case EscrowEvent:
		if !identifier.MatchString(x.ID) || !identifier.MatchString(x.SettlementID) || !assetValid(x.Asset) || !addressValid(x.Program) || !addressValid(x.Escrow) || !addressValid(x.Vault) || !amountValid(x.Amount) || x.Amount == "0" || !transactionValid(x.TransactionHash) || x.Outer < 0 || x.Outer > math.MaxInt32 || x.Inner < 0 || uint64(x.Inner) >= math.MaxUint32 || x.Slot < 1 || x.TransactionIndex < 0 || x.BlockTime < 1 || !addressValid(x.SenderAccount) || !addressValid(x.RecipientAccount) || len(x.Evidence) == 0 || x.SourceEventKey != Digest([]any{x.Asset.Network, x.Program, x.TransactionHash, x.Outer}) {
			return bad
		}
		if x.Kind != "funding" && x.Kind != "release" && x.Kind != "refund" {
			return bad
		}
		if x.Kind == "release" && (!addressValid(x.Config) || !addressValid(x.Seller) || x.ExpiresAt <= 0) {
			return bad
		}
		if x.Kind == "refund" && !addressValid(x.Buyer) {
			return bad
		}
		if x.Kind == "funding" && (!addressValid(x.Config) || !addressValid(x.Buyer) || !addressValid(x.Seller) || x.RefundAt <= 0) {
			return bad
		}
	default:
		return bad
	}
	return nil
}
