-- 000008_users_profiles.down.sql
-- Drop users, profiles, tree_members, and profile_invites tables.

DROP TRIGGER IF EXISTS set_profiles_updated_at ON profiles;
DROP TRIGGER IF EXISTS set_users_updated_at ON users;
DROP FUNCTION IF EXISTS trigger_set_updated_at();

DROP TABLE IF EXISTS profile_invites CASCADE;
DROP TABLE IF EXISTS tree_members CASCADE;
DROP TABLE IF EXISTS profiles CASCADE;
DROP TABLE IF EXISTS users CASCADE;

DROP TYPE IF EXISTS invite_status;
DROP TYPE IF EXISTS tree_role;
DROP TYPE IF EXISTS profile_type;
