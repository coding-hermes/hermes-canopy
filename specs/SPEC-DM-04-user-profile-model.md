# SPEC-DM-04 — User & Profile Model

> **Status:** Spec | **Blocks:** Phase 3 (API), Phase 4 (Backend)
> **Prerequisite:** SPEC-DM-01 (nodes/edges/trees DDL), SPEC-DM-03 (approval DDL — references users/profiles via UUID)

---

## 1. Purpose

Define the exact PostgreSQL DDL, Go structs, TypeScript types, permission model, and invite flow for Canopy's users, Hermes profiles, and tree memberships. A worker reading this spec must produce the correct database layer, Go repository, TypeScript types, membership service, and invite system with zero clarifying questions.

Canopy has two participant types: **humans** (user accounts) and **Hermes profiles** (agent personas like coding-hermes, creative-hermes, research-hermes). Both participate in tree conversations with role-based permissions. Profiles are independently configurable agents with their own context windows — they appear as participants alongside humans.

---

## 2. Design Decisions (from ARCHITECTURE.md)

| Decision | Choice | Source |
|----------|--------|--------|
| Participant types | `human` and `hermes-profile` | ARCHITECTURE.md §1 (exec summary) |
| Auth | Existing Hermes JWT tokens | ARCHITECTURE.md §5.5 |
| Permissions | owner, admin, member, viewer (RBAC) | tasks.md SPEC-DM-04 |
| Profile visibility | Per-tree toggle — profiles can be hidden from some trees | tasks.md SPEC-DM-04 |
| Multi-profile | Multiple Hermes profiles per user, each independent persona | DuckBrain /multi-profile |
| Cross-server federation | Profile tokens for remote Hermes instances (post-MVP) | ARCHITECTURE.md §5.5 |
| Tree membership | Explicit members table — not derived from node authorship | DuckBrain /multi-user |
| Invite flow | Owner generates invite, recipient accepts via token, permissions assigned | DuckBrain /multi-user |
| Agent participation | Agents LISTEN to all input, only ACT on approved input — regardless of role | ARCHITECTURE.md §8.2 |
| Profile context isolation | Each profile has independent context window per tree | DuckBrain /multi-profile |
| Authoritative DB | PostgreSQL 17+, pgx v5, golang-migrate | ARCHITECTURE.md §2.3, §2.1 |
| IDs | UUIDv7 (time-ordered, time-sortable) | SPEC-DM-01 §3.1 |
| User ↔ tree.owner_id | FK to users table, enforced from SPEC-DM-01 day one | SPEC-DM-01 §2 |
| Approval ↔ users/profiles | `approvals.owner_id` FK to users, `approvals.requested_by` FK to users or profiles | SPEC-DM-03 §3.1 |

---

## 3. PostgreSQL DDL

### 3.1 Profile Type Enum

```sql
-- 000004_profiles.up.sql (migration 000004 follows 000003_approvals)

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
```

### 3.2 Users Table

```sql
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
CREATE INDEX idx_users_hermes_user_id ON users(hermes_user_id);
CREATE INDEX idx_users_email ON users(email) WHERE email IS NOT NULL;
CREATE INDEX idx_users_created_at ON users(created_at);
```

### 3.3 Profiles Table

```sql
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
CREATE INDEX idx_profiles_owner_id ON profiles(owner_id);
CREATE INDEX idx_profiles_name ON profiles(name);
CREATE INDEX idx_profiles_type ON profiles(profile_type);
```

### 3.4 Tree Members Table

```sql
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
CREATE INDEX idx_tree_members_tree_id ON tree_members(tree_id);
CREATE INDEX idx_tree_members_user_id ON tree_members(user_id) WHERE user_id IS NOT NULL;
CREATE INDEX idx_tree_members_profile_id ON tree_members(profile_id) WHERE profile_id IS NOT NULL;
CREATE INDEX idx_tree_members_role ON tree_members(tree_id, role);
```

### 3.5 Profile Invites Table

```sql
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
CREATE INDEX idx_profile_invites_tree_id ON profile_invites(tree_id);
CREATE INDEX idx_profile_invites_token ON profile_invites(invite_token);
CREATE INDEX idx_profile_invites_status ON profile_invites(status);
CREATE UNIQUE INDEX idx_profile_invites_active ON profile_invites(tree_id, profile_id)
    WHERE status = 'pending';
```

### 3.6 Triggers

```sql
-- ============================================================
-- updated_at trigger (shared pattern from SPEC-DM-01)
-- ============================================================

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
```

### 3.7 Migration Files

```
migrations/
├── 000001_trees_nodes_edges.up.sql      (SPEC-DM-01)
├── 000002_tree_snapshots.up.sql         (SPEC-DM-02)
├── 000003_approvals.up.sql              (SPEC-DM-03)
├── 000004_profiles.up.sql               (THIS SPEC)
└── 000004_profiles.down.sql
```

---

## 4. Go Structs & Repository Interface

### 4.1 User

