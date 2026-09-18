-- 000008_users_profiles.up.sql — SQLite translation of ../../000008_users_profiles.up.sql
--
-- Translation rules applied beyond the standard set (docs/SQLITE-PIVOT.md):
--   * PG enums -> TEXT + CHECK (col IN (..)), same value lists:
--       profile_type    (../../000008_users_profiles.up.sql:11-14)
--       tree_role       (:21-26)
--       invite_status   (:33-38)
--   * boolean -> INTEGER NOT NULL DEFAULT 0/1 + CHECK (col IN (0,1)); PG's `true`/`false`
--     literals also work in SQLite, but the stored domain 0/1 is what the Go layer reads.
--   * `now() + INTERVAL '7 days'` -> strftime('%Y-%m-%dT%H:%M:%fZ','now','+7 days'),
--     which keeps the same RFC3339-UTC lexicographic ordering as every other timestamp.
--   * uuid columns -> TEXT and carry no default (PG DEFAULT uuidv7() at :47,80,124,167) —
--     wave-2 Go write-path obligation.
--
-- WEAKENED TRANSLATION (recorded, not silently changed):
--   * chk_users_email — ../../000008_users_profiles.up.sql:68 uses the case-insensitive
--     POSIX regex operator `~*`, which SQLite does not implement (there is no REGEXP
--     function unless the embedding program registers one). The CHECK below is an
--     approximate GLOB: at least one character before '@', at least one after it, a dot,
--     then two or more ASCII letters. Wave-2 owner: validation moves to the Go write
--     path (or a registered REGEXP function is provided at connection setup). Open
--     decision "json storage + validation / regex validation" in docs/SQLITE-PIVOT.md.

CREATE TABLE IF NOT EXISTS users (
    id              TEXT    PRIMARY KEY NOT NULL,
    hermes_user_id  TEXT    NOT NULL UNIQUE,
    email           TEXT,
    display_name    TEXT    NOT NULL,
    avatar_url      TEXT,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_seen_at    TEXT,
    is_active       INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0,1)),
    deleted_at      TEXT,
    CONSTRAINT chk_users_display_name CHECK (length(display_name) >= 1 AND length(display_name) <= 100),
    CONSTRAINT chk_users_email CHECK (email IS NULL OR email GLOB '?*@?*.[A-Za-z][A-Za-z]*')
);

CREATE INDEX IF NOT EXISTS idx_users_hermes_user_id ON users(hermes_user_id);
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email) WHERE email IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_users_created_at ON users(created_at);

CREATE TABLE IF NOT EXISTS profiles (
    id              TEXT    PRIMARY KEY NOT NULL,
    owner_id        TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_type    TEXT    NOT NULL DEFAULT 'hermes-profile'
                            CHECK (profile_type IN ('human', 'hermes-profile')),
    name            TEXT    NOT NULL,
    display_name    TEXT    NOT NULL,
    description     TEXT,
    config_json     TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(config_json)),
    can_auto_respond INTEGER NOT NULL DEFAULT 0 CHECK (can_auto_respond IN (0,1)),
    context_window_size INTEGER NOT NULL DEFAULT 32768,
    is_public       INTEGER NOT NULL DEFAULT 0 CHECK (is_public IN (0,1)),
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    deleted_at      TEXT,
    CONSTRAINT chk_profiles_name CHECK (length(name) >= 1 AND length(name) <= 64),
    CONSTRAINT chk_profiles_display_name CHECK (length(display_name) >= 1 AND length(display_name) <= 200),
    CONSTRAINT chk_profiles_context_window CHECK (context_window_size >= 1024 AND context_window_size <= 2097152),
    CONSTRAINT uq_profiles_owner_name UNIQUE (owner_id, name)
);

CREATE INDEX IF NOT EXISTS idx_profiles_owner_id ON profiles(owner_id);
CREATE INDEX IF NOT EXISTS idx_profiles_name ON profiles(name);
CREATE INDEX IF NOT EXISTS idx_profiles_type ON profiles(profile_type);

