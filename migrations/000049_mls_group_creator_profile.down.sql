-- 000049_mls_group_creator_profile.down.sql
ALTER TABLE mls_groups
    DROP COLUMN IF EXISTS creator_profile_id;