```go
// internal/tree/user.go

type User struct {
    ID            uuid.UUID  `json:"id"`
    HermesUserID  string     `json:"hermes_user_id"`
    Email         *string    `json:"email,omitempty"`
    DisplayName   string     `json:"display_name"`
    AvatarURL     *string    `json:"avatar_url,omitempty"`
    CreatedAt     time.Time  `json:"created_at"`
    UpdatedAt     time.Time  `json:"updated_at"`
    LastSeenAt    *time.Time `json:"last_seen_at,omitempty"`
    IsActive      bool       `json:"is_active"`
    DeletedAt     *time.Time `json:"-"`
}

// CreateUserParams is the input for creating a new user.
type CreateUserParams struct {
    HermesUserID string  `json:"hermes_user_id" validate:"required,min=1,max=255"`
    Email        *string `json:"email,omitempty" validate:"omitempty,email"`
    DisplayName  string  `json:"display_name" validate:"required,min=1,max=100"`
    AvatarURL    *string `json:"avatar_url,omitempty" validate:"omitempty,url"`
}

// UpdateUserParams is the input for updating an existing user.
type UpdateUserParams struct {
    DisplayName *string `json:"display_name,omitempty" validate:"omitempty,min=1,max=100"`
    AvatarURL   *string `json:"avatar_url,omitempty" validate:"omitempty,url"`
}
```

### 4.2 Profile

```go
// internal/tree/profile.go

type ProfileType string

const (
    ProfileTypeHuman        ProfileType = "human"
    ProfileTypeHermesProfile ProfileType = "hermes-profile"
)

type Profile struct {
    ID              uuid.UUID          `json:"id"`
    OwnerID         uuid.UUID          `json:"owner_id"`
    ProfileType     ProfileType        `json:"profile_type"`
    Name            string             `json:"name"`
    DisplayName     string             `json:"display_name"`
    Description     *string            `json:"description,omitempty"`
    ConfigJSON      json.RawMessage    `json:"config_json"`
    CanAutoRespond  bool               `json:"can_auto_respond"`
    ContextWindowSize int              `json:"context_window_size"`
    IsPublic        bool               `json:"is_public"`
    CreatedAt       time.Time          `json:"created_at"`
    UpdatedAt       time.Time          `json:"updated_at"`
    DeletedAt       *time.Time         `json:"-"`
}

// ProfileConfig is the typed form of config_json (example — implementation details deferred to Phase 4).
type ProfileConfig struct {
    Provider      string            `json:"provider,omitempty"`       // "deepseek", "openai", etc.
    Model         string            `json:"model,omitempty"`         // "deepseek-v4-pro"
    SystemPrompt  string            `json:"system_prompt,omitempty"` // Profile-specific system instructions
    Skills        []string          `json:"skills,omitempty"`        // Skill names to load
    Temperature   *float64          `json:"temperature,omitempty"`   // 0.0–2.0
    MaxTokens     *int              `json:"max_tokens,omitempty"`    // Output token limit
    Extra         map[string]any    `json:"extra,omitempty"`         // Provider-specific params
}

func (p *Profile) ParseConfig() (*ProfileConfig, error) {
    var cfg ProfileConfig
    if err := json.Unmarshal(p.ConfigJSON, &cfg); err != nil {
        return nil, fmt.Errorf("profile config parse: %w", err)
    }
    return &cfg, nil
}

type CreateProfileParams struct {
    OwnerID          uuid.UUID   `json:"owner_id" validate:"required"`
    Name             string      `json:"name" validate:"required,min=1,max=64"`
    DisplayName      string      `json:"display_name" validate:"required,min=1,max=200"`
    Description      *string     `json:"description,omitempty"`
    ConfigJSON       json.RawMessage `json:"config_json"`
    CanAutoRespond   bool        `json:"can_auto_respond"`
    ContextWindowSize int        `json:"context_window_size" validate:"min=1024,max=2097152"`
    IsPublic         bool        `json:"is_public"`
}
```

### 4.3 Tree Member

