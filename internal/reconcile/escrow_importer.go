package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

type IndexerEscrowEvent struct {
	ID               int64           `json:"id"`
	EventKey         string          `json:"event_key"`
	Chain            string          `json:"chain"`
	Program          string          `json:"program"`
	Config           string          `json:"config"`
	Escrow           string          `json:"escrow"`
	DealID           string          `json:"deal_id"`
	Kind             string          `json:"kind"`
	Mint             string          `json:"mint"`
	Vault            string          `json:"vault"`
	Buyer            string          `json:"buyer"`
	Seller           string          `json:"seller"`
	Amount           string          `json:"amount_atomic"`
	RefundAt         int64           `json:"refund_at"`
	ExpiresAt        int64           `json:"expires_at"`
	Signature        string          `json:"transaction_hash"`
	Outer            int             `json:"outer_instruction"`
	Inner            int             `json:"inner_instruction"`
	LogIndex         int64           `json:"log_index"`
	Slot             int64           `json:"slot"`
	TransactionIndex int             `json:"transaction_index"`
	BlockHash        string          `json:"block_hash"`
	BlockTime        int64           `json:"block_time"`
	FinalizedSlot    int64           `json:"finalized_number"`
	FinalizedHash    string          `json:"finalized_hash"`
	SettlementID     int64           `json:"settlement_id"`
	Raw              json.RawMessage `json:"evidence"`
}
type IndexerEscrowEnvelope struct {
	Event      IndexerEscrowEvent `json:"event"`
	Settlement IndexerSettlement  `json:"settlement"`
}

