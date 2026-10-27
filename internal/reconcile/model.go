package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const RuleVersion = "single-full-transfer-v1"

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

type Asset struct {
	Network string `json:"network"`
	Address string `json:"address"`
}
type Claims struct {
	ID              string    `json:"id"`
	Issuer          string    `json:"issuer"`
	Tenant          string    `json:"tenant_id"`
	Agent           string    `json:"agent_id"`
	Mandate         string    `json:"mandate_id"`
	Verdict         string    `json:"verdict"`
	Asset           Asset     `json:"asset"`
	Amount          string    `json:"amount_atomic"`
	Payer           string    `json:"payer"`
	RecipientRef    string    `json:"recipient_reference"`
	MappingID       string    `json:"mapping_id"`
	IssuedAt        time.Time `json:"issued_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	PaymentDeadline time.Time `json:"payment_deadline"`
}
type Verification struct {
	PublicKey      string    `json:"public_key,omitempty"`
	Mode           string    `json:"mode"`
	KeyID          string    `json:"key_id,omitempty"`
	AcceptedAt     time.Time `json:"accepted_at"`
	SignatureValid bool      `json:"signature_valid"`
}
type Decision struct {
	ID           string          `json:"id"`
	Claims       Claims          `json:"claims"`
	Verification Verification    `json:"verification"`
	Original     json.RawMessage `json:"original"`
}
type DecisionInput struct {
	Token   string  `json:"token,omitempty"`
	Fixture *Claims `json:"fixture,omitempty"`
}
type Mapping struct {
	ID        string `json:"id"`
	Reference string `json:"recipient_reference"`
	Network   string `json:"network"`
	Address   string `json:"address"`
	Version   int    `json:"version"`
}
type Attempt struct {
	ID              string    `json:"id"`
	DecisionID      string    `json:"decision_id"`
	Asset           Asset     `json:"asset"`
	TransactionHash string    `json:"transaction_hash"`
	EventIndex      string    `json:"event_index,omitempty"`
	Sender          string    `json:"expected_sender"`
	Recipient       string    `json:"expected_recipient"`
	Amount          string    `json:"amount_atomic"`
	BroadcastAt     time.Time `json:"broadcast_at"`
}
type Settlement struct {
	ID              string          `json:"id"`
	Asset           Asset           `json:"asset"`
	TransactionHash string          `json:"transaction_hash"`
	EventIndex      string          `json:"event_index"`
	Sender          string          `json:"sender"`
	Recipient       string          `json:"recipient"`
	Amount          string          `json:"amount_atomic"`
	BlockTime       time.Time       `json:"block_time"`
	FinalizedAt     *time.Time      `json:"finalized_at,omitempty"`
	Status          string          `json:"status"`
	Evidence        json.RawMessage `json:"evidence"`
}
type Coverage struct {
	ID       string          `json:"id"`
	Asset    Asset           `json:"asset"`
	Address  string          `json:"watch_address"`
	From     time.Time       `json:"from_time"`
	Through  time.Time       `json:"through_time"`
	Complete bool            `json:"complete"`
	Evidence json.RawMessage `json:"evidence"`
}
type Resolution struct {
	ID      string    `json:"id"`
	CaseKey string    `json:"case_key"`
	Version int       `json:"case_version"`
	Action  string    `json:"action"`
	Note    string    `json:"note"`
	Actor   string    `json:"actor"`
	At      time.Time `json:"created_at"`
}
type ResolutionInput struct {
	ID      string `json:"id"`
	CaseKey string `json:"case_key"`
	Version int    `json:"case_version"`
	Action  string `json:"action"`
	Note    string `json:"note"`
}
type Snapshot struct {
	Decisions   []Decision
	Mappings    []Mapping
	Attempts    []Attempt
	Settlements []Settlement
	Coverage    []Coverage
}
type Finding struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}
type Evidence struct {
	Decisions   []string `json:"decisions"`
	Mappings    []string `json:"mappings"`
	Attempts    []string `json:"attempts"`
	Settlements []string `json:"settlements"`
	Coverage    []string `json:"coverage"`
}
type Case struct {
	Key         string    `json:"key"`
	Subject     string    `json:"subject"`
	Status      string    `json:"status"`
	RuleVersion string    `json:"rule_version"`
	Findings    []Finding `json:"findings"`
	Candidates  []string  `json:"candidates"`
	Evidence    Evidence  `json:"evidence"`
}
type Version struct {
	Case            Case      `json:"case"`
	Version         int       `json:"version"`
	PreviousVersion int       `json:"previous_version"`
	AsOf            time.Time `json:"as_of"`
	Fingerprint     string    `json:"fingerprint"`
}

func Digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (s Settlement) EventKey() string {
	return Digest([]string{s.Asset.Network, s.TransactionHash, s.EventIndex})
}
func (s Settlement) ImmutableDigest() string {
	s.Status = ""
	s.FinalizedAt = nil
	s.Evidence = nil
	return Digest(s)
}
func amountValid(s string) bool {
	n, ok := new(big.Int).SetString(s, 10)
	return ok && n.Sign() >= 0 && n.BitLen() <= 256 && n.String() == s
}
func textValid(s string) bool              { return len(s) > 0 && len(s) <= 256 && strings.TrimSpace(s) == s }
func assetValid(a Asset) bool              { return textValid(a.Network) && addressValid(a.Address) }
func evidenceValid(v json.RawMessage) bool { return len(v) > 0 && string(v) != "null" && json.Valid(v) }

// Addresses are explicit canonical identifiers. EVM identifiers must be lower-case.
func addressValid(s string) bool {
	if !textValid(s) {
		return false
	}
	if strings.HasPrefix(strings.ToLower(s), "0x") {
		if len(s) != 42 || s != strings.ToLower(s) {
			return false
		}
		_, err := hex.DecodeString(s[2:])
		return err == nil
	}
	return true
}

func transactionValid(s string) bool {
	if !textValid(s) {
		return false
	}
	if strings.HasPrefix(strings.ToLower(s), "0x") {
		if len(s) != 66 || s != strings.ToLower(s) {
			return false
		}
		_, err := hex.DecodeString(s[2:])
		return err == nil
	}
	return true
}
func locatorValid(tx, index string, optional bool) bool {
	if index == "" {
		return optional
	}
	if len(index) > 100 {
		return false
	}
	if strings.HasPrefix(tx, "0x") {
		n, err := strconv.ParseUint(index, 10, 64)
		return err == nil && strconv.FormatUint(n, 10) == index
	}
	return textValid(index)
}

func Validate(kind string, v any) error {
	bad := func() error { return fmt.Errorf("invalid %s record", kind) }
	switch x := v.(type) {
	case Mapping:
		if !identifier.MatchString(x.ID) || !textValid(x.Reference) || !textValid(x.Network) || !addressValid(x.Address) || x.Version < 1 {
			return bad()
		}
	case Attempt:
		if !identifier.MatchString(x.ID) || !identifier.MatchString(x.DecisionID) || !assetValid(x.Asset) || !transactionValid(x.TransactionHash) || !locatorValid(x.TransactionHash, x.EventIndex, true) || !addressValid(x.Sender) || !addressValid(x.Recipient) || !amountValid(x.Amount) || x.Amount == "0" || x.BroadcastAt.IsZero() {
			return bad()
		}
	case Settlement:
		if !identifier.MatchString(x.ID) || !assetValid(x.Asset) || !transactionValid(x.TransactionHash) || !locatorValid(x.TransactionHash, x.EventIndex, false) || !addressValid(x.Sender) || !addressValid(x.Recipient) || !amountValid(x.Amount) || x.BlockTime.IsZero() || !evidenceValid(x.Evidence) || (x.Status != "observed" && x.Status != "finalized") {
			return bad()
		}
		if x.Status == "finalized" && (x.FinalizedAt == nil || x.FinalizedAt.Before(x.BlockTime)) {
			return bad()
		}
		if x.Status == "observed" && x.FinalizedAt != nil {
			return bad()
		}
	case Coverage:
		if !identifier.MatchString(x.ID) || !assetValid(x.Asset) || !addressValid(x.Address) || x.From.IsZero() || x.Through.Before(x.From) || !evidenceValid(x.Evidence) {
			return bad()
		}
	default:
		return bad()
	}
	return nil
}

func ValidateClaims(c Claims, tenant string, now time.Time) error {
	if !identifier.MatchString(c.ID) || c.Tenant != tenant || !textValid(c.Issuer) || !textValid(c.Agent) || !textValid(c.Mandate) || (c.Verdict != "allow" && c.Verdict != "deny") || !assetValid(c.Asset) || !amountValid(c.Amount) || c.Amount == "0" || !addressValid(c.Payer) || !textValid(c.RecipientRef) || !identifier.MatchString(c.MappingID) {
		return fmt.Errorf("invalid decision claims or tenant")
	}
	if c.IssuedAt.IsZero() || c.IssuedAt.After(now) || !c.ExpiresAt.After(now) || !c.ExpiresAt.After(c.IssuedAt) || c.PaymentDeadline.Before(c.ExpiresAt) {
		return fmt.Errorf("decision is not valid at acceptance or has invalid deadline")
	}
	return nil
}