```go
// internal/tree/member.go

type TreeRole string

const (
    TreeRoleOwner  TreeRole = "owner"
    TreeRoleAdmin  TreeRole = "admin"
    TreeRoleMember TreeRole = "member"
    TreeRoleViewer TreeRole = "viewer"
)

// PermissionMatrix maps roles to allowed actions.
//
//   Action           | owner | admin | member | viewer
//   -----------------|-------|-------|--------|--------
//   tree.read         |  ✓    |  ✓    |  ✓     |  ✓
//   tree.update       |  ✓    |  ✓    |  —     |  —
//   tree.delete       |  ✓    |  —    |  —     |  —
//   tree.invite       |  ✓    |  ✓    |  —     |  —
//   tree.remove_member|  ✓    |  ✓    |  —     |  —
//   node.create       |  ✓    |  ✓    |  ✓     |  —
//   node.update_own   |  ✓    |  ✓    |  ✓     |  —
//   node.update_any   |  ✓    |  ✓    |  —     |  —
//   node.delete_own   |  ✓    |  ✓    |  ✓     |  —
//   node.delete_any   |  ✓    |  ✓    |  —     |  —
//   approval.approve  |  ✓    |  ✓    |  —     |  —
//   approval.deny     |  ✓    |  ✓    |  —     |  —
//   profile.configure |  ✓    |  —    |  —     |  —

var rolePermissions = map[TreeRole]map[string]bool{
    TreeRoleOwner: {
        "tree.read": true, "tree.update": true, "tree.delete": true,
        "tree.invite": true, "tree.remove_member": true,
        "node.create": true, "node.update_own": true, "node.update_any": true,
        "node.delete_own": true, "node.delete_any": true,
        "approval.approve": true, "approval.deny": true,
        "profile.configure": true,
    },
    TreeRoleAdmin: {
        "tree.read": true, "tree.update": true,
        "tree.invite": true, "tree.remove_member": true,
        "node.create": true, "node.update_own": true, "node.update_any": true,
        "node.delete_own": true, "node.delete_any": true,
        "approval.approve": true, "approval.deny": true,
    },
    TreeRoleMember: {
        "tree.read": true,
        "node.create": true, "node.update_own": true, "node.delete_own": true,
    },
    TreeRoleViewer: {
        "tree.read": true,
    },
}

// Can checks whether the role permits the given action.
func (r TreeRole) Can(action string) bool {
    perms, ok := rolePermissions[r]
    if !ok {
        return false
    }
    return perms[action]
}

// IsAtLeast returns true if this role is at least as powerful as other.
func (r TreeRole) IsAtLeast(other TreeRole) bool {
    rank := map[TreeRole]int{
        TreeRoleOwner:  3,
        TreeRoleAdmin:  2,
        TreeRoleMember: 1,
        TreeRoleViewer: 0,
    }
    return rank[r] >= rank[other]
}

type TreeMember struct {
    ID           uuid.UUID  `json:"id"`
    TreeID       uuid.UUID  `json:"tree_id"`
    UserID       *uuid.UUID `json:"user_id,omitempty"`
    ProfileID    *uuid.UUID `json:"profile_id,omitempty"`
    Role         TreeRole   `json:"role"`
    IsVisible    bool       `json:"is_visible"`
    AutoApproved bool       `json:"auto_approved"`
    JoinedAt     time.Time  `json:"joined_at"`
    InvitedBy    *uuid.UUID `json:"invited_by,omitempty"`
}

// ParticipantID returns the user or profile ID for this membership.
func (m *TreeMember) ParticipantID() uuid.UUID {
    if m.UserID != nil {
        return *m.UserID
    }
    return *m.ProfileID
}

// IsHuman returns true if this member is a human user.
func (m *TreeMember) IsHuman() bool { return m.UserID != nil }

// IsProfile returns true if this member is a Hermes profile.
func (m *TreeMember) IsProfile() bool { return m.ProfileID != nil }

type AddMemberParams struct {
    TreeID       uuid.UUID  `json:"tree_id" validate:"required"`
    UserID       *uuid.UUID `json:"user_id,omitempty"`
    ProfileID    *uuid.UUID `json:"profile_id,omitempty"`
    Role         TreeRole   `json:"role" validate:"required,oneof=owner admin member viewer"`
    AutoApproved bool       `json:"auto_approved"`
    InvitedBy    uuid.UUID  `json:"invited_by" validate:"required"`
}

type UpdateMemberParams struct {
    Role         *TreeRole `json:"role,omitempty" validate:"omitempty,oneof=owner admin member viewer"`
    IsVisible    *bool     `json:"is_visible,omitempty"`
    AutoApproved *bool     `json:"auto_approved,omitempty"`
}
```

### 4.4 Profile Invite

```go
// internal/tree/invite.go

type InviteStatus string

const (
    InviteStatusPending  InviteStatus = "pending"
    InviteStatusAccepted InviteStatus = "accepted"
    InviteStatusDeclined InviteStatus = "declined"
    InviteStatusExpired  InviteStatus = "expired"
)

type ProfileInvite struct {
    ID           uuid.UUID    `json:"id"`
    TreeID       uuid.UUID    `json:"tree_id"`
    ProfileID    uuid.UUID    `json:"profile_id"`
    InvitedBy    uuid.UUID    `json:"invited_by"`
    InviteToken  string       `json:"-"`                    // Never exposed in API responses
    Status       InviteStatus `json:"status"`
    ProposedRole TreeRole     `json:"proposed_role"`
    CreatedAt    time.Time    `json:"created_at"`
    ExpiresAt    time.Time    `json:"expires_at"`
    AcceptedAt   *time.Time   `json:"accepted_at,omitempty"`
    DeclinedAt   *time.Time   `json:"declined_at,omitempty"`
}

// IsExpired returns true if the invite has passed its expiration.
func (i *ProfileInvite) IsExpired() bool {
    return time.Now().After(i.ExpiresAt)
}

type CreateInviteParams struct {
    TreeID       uuid.UUID `json:"tree_id" validate:"required"`
    ProfileID    uuid.UUID `json:"profile_id" validate:"required"`
    InvitedBy    uuid.UUID `json:"invited_by" validate:"required"`
    ProposedRole TreeRole  `json:"proposed_role" validate:"required,oneof=admin member viewer"`
}

type AcceptInviteParams struct {
    Token string `json:"token" validate:"required,min=32"`
}

type DeclineInviteParams struct {
    Token string `json:"token" validate:"required,min=32"`
}
```

