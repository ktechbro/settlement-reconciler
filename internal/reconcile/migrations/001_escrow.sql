CREATE UNIQUE INDEX escrow_deal_identity ON records(tenant,(body->'asset'->>'network'),(body->>'program'),(body->>'escrow')) WHERE kind='escrow-deals';
CREATE UNIQUE INDEX escrow_event_identity ON records(tenant,(body->>'source_event_key')) WHERE kind='escrow-events';
CREATE UNIQUE INDEX escrow_event_movement ON records(tenant,(body->>'settlement_id')) WHERE kind='escrow-events';