// ImportEscrowPage atomically imports verified native actions with their financial evidence.
// Only operator-configured URLs reach this function; it is not a public API route.
func (s *Service) ImportEscrowPage(ctx context.Context, tenant, sourceID, endpoint, expectedScope string) (int, error) {
	if !identifier.MatchString(sourceID) || !identifier.MatchString(tenant) || expectedScope == "" {
		return 0, fmt.Errorf("invalid import identity or expected source scope")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return 0, fmt.Errorf("invalid indexer URL")
	}
	sourceID = "escrow:" + sourceID
	var cursor string
	var pinnedScope string
	err = s.Store.Pool.QueryRow(ctx, "SELECT cursor,scope FROM import_cursors WHERE tenant=$1 AND source=$2", tenant, sourceID).Scan(&cursor, &pinnedScope)
	if err != nil && err != pgx.ErrNoRows {
		return 0, err
	}
	if pinnedScope != "" && pinnedScope != expectedScope {
		return 0, ErrConflict
	}
	if cursor == "" {
		cursor = "0"
	}
	parsed.Path = "/v1/escrow-events"
	q := url.Values{"cursor": []string{cursor}, "limit": []string{"25"}}
	parsed.RawQuery = q.Encode()
	request, err := http.NewRequestWithContext(ctx, "GET", parsed.String(), nil)
	if err != nil {
		return 0, fmt.Errorf("invalid import request")
	}
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("indexer transport unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return 0, fmt.Errorf("indexer HTTP status %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
	if err != nil || len(raw) > 16<<20 {
		return 0, fmt.Errorf("indexer response unavailable or too large")
	}
	var page struct {
		Data  []IndexerEscrowEnvelope `json:"data"`
		Next  string                  `json:"next_cursor"`
		Scope string                  `json:"scope"`
	}
	if json.Unmarshal(raw, &page) != nil || page.Scope != expectedScope {
		return 0, fmt.Errorf("indexer response shape or scope mismatch")
	}
	if len(page.Data) == 0 {
		return 0, nil
	}
	next, err := strconv.ParseInt(page.Next, 10, 64)
	prior, e := strconv.ParseInt(cursor, 10, 64)
	if err != nil || e != nil || next <= prior || page.Data[len(page.Data)-1].Event.ID != next {
		return 0, fmt.Errorf("indexer cursor failed to advance")
	}
	lastID := prior
	movements := []Settlement{}
	events := []EscrowEvent{}
	for _, envelope := range page.Data {
		v, e := envelope.Settlement, envelope.Event
		if e.ID <= lastID || v.BlockNumber > v.FinalizedNumber || v.FinalizedHash == "" || v.BlockHash == "" || v.BlockTime <= 0 || v.LogIndex < 0 {
			return 0, fmt.Errorf("invalid indexer finality evidence")
		}
		lastID = e.ID
		evidence, _ := json.Marshal(v)
		at := s.Clock()
		block := time.Unix(v.BlockTime, 0).UTC()
		if at.Before(block) {
			return 0, fmt.Errorf("indexer block is in the future")
		}
		movement := Settlement{Asset: Asset{Network: v.Chain, Address: v.Asset}, TransactionHash: v.TransactionHash, EventIndex: strconv.FormatInt(v.LogIndex, 10), Sender: v.Sender, Recipient: v.Recipient, Amount: v.Amount, BlockTime: block, Status: "finalized", FinalizedAt: &at, Evidence: evidence}
		movement.ID = "event-" + movement.EventKey()[:40]
		if err := Validate("settlements", movement); err != nil {
			return 0, err
		}
		movements = append(movements, movement)
		if e.Chain != v.Chain || e.Mint != v.Asset || e.Signature != v.TransactionHash || e.SettlementID != v.ID || e.Amount != v.Amount || e.LogIndex != v.LogIndex || e.Slot != v.BlockNumber || e.BlockHash != v.BlockHash || e.BlockTime != v.BlockTime || e.FinalizedSlot < e.Slot || e.FinalizedHash == "" || e.Inner < 0 || e.LogIndex != (int64(e.Outer)<<32)|int64(e.Inner+1) || len(e.Raw) == 0 {
			return 0, fmt.Errorf("native action disagrees with paired financial/finality evidence")
		}
		original, _ := json.Marshal(envelope)
		var proof map[string]any
		_ = json.Unmarshal(original, &proof)
		event := EscrowEvent{ID: "esc-" + e.EventKey, SourceEventKey: e.EventKey, Asset: movement.Asset, Program: e.Program, Config: e.Config, Escrow: e.Escrow, NativeDealID: e.DealID, Kind: e.Kind, Vault: e.Vault, Buyer: e.Buyer, Seller: e.Seller, Amount: e.Amount, RefundAt: e.RefundAt, ExpiresAt: e.ExpiresAt, SettlementID: movement.ID, TransactionHash: e.Signature, Outer: e.Outer, Inner: e.Inner, Slot: e.Slot, TransactionIndex: e.TransactionIndex, BlockTime: e.BlockTime, SenderAccount: v.SenderAccount, RecipientAccount: v.RecipientAccount, Evidence: proof}
		if err := validateEscrow(event); err != nil {
			return 0, err
		}
		events = append(events, event)
	}
	// Retrieval time is retained on first import; the source's finality hash is the actual evidence.
	payload, _ := json.Marshal(map[string]any{"source": sourceID, "cursor": cursor, "scope": page.Scope, "next": page.Next, "data": page.Data})
	_, _, err = s.Store.Mutate(ctx, tenant, "indexer-importer", "import-escrow", Digest([]string{sourceID, cursor}), payload, func(tx pgx.Tx) (any, error) {
		var current string
		err := tx.QueryRow(ctx, "SELECT cursor FROM import_cursors WHERE tenant=$1 AND source=$2", tenant, sourceID).Scan(&current)
		if err != nil && err != pgx.ErrNoRows {
			return nil, err
		}
		if current != "" && current != cursor {
			return nil, ErrConflict
		}
		for _, v := range movements {
			var raw []byte
			err := tx.QueryRow(ctx, "SELECT body FROM records WHERE tenant=$1 AND kind='settlements' AND id=$2", tenant, v.ID).Scan(&raw)
			if err == nil {
				var old Settlement
				if json.Unmarshal(raw, &old) != nil {
					return nil, ErrConflict
				}
				if old.Status == "finalized" && old.ImmutableDigest() == v.ImmutableDigest() {
					continue
				}
			} else if err != pgx.ErrNoRows {
				return nil, err
			}
			if err := Put(ctx, tx, tenant, "settlements", v.ID, v); err != nil {
				return nil, err
			}
		}
		for _, event := range events {
			var raw []byte
			err := tx.QueryRow(ctx, "SELECT body FROM records WHERE tenant=$1 AND kind='escrow-events' AND id=$2", tenant, event.ID).Scan(&raw)
			if err == nil {
				var old EscrowEvent
				if json.Unmarshal(raw, &old) != nil {
					return nil, ErrConflict
				}
				old.Evidence = nil
				core := event
				core.Evidence = nil
				if Digest(old) != Digest(core) {
					return nil, ErrConflict
				}
				continue // Retain first observed native proof on a new source/scope replay.
			} else if err != pgx.ErrNoRows {
				return nil, err
			}
			if err := Put(ctx, tx, tenant, "escrow-events", event.ID, event); err != nil {
				return nil, err
			}
		}
		if _, err = tx.Exec(ctx, `INSERT INTO import_cursors(tenant,source,scope,cursor) VALUES($1,$2,$3,$4) ON CONFLICT(tenant,source) DO UPDATE SET cursor=EXCLUDED.cursor`, tenant, sourceID, page.Scope, page.Next); err != nil {
			return nil, err
		}
		if _, err = Reconcile(ctx, tx, tenant, s.Clock()); err != nil {
			return nil, err
		}
		return map[string]any{"imported": len(movements)}, nil
	})
	return len(movements), err
}