### 4.5 Repository Interfaces

```go
// internal/tree/repo.go (additions to existing repo interfaces)

// UserRepo manages user accounts.
type UserRepo interface {
    Create(ctx context.Context, params CreateUserParams) (*User, error)
    GetByID(ctx context.Context, id uuid.UUID) (*User, error)
    GetByHermesUserID(ctx context.Context, hermesUserID string) (*User, error)
    Update(ctx context.Context, id uuid.UUID, params UpdateUserParams) (*User, error)
    SoftDelete(ctx context.Context, id uuid.UUID) error
    List(ctx context.Context, opts ListUsersOptions) ([]User, int, error) // paginated
}

type ListUsersOptions struct {
    Limit  int    // max 100
    Offset int
    Search string // search display_name or email (ILIKE)
}

// ProfileRepo manages Hermes profiles.
type ProfileRepo interface {
    Create(ctx context.Context, params CreateProfileParams) (*Profile, error)
    GetByID(ctx context.Context, id uuid.UUID) (*Profile, error)
    GetByOwnerAndName(ctx context.Context, ownerID uuid.UUID, name string) (*Profile, error)
    ListByOwner(ctx context.Context, ownerID uuid.UUID) ([]Profile, error)
    Update(ctx context.Context, id uuid.UUID, params UpdateProfileParams) (*Profile, error)
    SoftDelete(ctx context.Context, id uuid.UUID) error
}

type UpdateProfileParams struct {
    DisplayName      *string          `json:"display_name,omitempty" validate:"omitempty,min=1,max=200"`
    Description      *string          `json:"description,omitempty"`
    ConfigJSON       *json.RawMessage `json:"config_json,omitempty"`
    CanAutoRespond   *bool            `json:"can_auto_respond,omitempty"`
    ContextWindowSize *int            `json:"context_window_size,omitempty" validate:"omitempty,min=1024,max=2097152"`
    IsPublic         *bool            `json:"is_public,omitempty"`
}

// TreeMemberRepo manages tree memberships.
type TreeMemberRepo interface {
    Add(ctx context.Context, params AddMemberParams) (*TreeMember, error)
    GetByID(ctx context.Context, id uuid.UUID) (*TreeMember, error)
    GetByTreeAndUser(ctx context.Context, treeID, userID uuid.UUID) (*TreeMember, error)
    GetByTreeAndProfile(ctx context.Context, treeID, profileID uuid.UUID) (*TreeMember, error)
    ListByTree(ctx context.Context, treeID uuid.UUID) ([]TreeMember, error)
    ListByUser(ctx context.Context, userID uuid.UUID) ([]TreeMember, error)
    ListByProfile(ctx context.Context, profileID uuid.UUID) ([]TreeMember, error)
    Update(ctx context.Context, id uuid.UUID, params UpdateMemberParams) (*TreeMember, error)
    Remove(ctx context.Context, id uuid.UUID) error // hard-delete (approval audit trail is separate)
    Exists(ctx context.Context, treeID uuid.UUID, participantID uuid.UUID) (bool, error)
}

// ProfileInviteRepo manages profile invite lifecycle.
type ProfileInviteRepo interface {
    Create(ctx context.Context, params CreateInviteParams) (*ProfileInvite, error)
    GetByID(ctx context.Context, id uuid.UUID) (*ProfileInvite, error)
    GetByToken(ctx context.Context, token string) (*ProfileInvite, error)
    Accept(ctx context.Context, token string) (*TreeMember, error) // creates membership on accept
    Decline(ctx context.Context, token string) error
    // ExpireStale marks all expired invites. Called by cron/scheduler.
    ExpireStale(ctx context.Context) (int64, error) // returns count of expired
    ListByTree(ctx context.Context, treeID uuid.UUID) ([]ProfileInvite, error)
    ListPendingByProfile(ctx context.Context, profileID uuid.UUID) ([]ProfileInvite, error)
}
```

---

## 5. TypeScript Types

### 5.1 Types

