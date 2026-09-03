ALTER TABLE relay_sessions DROP CONSTRAINT relay_sessions_tenant_id_fkey;
ALTER TABLE relay_instances DROP CONSTRAINT relay_instances_tenant_id_fkey;
DROP TABLE tenants;
