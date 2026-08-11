-- 000031_tree_events_yjs_update.down.sql

ALTER TABLE tree_events DROP CONSTRAINT chk_event_type;
ALTER TABLE tree_events ADD CONSTRAINT chk_event_type CHECK (event_type IN (
    'node_added', 'node_updated', 'node_removed',
    'edge_added', 'edge_removed'
));