```typescript
// src/types/user.ts

import { z } from 'zod';

// ── Enums ──────────────────────────────────────────────

export const ProfileType = z.enum(['human', 'hermes-profile']);
export type ProfileType = z.infer<typeof ProfileType>;

export const TreeRole = z.enum(['owner', 'admin', 'member', 'viewer']);
export type TreeRole = z.infer<typeof TreeRole>;

export const InviteStatus = z.enum(['pending', 'accepted', 'declined', 'expired']);
export type InviteStatus = z.infer<typeof InviteStatus>;

// ── User ───────────────────────────────────────────────

export const UserSchema = z.object({
  id: z.string().uuid(),
  hermes_user_id: z.string().min(1).max(255),
  email: z.string().email().nullable().optional(),
  display_name: z.string().min(1).max(100),
  avatar_url: z.string().url().nullable().optional(),
  created_at: z.string().datetime(),
  updated_at: z.string().datetime(),
  last_seen_at: z.string().datetime().nullable().optional(),
  is_active: z.boolean(),
});

export type User = z.infer<typeof UserSchema>;

// ── Profile ────────────────────────────────────────────

export const ProfileConfigSchema = z.object({
  provider: z.string().optional(),
  model: z.string().optional(),
  system_prompt: z.string().optional(),
  skills: z.array(z.string()).optional(),
  temperature: z.number().min(0).max(2).optional(),
  max_tokens: z.number().int().positive().optional(),
  extra: z.record(z.string(), z.unknown()).optional(),
});

export const ProfileSchema = z.object({
  id: z.string().uuid(),
  owner_id: z.string().uuid(),
  profile_type: ProfileType,
  name: z.string().min(1).max(64),
  display_name: z.string().min(1).max(200),
  description: z.string().nullable().optional(),
  config_json: ProfileConfigSchema,
  can_auto_respond: z.boolean(),
  context_window_size: z.number().int().min(1024).max(2097152),
  is_public: z.boolean(),
  created_at: z.string().datetime(),
  updated_at: z.string().datetime(),
});

export type Profile = z.infer<typeof ProfileSchema>;
export type ProfileConfig = z.infer<typeof ProfileConfigSchema>;

// ── Tree Member ────────────────────────────────────────

export const TreeMemberSchema = z.object({
  id: z.string().uuid(),
  tree_id: z.string().uuid(),
  user_id: z.string().uuid().nullable().optional(),
  profile_id: z.string().uuid().nullable().optional(),
  role: TreeRole,
  is_visible: z.boolean(),
  auto_approved: z.boolean(),
  joined_at: z.string().datetime(),
  invited_by: z.string().uuid().nullable().optional(),
});

export type TreeMember = z.infer<typeof TreeMemberSchema>;

// ── Profile Invite ─────────────────────────────────────

export const ProfileInviteSchema = z.object({
  id: z.string().uuid(),
  tree_id: z.string().uuid(),
  profile_id: z.string().uuid(),
  invited_by: z.string().uuid(),
  // token is NEVER exposed to client — server-side only
  status: InviteStatus,
  proposed_role: TreeRole,
  created_at: z.string().datetime(),
  expires_at: z.string().datetime(),
  accepted_at: z.string().datetime().nullable().optional(),
  declined_at: z.string().datetime().nullable().optional(),
});

export type ProfileInvite = z.infer<typeof ProfileInviteSchema>;
```

### 5.2 Permission Matrix (TypeScript)

```typescript
// src/lib/permissions.ts

export const PERMISSIONS = {
  owner: {
    'tree.read': true,
    'tree.update': true,
    'tree.delete': true,
    'tree.invite': true,
    'tree.remove_member': true,
    'node.create': true,
    'node.update_own': true,
    'node.update_any': true,
    'node.delete_own': true,
    'node.delete_any': true,
    'approval.approve': true,
    'approval.deny': true,
    'profile.configure': true,
  },
  admin: {
    'tree.read': true,
    'tree.update': true,
    'tree.invite': true,
    'tree.remove_member': true,
    'node.create': true,
    'node.update_own': true,
    'node.update_any': true,
    'node.delete_own': true,
    'node.delete_any': true,
    'approval.approve': true,
    'approval.deny': true,
  },
  member: {
    'tree.read': true,
    'node.create': true,
    'node.update_own': true,
    'node.delete_own': true,
  },
  viewer: {
    'tree.read': true,
  },
} as const;

export type PermissionAction = keyof typeof PERMISSIONS.owner;

export function can(role: TreeRole, action: PermissionAction): boolean {
  return PERMISSIONS[role]?.[action] ?? false;
}

export function roleIsAtLeast(a: TreeRole, b: TreeRole): boolean {
  const rank: Record<TreeRole, number> = { owner: 3, admin: 2, member: 1, viewer: 0 };
  return rank[a] >= rank[b];
}
```

---

## 6. Invite Flow State Machine

```
                          ┌──────────┐
                          │  Create   │
                          │  Invite   │
                          └────┬─────┘
                               │
                               ▼
                         ┌──────────┐
                    ┌───▶│ pending   │───┐
                    │    └──────────┘   │
                    │         │         │
                    │         │         │
               expires     accept    decline
                    │         │         │
                    ▼         ▼         ▼
              ┌──────────┐ ┌──────────┐ ┌──────────┐
              │ expired   │ │ accepted │ │ declined  │
              └──────────┘ └────┬─────┘ └──────────┘
                                │
                                │ creates
                                ▼
                          ┌──────────┐
                          │tree_member│
                          │  record   │
                          └──────────┘
```

**States:**
1. **pending** — Invite created, awaiting recipient action. Token is valid.
2. **accepted** — Recipient accepted. `tree_members` row created. Invite record preserved for audit.
3. **declined** — Recipient explicitly declined. Invite record preserved.
4. **expired** — Passed `expires_at` without action. Set by cron or on-access check.

**Transitions:**
- `pending → accepted`: POST `/invites/{token}/accept`. Creates `tree_members` row with `proposed_role`.
- `pending → declined`: POST `/invites/{token}/decline`. Sets `declined_at`.
- `pending → expired`: Automatic. `ExpireStale()` marks all where `expires_at < now() AND status = 'pending'`.

