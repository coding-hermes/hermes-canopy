-- 000047_multi_message_reference.up.sql
-- SPEC-PL-06 §3.1 (DDL is authoritative; the spec's filename predates the
-- current migration head, so this lands as 000047).
--
-- A `message` node may reply to 2-20 selected source messages through
-- multiple incoming `reference` edges. `nodes.parent_id` stays the
-- deterministic display anchor (the first canonical source) while the
-- edge set is the authoritative parent set (SPEC-PL-06 §1, §3.2).
--
-- The existing chk_edge_type and UNIQUE (source_id, target_id, edge_type)
-- constraints are deliberately untouched: they already permit N distinct
-- reference rows per target while blocking a duplicate source reference
-- (SPEC-PL-06 §3.1, §2 decision 11).

ALTER TABLE nodes
    ADD COLUMN parent_mode text NOT NULL DEFAULT 'lineage';

ALTER TABLE nodes
    ADD CONSTRAINT chk_nodes_parent_mode
    CHECK (parent_mode IN ('lineage', 'multi_reference'));

-- A multi-reference reply must retain a deterministic tree display anchor.
ALTER TABLE nodes
    ADD CONSTRAINT chk_multi_reference_has_display_parent
    CHECK (parent_mode <> 'multi_reference' OR parent_id IS NOT NULL);

-- Reference-set lookup for invariant validation and provenance reads.
CREATE INDEX idx_edges_active_reference_target
    ON edges (tree_id, target_id, sequence_num, source_id)
    WHERE edge_type = 'reference' AND deleted_at IS NULL;

-- Multi-reference node listing / tree scanning.
CREATE INDEX idx_nodes_multi_reference
    ON nodes (tree_id, sequence_num)
    WHERE parent_mode = 'multi_reference' AND deleted_at IS NULL;
