-- 000031_tree_events_yjs_update.up.sql
-- WIRE-001 (sync engine): engine.go AppendEvent writes event_type 'yjs_update'
-- for every pushed Yjs update, but chk_event_type (000007) only allows the
-- five node/edge mutation types — every POST /trees/{id}/sync push failed
-- with a CHECK violation (500 INTERNAL_ERROR, silent log). Add the sync
-- event type to the constraint.

ALTER TABLE tree_events DROP CONSTRAINT chk_event_type;
ALTER TABLE tree_events ADD CONSTRAINT chk_event_type CHECK (event_type IN (
    'node_added', 'node_updated', 'node_removed',
    'edge_added', 'edge_removed',
    'yjs_update'
));