**Idempotency:**
- Accepting an already-accepted invite returns the existing `tree_members` row (idempotent).
- Accepting an expired invite returns an error.
- Declining an already-declined invite is idempotent.

**Invite Token Generation:**
```go
import "crypto/rand"

func GenerateInviteToken() (string, error) {
    b := make([]byte, 32)
    if _, err := rand.Read(b); err != nil {
        return "", err
    }
    return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(b), nil
}
```
Result: 43-character URL-safe token (32 bytes → 43 base64url chars).

---

## 7. Auth Integration

### 7.1 Hermes JWT → User Resolution

```go
// internal/auth/middleware.go

// ResolveUser extracts the Hermes user ID from the validated JWT
// and resolves it to a Canopy user record.
//
// Flow:
//   1. JWT validated (existing Hermes auth middleware)
//   2. Extract `sub` claim → hermes_user_id
//   3. Lookup user by hermes_user_id
//   4. If user doesn't exist → auto-create (first login) or return 401
//   5. Attach User to request context

type contextKey string

const (
    ctxKeyUser    contextKey = "canopy:user"
    ctxKeyProfile contextKey = "canopy:profile"
)

func GetUser(ctx context.Context) (*User, bool) {
    u, ok := ctx.Value(ctxKeyUser).(*User)
    return u, ok
}

func RequireUser(ctx context.Context) (*User, error) {
    u, ok := GetUser(ctx)
    if !ok {
        return nil, ErrUnauthenticated
    }
    return u, nil
}
```

### 7.2 Profile Token → Profile Resolution (Post-MVP)

```go
// For cross-server federation (post-MVP):
// Profile tokens are JWTs signed by the profile owner's Hermes instance.
// They contain: profile_id, owner_id, instance_origin, capabilities.
// The receiving canopyd validates the token against the issuing instance's JWKS endpoint.
type ProfileTokenClaims struct {
    jwt.RegisteredClaims
    ProfileID      string   `json:"profile_id"`
    OwnerID        string   `json:"owner_id"`
    InstanceOrigin string   `json:"instance_origin"` // https://hermes.example.com
    Capabilities   []string `json:"capabilities"`    // ["tree.read", "node.create", ...]
}
```

### 7.3 Tree Membership Middleware

```go
// RequireTreeMembership validates that the authenticated user or profile
// is a member of the requested tree with at least the required role.
func RequireTreeMembership(memberRepo TreeMemberRepo, minRole TreeRole) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            user, err := RequireUser(r.Context())
            if err != nil {
                http.Error(w, err.Error(), http.StatusUnauthorized)
                return
            }
            
            treeID, err := extractTreeID(r)
            if err != nil {
                http.Error(w, "invalid tree ID", http.StatusBadRequest)
                return
            }
            
            member, err := memberRepo.GetByTreeAndUser(r.Context(), treeID, user.ID)
            if err != nil {
                http.Error(w, "not a tree member", http.StatusForbidden)
                return
            }
            
            if !member.Role.IsAtLeast(minRole) {
                http.Error(w, "insufficient permissions", http.StatusForbidden)
                return
            }
            
            ctx := context.WithValue(r.Context(), ctxKeyMember, member)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}
```

---

## 8. Wiring

### 8.1 PostgreSQL Schema Dependency Order

```
SPEC-DM-01 (000001): trees, nodes, edges       ← no FKs to users/profiles
SPEC-DM-02 (000002): tree_snapshots            ← FK to trees
SPEC-DM-03 (000003): approvals, rules, audit   ← FK to trees, nodes
SPEC-DM-04 (000004): users, profiles, members, invites  ← FK to trees, FKs between users/profiles

NOTE: trees.owner_id is a UUID column defined in SPEC-DM-01 §3.2.
It was added speculatively without an FK constraint.
Migration 000004 adds the FK constraint:
  ALTER TABLE trees ADD CONSTRAINT fk_trees_owner
    FOREIGN KEY (owner_id) REFERENCES users(id) ON DELETE RESTRICT;
```

### 8.2 Go Package Structure

```
internal/
├── tree/
│   ├── node.go           (SPEC-DM-01)
│   ├── edge.go           (SPEC-DM-01)
│   ├── snapshot.go       (SPEC-DM-02)
│   ├── approval.go       (SPEC-DM-03)
│   ├── user.go           (THIS SPEC)
│   ├── profile.go        (THIS SPEC)
│   ├── member.go         (THIS SPEC)
│   ├── invite.go         (THIS SPEC)
│   └── repo.go           (all repo interfaces)
├── auth/
│   ├── middleware.go     (JWT → user resolution, tree membership)
│   └── tokens.go         (profile token verification — post-MVP)
└── migrate/
    └── migrations/       (000001–000004 .up.sql / .down.sql)
```

### 8.3 HTTP Endpoints (Preview — full spec in Phase 3)

