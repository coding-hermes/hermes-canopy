-- 000008_users_profiles.down.sql — SQLite translation of ../../000008_users_profiles.down.sql
--
-- PG drops the two triggers, the shared PL/pgSQL function (no SQLite equivalent — the
-- function name is retired with the wave-2 Go layer) and finally the three enums
-- (types) after the tables. SQLite types ARE the table CHECK constraints, so they
-- disappear with their tables; DROP TRIGGER takes no ON <table> clause here.

DROP TRIGGER IF EXISTS set_profiles_updated_at;
DROP TRIGGER IF EXISTS set_users_updated_at;

DROP TABLE IF EXISTS profile_invites;
DROP TABLE IF EXISTS tree_members;
DROP TABLE IF EXISTS profiles;
DROP TABLE IF EXISTS users;
