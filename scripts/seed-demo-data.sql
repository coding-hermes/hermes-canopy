-- ⚠️ E2E-ONLY TEST FIXTURE (GAP-051, 2026-08-27).
-- This seed exists SOLELY for the E2E battery. Never run it in product
-- use, and remove the fixture after the battery completes:
--   psql ... -f scripts/remove-demo-data.sql
-- Product surfaces show REAL Hermes data (gateway runs, imported
-- sessions, user-created trees); the demo tree must never appear in
-- normal use. The sidebar 'Tree View' nav no longer points at it.
--
-- Deterministic demo-data seed for the Hermes Canopy E2E battery.
-- Created 2026-08-26 (tick 416) after the live canopy DB was found wiped
-- (all tables 0 rows between ticks 410 and 416; the original UI-02 Rail Demo
-- tree b1655761-2d7f-4b3c-85d5-21396da15691 was unrecoverable — the only
-- pg_dump, backups/canopy-20260801-112134.dump, predates it).
--
-- Requirements this seed satisfies (canopy-e2e-testing skill):
--   * dev JWT user must exist or every POST 503s (failure mode #28)
--   * /tree/demo alias + visual-regression goldens need the stable demo tree
--     UUID b1655761-2d7f-4b3c-85d5-21396da15691 titled 'UI-02 Rail Demo'
--     (activeTree.ts DEMO_TREE_UUID, VREG-001 label preference)
--   * tree-rendering/accessibility need a tree with nodes (failure mode #10)
-- Idempotent: safe to re-run on any state (ON CONFLICT DO NOTHING + guards).

-- 1. Dev JWT user (id the E2E dev JWT resolves to).
INSERT INTO users (id, hermes_user_id, email, display_name, is_active)
VALUES ('00000000-0000-0000-0000-000000000001',
        '00000000-0000-0000-0000-000000000001',
        'dev@canopy.dev', 'Dev User', true)
ON CONFLICT (id) DO NOTHING;

-- 2. Demo tree with the canonical stable UUID.
INSERT INTO trees (id, owner_id, title, description)
VALUES ('b1655761-2d7f-4b3c-85d5-21396da15691',
        '00000000-0000-0000-0000-000000000001',
        'UI-02 Rail Demo',
        'Seeded demo tree for E2E tests (deterministic reseed, tick 416)')
ON CONFLICT (id) DO NOTHING;

-- 3. Nodes: root + 9 children (3 branches + 1 synthesis), fixed UUIDs.
INSERT INTO nodes (id, tree_id, parent_id, author_id, content, content_format, node_type, sequence_num, content_hash) VALUES
('11111111-1111-4111-8111-111111111101', 'b1655761-2d7f-4b3c-85d5-21396da15691', NULL, '00000000-0000-0000-0000-000000000001', 'Welcome to the UI-02 Rail Demo tree. Every message here is a node in a conversation DAG.', 'markdown', 'message', 1, encode(sha256('Welcome to the UI-02 Rail Demo tree. Every message here is a node in a conversation DAG.'::bytea),'hex')),
('11111111-1111-4111-8111-111111111102', 'b1655761-2d7f-4b3c-85d5-21396da15691', '11111111-1111-4111-8111-111111111101', '00000000-0000-0000-0000-000000000001', 'Graph Overview mode shows the full conversation DAG with fork and synthesis edges.', 'markdown', 'message', 2, encode(sha256('Graph Overview mode shows the full conversation DAG with fork and synthesis edges.'::bytea),'hex')),
('11111111-1111-4111-8111-111111111103', 'b1655761-2d7f-4b3c-85d5-21396da15691', '11111111-1111-4111-8111-111111111101', '00000000-0000-0000-0000-000000000001', 'Thread Focus collapses side branches so you can read one line of reasoning.', 'markdown', 'message', 3, encode(sha256('Thread Focus collapses side branches so you can read one line of reasoning.'::bytea),'hex')),
('11111111-1111-4111-8111-111111111104', 'b1655761-2d7f-4b3c-85d5-21396da15691', '11111111-1111-4111-8111-111111111101', '00000000-0000-0000-0000-000000000001', 'Synthesis View merges multiple sources into a single multi-parent node.', 'markdown', 'message', 4, encode(sha256('Synthesis View merges multiple sources into a single multi-parent node.'::bytea),'hex')),
('11111111-1111-4111-8111-111111111105', 'b1655761-2d7f-4b3c-85d5-21396da15691', '11111111-1111-4111-8111-111111111101', '00000000-0000-0000-0000-000000000001', 'The Context Compiler assembles a budgeted, auditable manifest for every model call.', 'markdown', 'message', 5, encode(sha256('The Context Compiler assembles a budgeted, auditable manifest for every model call.'::bytea),'hex')),
('11111111-1111-4111-8111-111111111106', 'b1655761-2d7f-4b3c-85d5-21396da15691', '11111111-1111-4111-8111-111111111105', '00000000-0000-0000-0000-000000000001', 'The manifest shows exactly what was sent: ancestors, references, and token budget.', 'markdown', 'message', 6, encode(sha256('The manifest shows exactly what was sent: ancestors, references, and token budget.'::bytea),'hex')),
('11111111-1111-4111-8111-111111111107', 'b1655761-2d7f-4b3c-85d5-21396da15691', '11111111-1111-4111-8111-111111111105', '00000000-0000-0000-0000-000000000001', 'Budget rules resolve #references per tree and node type.', 'markdown', 'message', 7, encode(sha256('Budget rules resolve #references per tree and node type.'::bytea),'hex')),
('11111111-1111-4111-8111-111111111108', 'b1655761-2d7f-4b3c-85d5-21396da15691', '11111111-1111-4111-8111-111111111101', '00000000-0000-0000-0000-000000000001', 'Topics are named, searchable subgraphs with #references.', 'markdown', 'message', 8, encode(sha256('Topics are named, searchable subgraphs with #references.'::bytea),'hex')),
('11111111-1111-4111-8111-111111111109', 'b1655761-2d7f-4b3c-85d5-21396da15691', '11111111-1111-4111-8111-111111111108', '00000000-0000-0000-0000-000000000001', 'Reference resolution is cached and auditable per node.', 'markdown', 'message', 9, encode(sha256('Reference resolution is cached and auditable per node.'::bytea),'hex')),
('11111111-1111-4111-8111-11111111110a', 'b1655761-2d7f-4b3c-85d5-21396da15691', '11111111-1111-4111-8111-111111111101', '00000000-0000-0000-0000-000000000001', 'This synthesis node merges the Graph, Context, and Topics branches into one summary.', 'markdown', 'synthesis', 10, encode(sha256('This synthesis node merges the Graph, Context, and Topics branches into one summary.'::bytea),'hex'))
ON CONFLICT (id) DO NOTHING;

-- 4. Edges: root -> each child (reply edges).
INSERT INTO edges (id, tree_id, source_id, target_id, edge_type, sequence_num) VALUES
('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1', 'b1655761-2d7f-4b3c-85d5-21396da15691', '11111111-1111-4111-8111-111111111101', '11111111-1111-4111-8111-111111111102', 'reply', 1),
('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa2', 'b1655761-2d7f-4b3c-85d5-21396da15691', '11111111-1111-4111-8111-111111111101', '11111111-1111-4111-8111-111111111103', 'reply', 2),
('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa3', 'b1655761-2d7f-4b3c-85d5-21396da15691', '11111111-1111-4111-8111-111111111101', '11111111-1111-4111-8111-111111111104', 'reply', 3),
('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa4', 'b1655761-2d7f-4b3c-85d5-21396da15691', '11111111-1111-4111-8111-111111111101', '11111111-1111-4111-8111-111111111105', 'reply', 4),
('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa5', 'b1655761-2d7f-4b3c-85d5-21396da15691', '11111111-1111-4111-8111-111111111105', '11111111-1111-4111-8111-111111111106', 'reply', 5),
('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa6', 'b1655761-2d7f-4b3c-85d5-21396da15691', '11111111-1111-4111-8111-111111111105', '11111111-1111-4111-8111-111111111107', 'reply', 6),
('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa7', 'b1655761-2d7f-4b3c-85d5-21396da15691', '11111111-1111-4111-8111-111111111101', '11111111-1111-4111-8111-111111111108', 'reply', 7),
('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa8', 'b1655761-2d7f-4b3c-85d5-21396da15691', '11111111-1111-4111-8111-111111111108', '11111111-1111-4111-8111-111111111109', 'reply', 8),
('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa9', 'b1655761-2d7f-4b3c-85d5-21396da15691', '11111111-1111-4111-8111-111111111101', '11111111-1111-4111-8111-11111111110a', 'synthesis', 9)
ON CONFLICT (id) DO NOTHING;

-- 5. Topics (3, deterministic slugs).
INSERT INTO topics (id, tree_id, root_node_id, title, description, slug, status, node_count, ref_count) VALUES
('bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbb01', 'b1655761-2d7f-4b3c-85d5-21396da15691', '11111111-1111-4111-8111-111111111101', 'Graph Navigation', 'View modes and DAG navigation.', 'graph-navigation', 'active', 4, 0),
('bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbb02', 'b1655761-2d7f-4b3c-85d5-21396da15691', '11111111-1111-4111-8111-111111111105', 'Context Compiler', 'Context manifests and budget rules.', 'context-compiler', 'active', 3, 0),
('bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbb03', 'b1655761-2d7f-4b3c-85d5-21396da15691', '11111111-1111-4111-8111-111111111108', 'Topics & References', 'Named subgraphs with #references.', 'topics-references', 'active', 2, 0)
ON CONFLICT (id) DO NOTHING;

-- 6. Tree membership: dev user owns the demo tree.
INSERT INTO tree_members (tree_id, user_id, role)
VALUES ('b1655761-2d7f-4b3c-85d5-21396da15691', '00000000-0000-0000-0000-000000000001', 'owner')
ON CONFLICT (tree_id, user_id) DO NOTHING;

-- 7. Point the tree at its root node (only when the tree was just created).
UPDATE trees SET root_node_id = '11111111-1111-4111-8111-111111111101'
WHERE id = 'b1655761-2d7f-4b3c-85d5-21396da15691' AND root_node_id IS NULL;