| Method | Path | Auth | Min Role | Description |
|--------|------|------|----------|-------------|
| GET | `/api/users/me` | JWT | — | Get current user |
| PATCH | `/api/users/me` | JWT | — | Update current user |
| GET | `/api/profiles` | JWT | — | List user's profiles |
| POST | `/api/profiles` | JWT | — | Create profile |
| GET | `/api/profiles/{id}` | JWT | — | Get profile |
| PATCH | `/api/profiles/{id}` | JWT | — | Update profile |
| DELETE | `/api/profiles/{id}` | JWT | — | Soft-delete profile |
| GET | `/api/trees/{tree_id}/members` | JWT+Tree | member | List tree members |
| POST | `/api/trees/{tree_id}/members` | JWT+Tree | admin | Add member directly |
| PATCH | `/api/trees/{tree_id}/members/{id}` | JWT+Tree | admin | Update member role/visibility |
| DELETE | `/api/trees/{tree_id}/members/{id}` | JWT+Tree | admin | Remove member |
| POST | `/api/trees/{tree_id}/profiles/{profile_id}/invite` | JWT+Tree | admin | Invite profile |
| GET | `/api/invites` | JWT | — | List pending invites for user's profiles |
| POST | `/api/invites/{token}/accept` | JWT | — | Accept invite |
| POST | `/api/invites/{token}/decline` | JWT | — | Decline invite |

---

## 9. Error Catalog

| HTTP Status | Error Code | Condition |
|-------------|------------|-----------|
| 400 | `INVALID_DISPLAY_NAME` | Display name empty or >100 chars |
| 400 | `INVALID_PROFILE_NAME` | Profile name empty or >64 chars |
| 400 | `DUPLICATE_PROFILE_NAME` | Profile name already exists for this owner |
| 400 | `INVALID_ROLE` | Role not one of owner/admin/member/viewer |
| 400 | `INVALID_INVITE_ROLE` | Proposed role is 'owner' (owners are set at tree creation) |
| 400 | `CANNOT_REMOVE_OWNER` | Attempting to remove the tree owner |
| 400 | `CANNOT_DEMOTE_OWNER` | Attempting to change owner's role |
| 400 | `MISSING_PARTICIPANT` | Neither user_id nor profile_id provided |
| 400 | `DUAL_PARTICIPANT` | Both user_id and profile_id provided (exactly one required) |
| 401 | `UNAUTHENTICATED` | No valid JWT token |
| 403 | `NOT_TREE_MEMBER` | Authenticated but not a member of this tree |
| 403 | `INSUFFICIENT_PERMISSIONS` | Member but role too low for the action |
| 404 | `USER_NOT_FOUND` | User ID doesn't exist |
| 404 | `PROFILE_NOT_FOUND` | Profile ID doesn't exist |
| 404 | `TREE_NOT_FOUND` | Tree ID doesn't exist |
| 404 | `INVITE_NOT_FOUND` | Invite token doesn't exist |
| 404 | `MEMBER_NOT_FOUND` | Member ID doesn't exist in this tree |
| 409 | `ALREADY_MEMBER` | User or profile is already a tree member |
| 409 | `INVITE_ALREADY_PENDING` | Active invite already exists for this profile+tree |
| 410 | `INVITE_EXPIRED` | Invite has passed expires_at |
| 422 | `INVITE_ALREADY_ACCEPTED` | Invite was already accepted |
| 422 | `INVITE_ALREADY_DECLINED` | Invite was already declined |

---

## 10. Edge Cases

### 10.1 Tree Creation — First Member
When a tree is created, the creating user is automatically added as a `tree_members` row with role `owner`. The `trees.owner_id` column references this membership. Both happen in a single transaction.

### 10.2 Owner Cannot Be Removed
The owner role is special. Attempting to remove the owner's membership row returns `CANNOT_REMOVE_OWNER`. The owner can transfer ownership to another member first, then be demoted to admin/member.

### 10.3 Profile Visibility Toggle
A profile with `is_visible = false` in a tree still participates fully (receives context, can respond). It simply doesn't appear in the member list shown to other members. The owner always sees all profiles regardless of visibility.

### 10.4 Auto-Approved Members
When `auto_approved = true`, messages from this member skip the approval queue entirely. The agent acts on them immediately. Use with caution — typically for trusted human collaborators, not profiles.

### 10.5 Self-Invite Prevention
A user cannot invite their own profile to a tree they already own. The owner's profiles are implicitly members (auto-added on tree creation). Explicit invites from owner to own profile return `ALREADY_MEMBER`.

### 10.6 Deleted Profiles
When a profile is soft-deleted, existing `tree_members` rows remain (FK is ON DELETE CASCADE for hard-delete only, but soft-delete uses `deleted_at`). The profile is excluded from active member lists. The `is_active` check is at the application layer, not the database.

### 10.7 Cascade on Tree Deletion
When a tree is deleted (soft-delete via `trees.deleted_at`), all `tree_members` rows for that tree remain in the database for audit purposes. Hard-delete of a tree cascades to `tree_members` (ON DELETE CASCADE).

### 10.8 Viewer Restrictions
A viewer can read the tree but cannot create nodes, update any nodes, or perform any approval actions. They are invisible to the agent (not included in context assembly). This is for read-only observers like auditors or stakeholders.

