-- Remove the E2E demo-data fixture from the live canopy DB.
-- GAP-051 (2026-08-27): the 'UI-02 Rail Demo' tree is an E2E-ONLY test
-- fixture. It must exist ONLY while the E2E battery runs (window-opening
-- ticks). Product data is real Hermes data (gateway runs, imported
-- sessions, user-created trees) — seeded content must never be visible in
-- normal use.
--
-- E2E window procedure (fixture lifecycle):
--   1. BEFORE battery:  psql ... -f scripts/seed-demo-data.sql
--   2. run the battery
--   3. AFTER battery:   psql ... -f scripts/remove-demo-data.sql
--
-- This is a HARD delete (not soft): the idempotent seed script re-inserts
-- with ON CONFLICT (id) DO NOTHING, which would NOT resurrect a
-- soft-deleted row. The dev JWT user (00000000-...-0001) is intentionally
-- kept — every write path depends on it (canopy-e2e-testing failure mode
-- #28/#30) and it is not demo content.
--
-- Idempotent: safe to re-run on any state (deletes match zero rows when
-- the fixture is absent).

BEGIN;

-- All FK tables referencing trees.id (schema-derived 2026-08-27).
DELETE FROM reference_resolution_cache WHERE tree_id = 'b1655761-2d7f-4b3c-85d5-21396da15691';
DELETE FROM topic_proposals            WHERE tree_id = 'b1655761-2d7f-4b3c-85d5-21396da15691';
DELETE FROM topic_detection_config     WHERE tree_id = 'b1655761-2d7f-4b3c-85d5-21396da15691';
DELETE FROM subject_cooldowns          WHERE tree_id = 'b1655761-2d7f-4b3c-85d5-21396da15691';
DELETE FROM tree_snapshots             WHERE tree_id = 'b1655761-2d7f-4b3c-85d5-21396da15691';
DELETE FROM tree_events                WHERE tree_id = 'b1655761-2d7f-4b3c-85d5-21396da15691';
DELETE FROM tree_event_seq             WHERE tree_id = 'b1655761-2d7f-4b3c-85d5-21396da15691';
DELETE FROM profile_invites            WHERE tree_id = 'b1655761-2d7f-4b3c-85d5-21396da15691';
DELETE FROM approvals                  WHERE tree_id = 'b1655761-2d7f-4b3c-85d5-21396da15691';
DELETE FROM approval_rules             WHERE tree_id = 'b1655761-2d7f-4b3c-85d5-21396da15691';
DELETE FROM workspaces                 WHERE tree_id = 'b1655761-2d7f-4b3c-85d5-21396da15691';
DELETE FROM topics                     WHERE tree_id = 'b1655761-2d7f-4b3c-85d5-21396da15691';
DELETE FROM edges                      WHERE tree_id = 'b1655761-2d7f-4b3c-85d5-21396da15691';
DELETE FROM tree_members               WHERE tree_id = 'b1655761-2d7f-4b3c-85d5-21396da15691';
DELETE FROM nodes                      WHERE tree_id = 'b1655761-2d7f-4b3c-85d5-21396da15691';

-- The fixture tree itself.
DELETE FROM trees WHERE id = 'b1655761-2d7f-4b3c-85d5-21396da15691';

COMMIT;