CREATE TABLE IF NOT EXISTS tree_members (
    id              TEXT    PRIMARY KEY NOT NULL,
    tree_id         TEXT    NOT NULL REFERENCES trees(id) ON DELETE CASCADE,
    user_id         TEXT    REFERENCES users(id) ON DELETE CASCADE,
    profile_id      TEXT    REFERENCES profiles(id) ON DELETE CASCADE,
    role            TEXT    NOT NULL DEFAULT 'member'
                            CHECK (role IN ('owner', 'admin', 'member', 'viewer')),
    is_visible      INTEGER NOT NULL DEFAULT 1 CHECK (is_visible IN (0,1)),
    auto_approved   INTEGER NOT NULL DEFAULT 0 CHECK (auto_approved IN (0,1)),
    joined_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    invited_by      TEXT    REFERENCES users(id),
    -- REORDERED vs PG: SQLite requires every column definition to precede every table
    -- constraint (a column after `CONSTRAINT .. CHECK (..)` is a syntax error there),
    -- while PG accepts them interleaved. chk_tree_members_participant therefore moves
    -- from the middle of the PG column list (../../000008_users_profiles.up.sql:133)
    -- to the constraint block at the end.
    CONSTRAINT chk_tree_members_participant CHECK (
        (user_id IS NOT NULL AND profile_id IS NULL) OR
        (user_id IS NULL AND profile_id IS NOT NULL)
    ),
    CONSTRAINT uq_tree_members_tree_user UNIQUE (tree_id, user_id),
    CONSTRAINT uq_tree_members_tree_profile UNIQUE (tree_id, profile_id)
);

CREATE INDEX IF NOT EXISTS idx_tree_members_tree_id ON tree_members(tree_id);
CREATE INDEX IF NOT EXISTS idx_tree_members_user_id ON tree_members(user_id) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tree_members_profile_id ON tree_members(profile_id) WHERE profile_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tree_members_role ON tree_members(tree_id, role);

CREATE TABLE IF NOT EXISTS profile_invites (
    id              TEXT    PRIMARY KEY NOT NULL,
    tree_id         TEXT    NOT NULL REFERENCES trees(id) ON DELETE CASCADE,
    profile_id      TEXT    NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    invited_by      TEXT    NOT NULL REFERENCES users(id),
    invite_token    TEXT    NOT NULL UNIQUE,
    status          TEXT    NOT NULL DEFAULT 'pending'
                            CHECK (status IN ('pending', 'accepted', 'declined', 'expired')),
    proposed_role   TEXT    NOT NULL DEFAULT 'member'
                            CHECK (proposed_role IN ('owner', 'admin', 'member', 'viewer')),
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    expires_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now','+7 days')),
    accepted_at     TEXT,
    declined_at     TEXT,
    CONSTRAINT chk_profile_invites_token CHECK (length(invite_token) >= 32)
);

CREATE INDEX IF NOT EXISTS idx_profile_invites_tree_id ON profile_invites(tree_id);
CREATE INDEX IF NOT EXISTS idx_profile_invites_token ON profile_invites(invite_token);
CREATE INDEX IF NOT EXISTS idx_profile_invites_status ON profile_invites(status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_profile_invites_active ON profile_invites(tree_id, profile_id)
    WHERE status = 'pending';

-- ── Triggers ─────────────────────────────────────────────────────────────────────
-- PG defines trigger_set_updated_at() once (:208-214) and attaches it to users (:217)
-- and profiles (:221). SQLite has no user-defined functions, so the (one-statement)
-- body is inlined into each of the two triggers, which keep their PG names.
CREATE TRIGGER set_users_updated_at
    AFTER UPDATE ON users
    FOR EACH ROW
BEGIN
    UPDATE users SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = NEW.id;
END;

CREATE TRIGGER set_profiles_updated_at
    AFTER UPDATE ON profiles
    FOR EACH ROW
BEGIN
    UPDATE profiles SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = NEW.id;
END;
