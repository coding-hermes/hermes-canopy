-- 000018_workspaces.up.sql
-- Workspaces root table (SPEC-FTR-07 §6.1).
-- Created retroactively: migrated 000010 and 000014 already FK to this table.

CREATE TABLE IF NOT EXISTS workspaces (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(128) NOT NULL,
    slug        VARCHAR(64) NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- FK from profile_route (000010) — deferred here because the
-- profile_route table was created before workspaces existed.
ALTER TABLE profile_route ADD CONSTRAINT fk_profile_route_workspace
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE;

-- FK from mls_groups (000014) — same ordering fix.
ALTER TABLE mls_groups ADD CONSTRAINT fk_mls_groups_workspace
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE;
