-- 000049_mls_group_creator_profile.up.sql
-- RVW2-CANOPY-104: record which profile created each MLS group.
--
-- POST /api/v1/workspaces/{workspace_id}/mls/groups seeds only a member row
-- (leaf 0); nothing on the group itself named the creator, so GET
-- /api/v1/workspaces/{workspace_id}/mls/groups could not echo
-- creator_profile_id back and callers could not verify which profile owns
-- the group. Persist it as a group column with an FK to profiles(id) — the
-- same id space MLS create-group already validates (ErrCreatorProfileNotFound).
ALTER TABLE mls_groups
    ADD COLUMN IF NOT EXISTS creator_profile_id uuid REFERENCES profiles(id) ON DELETE CASCADE;
