-- 000008_users_profiles.up.sql
-- Users, profiles, tree memberships, and profile invites.
-- SPEC-DM-04 §3.2–3.5. Runs after 000007_tree_events.

-- ============================================================
-- Enums
-- ============================================================

-- Profile type enum
DO $$ BEGIN
    CREATE TYPE profile_type AS ENUM (
        'human',
        'hermes-profile'
    );
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

-- Tree member role enum
DO $$ BEGIN
    CREATE TYPE tree_role AS ENUM (
        'owner',
        'admin',
        'member',
        'viewer'
    );
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

-- Invite status enum
DO $$ BEGIN
    CREATE TYPE invite_status AS ENUM (
        'pending',
        'accepted',
        'declined',
        'expired'
    );
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

-- ============================================================
-- users: Human accounts (Canopy users)
-- ============================================================
CREATE TABLE IF NOT EXISTS users (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),

    -- Identity (mirrors Hermes auth)
    hermes_user_id  text NOT NULL UNIQUE,                       -- Hermes auth provider user ID (sub claim)
    email           text,                                       -- Optional, for notifications/invites

    -- Display
    display_name    text NOT NULL,                              -- "Bane", "Kara"
    avatar_url      text,                                       -- Optional avatar URL

    -- Timestamps
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    last_seen_at    timestamptz,                                -- Last activity timestamp

    -- Status
    is_active       boolean NOT NULL DEFAULT true,              -- Soft-disable account
    deleted_at      timestamptz,                                -- Soft-delete

    -- Constraints
    CONSTRAINT chk_users_display_name CHECK (char_length(display_name) >= 1 AND char_length(display_name) <= 100),
    CONSTRAINT chk_users_email CHECK (email IS NULL OR email ~* '^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$')
);

-- Indexes
CREATE INDEX IF NOT EXISTS idx_users_hermes_user_id ON users(hermes_user_id);
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email) WHERE email IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_users_created_at ON users(created_at);

-- ============================================================
-- profiles: Hermes agent profiles (owned by a user)
-- ============================================================
CREATE TABLE IF NOT EXISTS profiles (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),

    -- Ownership
    owner_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- Identity
    profile_type    profile_type NOT NULL DEFAULT 'hermes-profile',
    name            text NOT NULL,                              -- "coding-hermes", "creative-hermes"
    display_name    text NOT NULL,                              -- "Coding Hermes", "Creative Hermes"
    description     text,                                       -- "Autonomous coding agent for the fleet"

    -- Configuration (profile-specific settings)
    config_json     jsonb NOT NULL DEFAULT '{}',                -- Provider, model, skills, system prompt, etc.

    -- Capabilities
    can_auto_respond boolean NOT NULL DEFAULT false,            -- Auto-reply to @mentions without human approval
    context_window_size integer NOT NULL DEFAULT 32768,         -- Token budget for this profile

    -- Visibility
    is_public       boolean NOT NULL DEFAULT false,             -- Discoverable by other users

    -- Timestamps
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    deleted_at      timestamptz,                                -- Soft-delete

    -- Constraints
    CONSTRAINT chk_profiles_name CHECK (char_length(name) >= 1 AND char_length(name) <= 64),
    CONSTRAINT chk_profiles_display_name CHECK (char_length(display_name) >= 1 AND char_length(display_name) <= 200),
    CONSTRAINT chk_profiles_context_window CHECK (context_window_size >= 1024 AND context_window_size <= 2097152),

    -- One profile name per owner (no duplicates)
    CONSTRAINT uq_profiles_owner_name UNIQUE (owner_id, name)
);

-- Indexes
CREATE INDEX IF NOT EXISTS idx_profiles_owner_id ON profiles(owner_id);
CREATE INDEX IF NOT EXISTS idx_profiles_name ON profiles(name);
CREATE INDEX IF NOT EXISTS idx_profiles_type ON profiles(profile_type);

-- ============================================================
-- tree_members: Which users/profiles have access to which trees
-- ============================================================
CREATE TABLE IF NOT EXISTS tree_members (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),

    tree_id         uuid NOT NULL REFERENCES trees(id) ON DELETE CASCADE,

    -- Polymorphic: either a user or a profile
    user_id         uuid REFERENCES users(id) ON DELETE CASCADE,
    profile_id      uuid REFERENCES profiles(id) ON DELETE CASCADE,

    -- Exactly one of user_id or profile_id must be set
    CONSTRAINT chk_tree_members_participant CHECK (
        (user_id IS NOT NULL AND profile_id IS NULL) OR
        (user_id IS NULL AND profile_id IS NOT NULL)
    ),

    -- Role
    role            tree_role NOT NULL DEFAULT 'member',

    -- Visibility (profiles only — can this profile be seen in this tree?)
    is_visible      boolean NOT NULL DEFAULT true,             -- Per-tree profile visibility toggle

    -- Auto-approval (pre-approved input from this member)
    auto_approved   boolean NOT NULL DEFAULT false,            -- Owner trusts this member

    -- Timestamps
    joined_at       timestamptz NOT NULL DEFAULT now(),
    invited_by      uuid REFERENCES users(id),                 -- Which user sent the invite

    -- Constraints
    -- One membership per participant per tree
    CONSTRAINT uq_tree_members_tree_user UNIQUE (tree_id, user_id),
    CONSTRAINT uq_tree_members_tree_profile UNIQUE (tree_id, profile_id)
);

-- Indexes
CREATE INDEX IF NOT EXISTS idx_tree_members_tree_id ON tree_members(tree_id);
CREATE INDEX IF NOT EXISTS idx_tree_members_user_id ON tree_members(user_id) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tree_members_profile_id ON tree_members(profile_id) WHERE profile_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tree_members_role ON tree_members(tree_id, role);

-- ============================================================
-- profile_invites: Invite a Hermes profile into a tree
-- ============================================================
CREATE TABLE IF NOT EXISTS profile_invites (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),

    tree_id         uuid NOT NULL REFERENCES trees(id) ON DELETE CASCADE,

    -- Who is being invited
    profile_id      uuid NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,

    -- Who invited
    invited_by      uuid NOT NULL REFERENCES users(id),

    -- Token (for accepting via link)
    invite_token    text NOT NULL UNIQUE,                      -- Cryptographically random, URL-safe

    -- Status
    status          invite_status NOT NULL DEFAULT 'pending',

    -- Proposed role
    proposed_role   tree_role NOT NULL DEFAULT 'member',

    -- Timestamps
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL DEFAULT (now() + INTERVAL '7 days'),
    accepted_at     timestamptz,
    declined_at     timestamptz,

    -- Constraints
    CONSTRAINT chk_profile_invites_token CHECK (char_length(invite_token) >= 32)
);

-- Indexes
CREATE INDEX IF NOT EXISTS idx_profile_invites_tree_id ON profile_invites(tree_id);
CREATE INDEX IF NOT EXISTS idx_profile_invites_token ON profile_invites(invite_token);
CREATE INDEX IF NOT EXISTS idx_profile_invites_status ON profile_invites(status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_profile_invites_active ON profile_invites(tree_id, profile_id)
    WHERE status = 'pending';

-- ============================================================
-- Triggers
-- ============================================================

-- updated_at trigger (shared pattern from SPEC-DM-01)
CREATE OR REPLACE FUNCTION trigger_set_updated_at()
RETURNS trigger AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Apply to tables with updated_at
CREATE TRIGGER set_users_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION trigger_set_updated_at();

CREATE TRIGGER set_profiles_updated_at
    BEFORE UPDATE ON profiles
    FOR EACH ROW EXECUTE FUNCTION trigger_set_updated_at();
