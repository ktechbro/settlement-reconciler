package reconcile

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

func (s *Store) EscrowTrace(ctx context.Context, tenant, id string) (map[string]any, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	snapshot, err := load(ctx, tx, tenant)
	if err != nil {
		return nil, err
	}
	var deal *EscrowDeal
	for i := range snapshot.Escrows {
		if snapshot.Escrows[i].ID == id {
			deal = &snapshot.Escrows[i]
			break
		}
	}
	if deal == nil {
		return nil, ErrNotFound
	}
	events := []EscrowEvent{}
	movementIDs := map[string]bool{}
	for _, e := range snapshot.EscrowEvents {
		if e.Asset.Network == deal.Asset.Network && e.Program == deal.Program && e.Escrow == deal.Escrow {
			events = append(events, e)
			movementIDs[e.SettlementID] = true
		}
	}
	movements := []Settlement{}
	for _, m := range snapshot.Settlements {
		if movementIDs[m.ID] || m.Asset == deal.Asset && (m.Sender == deal.Escrow || m.Recipient == deal.Escrow) {
			movements = append(movements, m)
		}
	}
	decisions := []Decision{}
	mappingIDs := map[string]bool{}
	for _, d := range snapshot.Decisions {
		if d.ID == deal.FundingDecisionID || d.ID == deal.ReleaseDecisionID {
			decisions = append(decisions, d)
			mappingIDs[d.Claims.MappingID] = true
		}
	}
	mappings := []Mapping{}
	for _, m := range snapshot.Mappings {
		if mappingIDs[m.ID] {
			mappings = append(mappings, m)
		}
	}
	versions, err := history(ctx, tx, tenant, "escrow:"+id)
	if err != nil {
		return nil, err
	}
	resolutions := []json.RawMessage{}
	rows, err := tx.Query(ctx, "SELECT body FROM resolutions WHERE tenant=$1 AND case_key=$2 ORDER BY id", tenant, "escrow:"+id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var body json.RawMessage
		if err = rows.Scan(&body); err != nil {
			rows.Close()
			return nil, err
		}
		resolutions = append(resolutions, body)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return map[string]any{"deal": deal, "events": events, "movements": movements, "decisions": decisions, "mappings": mappings, "case_history": versions, "resolutions": resolutions}, tx.Commit(ctx)
}
