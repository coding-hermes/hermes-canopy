-- Restore the defaults used by the original registry migrations. Registry
-- data and tables are intentionally preserved when rolling back this
-- compatibility migration.
ALTER TABLE plugin_registry ALTER COLUMN id SET DEFAULT uuidv7();
ALTER TABLE plugin_instances ALTER COLUMN id SET DEFAULT uuidv7();
ALTER TABLE plugin_audit_log ALTER COLUMN id SET DEFAULT uuidv7();

