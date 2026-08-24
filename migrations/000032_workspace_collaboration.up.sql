-- 000032_workspace_collaboration.up.sql
-- SPEC-FTR-01 §6.1 — workspace collaboration core (Phase P1): workspace
-- ownership columns, workspace_members, and invitations.
--
-- IDENTITY DEVIATION (documented per worker brief): SPEC-FTR-01 §6.1
-- references profiles(id), but the authenticated identity in this
-- codebase is the users table (JWT sub = user UUID; tree_members.user_id;
-- multi-user integration tests use db.User). owner_id and
-- workspace_members.user_id therefore reference users(id), NOT
-- profiles(id). This matches the existing auth model.

-- ALTER existing workspaces table (it exists from 000018, which already
-- provides id, name, slug, description, created_at, updated_at):
ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS owner_id UUID REFERENCES users(id) ON DELETE RESTRICT;
ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS tree_id UUID REFERENCES trees(id) ON DELETE CASCADE;  -- nullable: workspace may exist before tree binding
ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS approval_ttl BIGINT NOT NULL DEFAULT 300;            -- seconds, default 5m per SPEC-FTR-01 §2 decision 12
-- NOTE: updated_at already exists from 000018 — do NOT add it twice.

-- workspace_members per spec §6.1 (identity = users):
CREATE TABLE IF NOT EXISTS workspace_members (
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role         INT NOT NULL DEFAULT 1, -- 0=viewer, 1=editor, 2=admin
    joined_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, user_id)
);

-- Membership lookups by user (GetUserWorkspaces) scan the PK backwards;
-- a small index keeps list queries cheap.
CREATE INDEX IF NOT EXISTS idx_workspace_members_user ON workspace_members(user_id);

-- invitations per spec §6.1 (created_by = users):
CREATE TABLE IF NOT EXISTS invitations (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    created_by   UUID NOT NULL REFERENCES users(id),
    token_hash   TEXT NOT NULL UNIQUE,
    expires_at   TIMESTAMPTZ NOT NULL,
    used         BOOLEAN NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