### 10.9 Concurrent Invite Acceptance
If two users try to accept the same invite simultaneously, PostgreSQL's unique constraint on `profile_invites(tree_id, profile_id) WHERE status = 'pending'` prevents duplicate memberships. The second caller receives `INVITE_ALREADY_ACCEPTED` (the first succeeded).

---

## 11. Testing

### 11.1 Unit Tests (Go)

| Test | What It Verifies |
|------|-----------------|
| `TestTreeRole_Can` | Every role returns correct permissions for every action |
| `TestTreeRole_IsAtLeast` | Role comparison: owner > admin > member > viewer |
| `TestProfile_ParseConfig` | Valid JSON → ProfileConfig, invalid JSON → error |
| `TestProfile_ParseConfig_EmptyJSON` | Empty config → default zero-value ProfileConfig |
| `TestGenerateInviteToken` | Token is 43 chars, URL-safe, no collisions in 1000 calls |
| `TestTreeMember_ParticipantID` | Returns correct UUID from user_id or profile_id |
| `TestTreeMember_IsHuman` | True when user_id set, false when profile_id set |
| `TestTreeMember_IsProfile` | True when profile_id set, false when user_id set |
| `TestCreateUserParams_Validation` | Valid params pass, invalid rejected |
| `TestCreateProfileParams_Validation` | Valid params pass, context_window_size range enforced |
| `TestAddMemberParams_Validation` | Exactly one of user_id/profile_id, valid role enum |

### 11.2 Integration Tests (Go, against test PostgreSQL)

| Test | What It Verifies |
|------|-----------------|
| `TestUserRepo_CRUD` | Create → GetByID → Update → SoftDelete → GetByID returns 404 |
| `TestUserRepo_GetByHermesUserID` | Lookup by auth provider ID |
| `TestUserRepo_DuplicateHermesUserID` | Unique constraint enforced |
| `TestProfileRepo_Create` | Create with valid params |
| `TestProfileRepo_DuplicateName` | Same owner, same name → error |
| `TestProfileRepo_ListByOwner` | Returns only that owner's profiles |
| `TestTreeMemberRepo_Add` | Add user and profile members to tree |
| `TestTreeMemberRepo_DualAdd` | Both user_id and profile_id → error |
| `TestTreeMemberRepo_DuplicateUser` | Same user+tree → unique constraint |
| `TestTreeMemberRepo_DuplicateProfile` | Same profile+tree → unique constraint |
| `TestTreeMemberRepo_Remove` | Hard-delete removes row |
| `TestTreeMemberRepo_CannotRemoveOwner` | Application-level guard |
| `TestProfileInviteRepo_Create` | Create invite, verify token generated |
| `TestProfileInviteRepo_Accept` | Accept → membership created, invite status updated |
| `TestProfileInviteRepo_Accept_Expired` | Expired invite → error |
| `TestProfileInviteRepo_Accept_Idempotent` | Accept twice → second call returns same membership |
| `TestProfileInviteRepo_Decline` | Decline → status updated, no membership created |
| `TestProfileInviteRepo_ExpireStale` | Expired pending invites → status set to expired |
| `TestProfileInviteRepo_DuplicateActive` | Two pending invites for same profile+tree → error |

### 11.3 Yjs CRDT Tests (TypeScript/Vitest)

| Test | What It Verifies |
|------|-----------------|
| `Test_TreeMember_YMap_Add` | Add member to Y.Map, verify synced to all peers |
| `Test_TreeMember_YMap_Remove` | Remove member, verify tombstone behavior |
| `Test_ProfileVisibility_Toggle` | Toggle is_visible, verify UI hides/shows member |
| `Test_Invite_Token_NotInYjs` | Invite tokens never enter CRDT (server-side only) |

### 11.4 Property-Based Tests (Go, fast-check equivalent)

| Test | What It Verifies |
|------|-----------------|
| `FuzzProfileConfig_RoundTrip` | Any valid JSON → ParseConfig → re-marshal → identical |
| `FuzzInviteToken_Uniqueness` | 10,000 generated tokens → all unique |
| `FuzzTreeRole_Serialization` | Any role → JSON marshal/unmarshal → same value |

---

## 12. Performance

| Benchmark | Target | Notes |
|-----------|--------|-------|
| `UserRepo.GetByHermesUserID` | <1ms p99 | Indexed by unique hermes_user_id |
| `ProfileRepo.ListByOwner` | <5ms p99 (up to 50 profiles) | Indexed by owner_id |
| `TreeMemberRepo.ListByTree` | <10ms p99 (up to 1000 members) | Indexed by tree_id |
| `TreeMemberRepo.Exists` | <1ms p99 | Composite unique index |
| `ProfileInviteRepo.GetByToken` | <2ms p99 | Indexed by unique invite_token |
| `ProfileInviteRepo.ExpireStale` | <50ms p99 | Batch update, index on status+expires_at |
| `TreeMemberRepo.ListByUser` | <10ms p99 (all trees for a user) | Indexed by user_id |
| `RequireTreeMembership middleware` | <2ms p99 (including DB call) | Cached in request context for repeat calls |
