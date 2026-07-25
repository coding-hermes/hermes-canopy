-- 000019_canopy_role.up.sql
-- Create canopy_app role for granular perms (used by 000009 REVOKE).

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'canopy_app') THEN
        CREATE ROLE canopy_app;
    END IF;
END
$$;

-- REVOKE UPDATE/DELETE from canopy_app on audit log (moved from 000009)
-- This must happen after the role exists, hence here in 000019.
REVOKE UPDATE, DELETE ON approval_audit_log FROM canopy_app;
