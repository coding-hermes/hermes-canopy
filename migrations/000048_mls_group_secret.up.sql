-- 000048_mls_group_secret.up.sql
-- Interim server-side MLS group-key material. Existing rows are lazily backfilled.
ALTER TABLE mls_groups ADD COLUMN group_secret BYTEA;
