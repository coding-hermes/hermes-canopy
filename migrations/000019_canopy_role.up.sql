-- 000019_canopy_role.up.sql
-- Create canopy_app role for granular perms (used by 000009 REVOKE).

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'canopy_app') THEN
        CREATE ROLE canopy_app;
    END IF;
END
$$;
