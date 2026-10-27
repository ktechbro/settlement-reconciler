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

type IndexerSettlement struct {
	ID              int64           `json:"id"`
	Chain           string          `json:"chain"`
	Asset           string          `json:"asset"`
	TransactionHash string          `json:"transaction_hash"`
	LogIndex        int64           `json:"log_index"`
	BlockNumber     int64           `json:"block_number"`
	BlockHash       string          `json:"block_hash"`
	BlockTime       int64           `json:"block_time"`
	Sender          string          `json:"sender"`
	Recipient       string          `json:"recipient"`
	Amount          string          `json:"amount_atomic"`
	Provider        string          `json:"provider"`
	FinalizedNumber int64           `json:"finalized_number"`
	FinalizedHash   string          `json:"finalized_hash"`
	Raw             json.RawMessage `json:"raw_log"`
}

// ImportPage consumes Project 1's scope-filtered REST API. Cursor and evidence commit together.
// Only operator-configured URLs reach this function; it is not a public API route.
func (s *Service) ImportPage(ctx context.Context, tenant, sourceID, endpoint, expectedScope string) (int, error) {
	if !identifier.MatchString(sourceID) || !identifier.MatchString(tenant) || expectedScope == "" {
		return 0, fmt.Errorf("invalid import identity or expected source scope")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return 0, fmt.Errorf("invalid indexer URL")
	}
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
	parsed.Path = "/v1/settlements"
	q := url.Values{"cursor": []string{cursor}, "limit": []string{"100"}}
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
		Data  []IndexerSettlement `json:"data"`
		Next  string              `json:"next_cursor"`
		Scope string              `json:"scope"`
	}
	if json.Unmarshal(raw, &page) != nil || page.Scope != expectedScope {
		return 0, fmt.Errorf("indexer response shape or scope mismatch")
	}
	if len(page.Data) == 0 {
		return 0, nil
	}
	next, err := strconv.ParseInt(page.Next, 10, 64)
	prior, e := strconv.ParseInt(cursor, 10, 64)
	if err != nil || e != nil || next <= prior || page.Data[len(page.Data)-1].ID != next {
		return 0, fmt.Errorf("indexer cursor failed to advance")
	}
	lastID := prior
	movements := []Settlement{}
	for _, v := range page.Data {
		if v.ID <= lastID || v.BlockNumber > v.FinalizedNumber || v.FinalizedHash == "" || v.BlockHash == "" || v.BlockTime <= 0 || v.LogIndex < 0 {
			return 0, fmt.Errorf("invalid indexer finality evidence")
		}
		lastID = v.ID
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
	}
	// Retrieval time is retained on first import; the source's finality hash is the actual evidence.
	payload, _ := json.Marshal(map[string]any{"source": sourceID, "cursor": cursor, "scope": page.Scope, "next": page.Next, "data": page.Data})
	_, _, err = s.Store.Mutate(ctx, tenant, "indexer-importer", "import-project1", sourceID+"-"+cursor, payload, func(tx pgx.Tx) (any, error) {
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
