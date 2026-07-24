-- 000010_profile_route.up.sql
-- Canopy workspace to Hermes profile mappings (SPEC-FTR-07 §6.1).

CREATE TABLE IF NOT EXISTS profile_route (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id             UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    profile_name             VARCHAR(64) NOT NULL,
    display_name             VARCHAR(128) NOT NULL DEFAULT '',
    is_active                BOOLEAN NOT NULL DEFAULT false,
    model_preference         VARCHAR(64),
    profile_token_encrypted  BYTEA,
    mapped_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(workspace_id, profile_name)
);

CREATE INDEX IF NOT EXISTS idx_profile_route_active
    ON profile_route(workspace_id) WHERE is_active = true;
