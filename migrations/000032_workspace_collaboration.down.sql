-- 000032_workspace_collaboration.down.sql

DROP TABLE IF EXISTS invitations;
DROP TABLE IF EXISTS workspace_members;
ALTER TABLE workspaces DROP COLUMN IF EXISTS owner_id, DROP COLUMN IF EXISTS tree_id, DROP COLUMN IF EXISTS approval_ttl;
