# SPEC-FTR-01 — Multi-User Collaboration & Approval Model

> **Status:** Spec | **Phase:** Post-MVP (FTR-01) | **Blocks:** BE-07 (Auth & Approval Engine), BE-08 (Profile Routing), FE-06 (Approval Panel), FE-07 (Multi-User Features), FE-10 (Accessibility)
> **References:** SPEC-API-05, SPEC-API-06, SPEC-DM-03, SPEC-DM-04, SPEC-API-01, SPEC-API-04, SPEC-TM-03, ARCHITECTURE.md §4, AGENTS.md §Deferred
> **Commit:** _(to be filled)_

---

## 1. Purpose

Define the exact implementation contract for multi-user real-time collaboration and approval-gated agent actions in Canopy OS. A Go worker reading this document can implement the collaboration service, presence tracker, approval gate engine, and CLI/HTTP wiring without additional design decisions. A TypeScript worker can implement the collaboration client, presence overlay, approval panel, and SSE subscription layer without guessing wire shapes.

The MVP is single-user with local PostgreSQL and SSE/HTTP transport (see SPEC-API-01 §SSE). This spec extends that architecture to N collaborating users on the same tree, with user-level permissions, real-time presence, and configurable approval gates that require human sign-off before an agent action applies to shared state.

---

## 2. Design Decisions

| # | Decision | Choice | Rationale |
|---|----------|--------|-----------|
| 1 | Collaboration model | Server-managed workspace with CRDT-based synchronisation | Yjs CRDTs (T1.2 decision) provide conflict-free merging. The server is the authoritative relay; peers never directly synchronise without server mediation in MVP. |
| 2 | User identification | UUIDv7 per installation, human-readable @handle per user, human-readable display name per profile | The UUID ties every mutation to an origin device. The handle is a user-chosen unique name for the workspace. The display name is free-form for UI. |
| 3 | Authentication | Token-based session via `Bearer` header, issued by workspace on first connection, validated per-request | No external identity provider in MVP. Workspace issues opaque tokens on password or invitation link. Tokens encode user UUID, handle, and role. |
| 4 | Session lifetime | 24-hour token with sliding window renewal on each SSE reconnect | Avoids forcing re-login during active sessions. Revoked tokens are blacklisted server-side. |
| 5 | Role model | Exactly three roles: `admin`, `editor`, `viewer` | A closed set gives the permission check a deterministic switch. Admin can manage users and change tree metadata. Editor can add, edit, delete nodes. Viewer can read only. |
| 6 | Permission scope | Per-tree (workspace-level) roles, not per-node | MVP-conservative: node-level ACL is a future extension. The role applies to the entire workspace tree. |
| 7 | Presence mechanism | Heartbeat SSE channel: each client sends `ping` every 30s, server broadcasts `user_online` / `user_offline` events | Heartbeats are lightweight and the SSE stream is already open. No separate WebSocket needed for presence in MVP. |
| 8 | Typing indicator | Debounced `user_typing` event (max once per 2s per user) | Avoids SSE flooding while keeping the indicator responsive. The event carries the node ID being composed. |
| 9 | Node locking | Optimistic: no explicit lock. Conflicting edits are resolved by Yjs CRDT merge. Explicit `node_lock` for long-running agent operations. | Most human edits are fast. Long agent actions (file read, code execution) should lock the node to prevent human interruption. |
| 10 | Lock duration | Maximum 30s per lock; auto-released on timeout. Agents may renew. | Prevents stale locks from abandoned agent sessions. 30s covers any single agent operation. |
| 11 | Approval gate model | Three-state gates: `pending` → `approved` / `rejected`. Gates are per-action, not per-node. | An agent action (delete node, modify shared state) creates a gate. The gate tracks which user must approve. |
| 12 | Approval timeout | 5-minute default; configurable per-workspace. Expired gates auto-reject. | Prevents orphaned gates from blocked queues. Configurable for flexible workflows. |
| 13 | Approval quorum | Single-user approval. Future: N-of-M quorum. | MVP for multi-user keeps it simple: one authorised user approves or rejects. |
| 14 | Gate target | The user(s) with role ≥ `editor` currently viewing the affected subtree | Approval is requested from present, authorised users, not the entire workspace roster. |
| 15 | Action catalogue | Agent actions requiring approval: `node_delete`, `node_modify_bulk`, `tree_rename`, `user_invite`, `agent_spawn`. Free actions: `node_add`, `node_modify_single`. | Five actions are gated because they are destructive or affect other users. Single-node edits by the creating user are free. |
| 16 | SSE event routing | Events are tagged with target user UUID. The server broadcasts only to the targeted user's SSE stream. | Keeps SSE bandwidth per-client bounded. Only relevant events enter each user's stream. |
| 17 | Conflict resolution | Yjs CRDT for merged edits. Server-side last-writer-wins for metadata (title, description). | CRDTs handle concurrent text and node additions deterministically. Metadata is small and rare enough that LWW is safe. |
| 18 | Offline backlog | Server queues events for offline users. On reconnect, replay missed events since last seen event ID. | Queued per-user, capped at 10,000 events. The client tracks `last_event_id` in IndexedDB. |
| 19 | Invitation model | Workspace owner generates a one-time invitation link. Link carries a signed token. | No email infrastructure in MVP. Links are shared out-of-band (chat, docs, in person). |
| 20 | Rate limiting | Per-user: 100 mutations/minute, 10 approval gated actions/hour, 5 invitation generations/hour | Protects workspace stability. Thresholds are configurable via workspace admin panel. |

---

## 3. Go Interface Definitions

### 3.1 Collaboration Service

```go
package collaboration

import (
    "context"
    "time"
    "github.com/google/uuid"
)

// Role defines the permission level for a user within a workspace.
type Role int

const (
    RoleViewer Role = iota
    RoleEditor
    RoleAdmin
)

func (r Role) String() string {
    switch r {
    case RoleViewer: return "viewer"
    case RoleEditor: return "editor"
    case RoleAdmin:  return "admin"
    default:         return "unknown"
    }
}

// Workspace represents a collaborative tree shared by multiple users.
type Workspace struct {
    ID          uuid.UUID `json:"id"`
    OwnerID     uuid.UUID `json:"owner_id"`
    Name        string    `json:"name"`
    TreeID      uuid.UUID `json:"tree_id"`
    Members     []Member  `json:"members"`
    ApprovalTTL Duration  `json:"approval_ttl"`    // default 5m
    CreatedAt   time.Time `json:"created_at"`
}

// Member associates a user with a role inside a workspace.
type Member struct {
    UserID uuid.UUID `json:"user_id"`
    Handle string    `json:"handle"`
    Role   Role      `json:"role"`
    JoinedAt time.Time `json:"joined_at"`
}

// CollaborationService is the primary interface for multi-user collaboration.
type CollaborationService interface {
    // CreateWorkspace creates a new workspace with the caller as admin.
    CreateWorkspace(ctx context.Context, ownerID uuid.UUID, name string) (*Workspace, error)

    // GetWorkspace retrieves a workspace by ID. Returns ErrNotFound if missing.
    GetWorkspace(ctx context.Context, workspaceID uuid.UUID) (*Workspace, error)

    // JoinWorkspace adds a user to a workspace using a valid invitation token.
    JoinWorkspace(ctx context.Context, workspaceID uuid.UUID, userID uuid.UUID, token string) error

    // LeaveWorkspace removes a user from a workspace.
    LeaveWorkspace(ctx context.Context, workspaceID uuid.UUID, userID uuid.UUID) error

    // GetUserWorkspaces returns all workspaces a user belongs to.
    GetUserWorkspaces(ctx context.Context, userID uuid.UUID) ([]*Workspace, error)

    // UpdateMemberRole changes a member's role. The caller must be admin.
    UpdateMemberRole(ctx context.Context, workspaceID, callerID, targetID uuid.UUID, newRole Role) error

    // RemoveMember removes a member from the workspace. The caller must be admin.
    RemoveMember(ctx context.Context, workspaceID, callerID, targetID uuid.UUID) error

    // GenerateInvitation creates a one-time invitation token for the workspace.
    GenerateInvitation(ctx context.Context, workspaceID, callerID uuid.UUID) (token string, expiresAt time.Time, err error)
}
```

### 3.2 Presence Tracker

```go
package collaboration

import "context"

// PresenceState indicates the user's current collaboration status.
type PresenceState int

const (
    PresenceOffline PresenceState = iota
    PresenceOnline
    PresenceAway
    PresenceBusy
)

// UserPresence represents a user's live status in a workspace.
type UserPresence struct {
    UserID        uuid.UUID     `json:"user_id"`
    Handle        string        `json:"handle"`
    State         PresenceState `json:"state"`
    CurrentNodeID *uuid.UUID    `json:"current_node_id,omitempty"` // nil = browsing tree
    LastHeartbeat time.Time     `json:"last_heartbeat"`
    TypingForNode *uuid.UUID    `json:"typing_for_node,omitempty"` // nil = not typing
}

// PresenceTracker manages user heartbeats and broadcasts presence changes.
type PresenceTracker interface {
    // Ping registers a heartbeat for a user in a workspace. Returns the current workspace presence snapshot.
    Ping(ctx context.Context, workspaceID, userID uuid.UUID, currentNodeID *uuid.UUID) ([]UserPresence, error)

    // SetTyping registers a typing-indicator event for a node. Repeated calls within 2s are debounced.
    SetTyping(ctx context.Context, workspaceID, userID uuid.UUID, nodeID uuid.UUID) error

    // ClearTyping removes the typing indicator.
    ClearTyping(ctx context.Context, workspaceID, userID uuid.UUID) error

    // GetPresence returns the current presence state for all users in the workspace.
    GetPresence(ctx context.Context, workspaceID uuid.UUID) ([]UserPresence, error)

    // MarkOffline is called when a user disconnects or their session expires.
    MarkOffline(ctx context.Context, workspaceID, userID uuid.UUID) error

    // HeartbeatLoop runs a goroutine that checks for stale users (no ping in 90s).
    HeartbeatLoop(ctx context.Context, checkInterval time.Duration)
}
```

### 3.3 Node Lock Manager

```go
package collaboration

import "context"

// Lock represents an exclusive write lock on a node held by an agent or user action.
type Lock struct {
    NodeID      uuid.UUID `json:"node_id"`
    HolderID    uuid.UUID `json:"holder_id"`
    WorkspaceID uuid.UUID `json:"workspace_id"`
    AcquiredAt  time.Time `json:"acquired_at"`
    ExpiresAt   time.Time `json:"expires_at"`
    Reason      string    `json:"reason"` // human-readable: "Running code execution..."
}

// NodeLockManager manages optimistic locks for long-running agent operations.
type NodeLockManager interface {
    // AcquireLock attempts to acquire a lock on a node. Returns ErrLockHeld if another lock exists.
    AcquireLock(ctx context.Context, workspaceID, nodeID, holderID uuid.UUID, ttl time.Duration, reason string) (*Lock, error)

    // ReleaseLock releases an acquired lock. Returns ErrNotLockOwner if the caller is not the holder.
    ReleaseLock(ctx context.Context, workspaceID, nodeID, holderID uuid.UUID) error

    // RenewLock extends the lock by ttl. Must be called before the current lock expires.
    RenewLock(ctx context.Context, workspaceID, nodeID, holderID uuid.UUID, ttl time.Duration) (*Lock, error)

    // GetLocks returns all active locks for a workspace.
    GetLocks(ctx context.Context, workspaceID uuid.UUID) ([]Lock, error)

    // LockCleanupLoop runs a goroutine that releases expired locks every 10s.
    LockCleanupLoop(ctx context.Context, checkInterval time.Duration)
}
```

### 3.4 Approval Gate Engine

```go
package collaboration

import "context"

// ApprovalGate tracks a single agent action that requires user approval.
type ApprovalGate struct {
    ID          uuid.UUID     `json:"id"`
    WorkspaceID uuid.UUID     `json:"workspace_id"`
    Action      string        `json:"action"`     // one of: node_delete, node_modify_bulk, tree_rename, user_invite, agent_spawn
    ActorID     uuid.UUID     `json:"actor_id"`   // agent or user requesting the action
    TargetNode  *uuid.UUID    `json:"target_node,omitempty"`
    Payload     []byte        `json:"payload"`    // JSON-encoded action parameters; returned to the actor on approval
    Status      GateStatus    `json:"status"`
    CreatedAt   time.Time     `json:"created_at"`
    ExpiresAt   time.Time     `json:"expires_at"`
    ApprovedBy  *uuid.UUID    `json:"approved_by,omitempty"`
    ApprovedAt  *time.Time    `json:"approved_at,omitempty"`
    RejectReason string       `json:"reject_reason,omitempty"`
}

type GateStatus int

const (
    GatePending  GateStatus = iota
    GateApproved
    GateRejected
    GateExpired
)

// ApprovalGateEngine creates, manages, and resolves approval gates.
type ApprovalGateEngine interface {
    // CreateGate creates a new pending gate for an agent action.
    CreateGate(ctx context.Context, gate *ApprovalGate) error

    // GetGate retrieves a gate by ID.
    GetGate(ctx context.Context, gateID uuid.UUID) (*ApprovalGate, error)

    // ListPendingGates returns all pending gates for a workspace, ordered by creation time ASC.
    ListPendingGates(ctx context.Context, workspaceID uuid.UUID) ([]*ApprovalGate, error)

    // ApproveGate approves a gate. The caller must have role >= editor in the workspace.
    ApproveGate(ctx context.Context, gateID, approverID uuid.UUID) error

    // RejectGate rejects a gate with an optional reason.
    RejectGate(ctx context.Context, gateID, approverID uuid.UUID, reason string) error

    // ExecuteApprovedAction executes the action associated with an approved gate. Returns the result.
    ExecuteApprovedAction(ctx context.Context, gate *ApprovalGate) (result []byte, err error)

    // ExpireStaleGates runs a goroutine that checks for expired gates every 30s.
    ExpireStaleGatesLoop(ctx context.Context, checkInterval time.Duration)
}
```

### 3.5 Error Catalogue

```go
package collaboration

import "errors"

var (
    ErrNotFound           = errors.New("collaboration: not found")
    ErrLockHeld           = errors.New("collaboration: node is locked by another operation")
    ErrNotLockOwner       = errors.New("collaboration: caller does not own this lock")
    ErrPermissionDenied   = errors.New("collaboration: permission denied")
    ErrNotWorkspaceMember = errors.New("collaboration: user is not a member of this workspace")
    ErrInvalidToken       = errors.New("collaboration: invitation token is invalid or expired")
    ErrGateExpired        = errors.New("collaboration: approval gate has expired")
    ErrGateClosed         = errors.New("collaboration: approval gate is not pending")
    ErrDuplicated        = errors.New("collaboration: resource already exists")
    ErrRateLimited       = errors.New("collaboration: rate limit exceeded. Try again later")
)
```

### 3.6 SSE Event Definitions

```go
package collaboration

// CollaborationEventType enumerates all SSE event types for collaboration.
type CollaborationEventType string

const (
    EventUserOnline        CollaborationEventType = "user_online"
    EventUserOffline       CollaborationEventType = "user_offline"
    EventUserTyping        CollaborationEventType = "user_typing"
    EventUserStoppedTyping CollaborationEventType = "user_stopped_typing"
    EventNodeLocked        CollaborationEventType = "node_locked"
    EventNodeUnlocked      CollaborationEventType = "node_unlocked"
    EventGateCreated       CollaborationEventType = "gate_created"
    EventGateApproved      CollaborationEventType = "gate_approved"
    EventGateRejected      CollaborationEventType = "gate_rejected"
    EventGateExpired       CollaborationEventType = "gate_expired"
    EventMemberJoined      CollaborationEventType = "member_joined"
    EventMemberLeft        CollaborationEventType = "member_left"
    EventMemberRoleChanged CollaborationEventType = "member_role_changed"
    EventWorkspaceUpdated  CollaborationEventType = "workspace_updated"
)

// CollaborationEvent is the payload sent over the user's SSE stream.
type CollaborationEvent struct {
    Type        CollaborationEventType `json:"type"`
    WorkspaceID uuid.UUID              `json:"workspace_id"`
    ActorID     uuid.UUID              `json:"actor_id"`
    Timestamp   time.Time              `json:"timestamp"`
    Payload     json.RawMessage        `json:"payload"` // type-specific JSON
}
```

---

## 4. SSE Events for Collaboration

All collaboration events are delivered over the existing SSE stream (defined in SPEC-API-01) but scoped per-user. The server maintains one SSE connection per active user session. Events carry `target_user_id` metadata; the server's event router dispatches only events targeted at the connected user's UUID.

### 4.1 User Online/Offline

```json
// event: user_online
{"type":"user_online","workspace_id":"uuid","actor_id":"uuid","timestamp":"...","payload":{"handle":"alice","role":"editor"}}

// event: user_offline
{"type":"user_offline","workspace_id":"uuid","actor_id":"uuid","timestamp":"...","payload":{"handle":"alice"}}
```

### 4.2 Typing Indicator

```json
// event: user_typing
{"type":"user_typing","workspace_id":"uuid","actor_id":"uuid","timestamp":"...","payload":{"node_id":"uuid","handle":"alice"}}

// event: user_stopped_typing (sent after 2s of no keystroke activity)
{"type":"user_stopped_typing","workspace_id":"uuid","actor_id":"uuid","timestamp":"...","payload":{"node_id":"uuid"}}
```

### 4.3 Node Lock Events

```json
// event: node_locked
{"type":"node_locked","workspace_id":"uuid","actor_id":"uuid","timestamp":"...","payload":{"node_id":"uuid","reason":"Running code execution...","expires_at":"..."}}

// event: node_unlocked
{"type":"node_unlocked","workspace_id":"uuid","actor_id":"uuid","timestamp":"...","payload":{"node_id":"uuid"}}
```

### 4.4 Approval Gate Events

```json
// event: gate_created — sent only to users with role >= editor
{"type":"gate_created","workspace_id":"uuid","actor_id":"uuid","timestamp":"...","payload":{"gate_id":"uuid","action":"node_delete","target_node":"uuid","actor_handle":"agent-bob","expires_at":"..."}}

// event: gate_approved
{"type":"gate_approved","workspace_id":"uuid","actor_id":"uuid","timestamp":"...","payload":{"gate_id":"uuid","approved_by":"uuid","approved_by_handle":"alice"}}

// event: gate_rejected
{"type":"gate_rejected","workspace_id":"uuid","actor_id":"uuid","timestamp":"...","payload":{"gate_id":"uuid","rejected_by":"uuid","reason":"Not now, too risky"}}

// event: gate_expired
{"type":"gate_expired","workspace_id":"uuid","actor_id":"uuid","timestamp":"...","payload":{"gate_id":"uuid"}}
```

---

## 5. API Endpoints

All endpoints are prefixed with `/api/v1/collab/`. Authentication: `Authorization: Bearer <token>` on every request.

### 5.1 Workspace Management

```
GET    /api/v1/collab/workspaces                          → List user's workspaces
POST   /api/v1/collab/workspaces                          → Create workspace
GET    /api/v1/collab/workspaces/{workspace_id}            → Get workspace details
PATCH  /api/v1/collab/workspaces/{workspace_id}            → Update workspace (admin only)
DELETE /api/v1/collab/workspaces/{workspace_id}            → Delete workspace (admin only)
```

**POST /api/v1/collab/workspaces:**
```json
// Request
{"name":"My Project Tree"}
// Response 201
{"workspace_id":"uuid","name":"My Project Tree","role":"admin","members":[{"user_id":"uuid","handle":"alice","role":"admin","joined_at":"..."}]}
```

### 5.2 Membership & Invitations

```
GET    /api/v1/collab/workspaces/{workspace_id}/members          → List members
PATCH  /api/v1/collab/workspaces/{workspace_id}/members/{user_id} → Change role (admin only)
DELETE /api/v1/collab/workspaces/{workspace_id}/members/{user_id} → Remove member (admin only)
POST   /api/v1/collab/workspaces/{workspace_id}/join?token=...   → Join workspace
POST   /api/v1/collab/workspaces/{workspace_id}/invite            → Generate invite link (admin only)
LEAVE  /api/v1/collab/workspaces/{workspace_id}/leave             → Leave workspace
```

### 5.3 Presence

```
POST   /api/v1/collab/workspaces/{workspace_id}/presence          → Ping heartbeat
POST   /api/v1/collab/workspaces/{workspace_id}/typing            → Set typing indicator
DELETE /api/v1/collab/workspaces/{workspace_id}/typing            → Clear typing indicator
GET    /api/v1/collab/workspaces/{workspace_id}/presence           → Get all presence
```

### 5.4 Node Locking

```
POST   /api/v1/collab/nodes/{node_id}/lock    → Acquire lock
DELETE /api/v1/collab/nodes/{node_id}/lock     → Release lock
POST   /api/v1/collab/nodes/{node_id}/lock/renew → Renew lock
GET    /api/v1/collab/workspaces/{workspace_id}/locks → Get all locks
```

### 5.5 Approval Gates

```
GET    /api/v1/collab/workspaces/{workspace_id}/gates              → List pending gates
POST   /api/v1/collab/workspaces/{workspace_id}/gates              → Create gate (agent call only)
GET    /api/v1/collab/workspaces/{workspace_id}/gates/{gate_id}    → Get gate status
POST   /api/v1/collab/workspaces/{workspace_id}/gates/{gate_id}/approve → Approve gate
POST   /api/v1/collab/workspaces/{workspace_id}/gates/{gate_id}/reject  → Reject gate with reason
```

**POST .../gates (Create):**
```json
// Request (agent internal)
{"action":"node_delete","target_node":"uuid","payload":{},"ttl_minutes":5}
// Response 201
{"gate_id":"uuid","status":"pending","expires_at":"..."}
```

**POST .../gates/{id}/approve:**
```json
// Response 200
{"gate_id":"uuid","status":"approved","approved_by":"uuid","action":"node_delete","result":{"deleted":true,"node_id":"uuid"}}
```

---

## 6. Data Model Extensions

### 6.1 Workspace Table (PostgreSQL)

```sql
CREATE TABLE workspaces (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id     UUID NOT NULL REFERENCES profiles(id) ON DELETE RESTRICT,
    name         TEXT NOT NULL CHECK (char_length(name) >= 1 AND char_length(name) <= 128),
    tree_id      UUID NOT NULL REFERENCES trees(id) ON DELETE CASCADE,
    approval_ttl BIGINT NOT NULL DEFAULT 300, -- seconds
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE workspace_members (
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id      UUID NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    role         INT NOT NULL DEFAULT 1, -- 0=viewer, 1=editor, 2=admin
    joined_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, user_id)
);

CREATE TABLE invitations (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    created_by   UUID NOT NULL REFERENCES profiles(id),
    token_hash   TEXT NOT NULL UNIQUE,
    expires_at   TIMESTAMPTZ NOT NULL,
    used         BOOLEAN NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### 6.2 Node Locks Table

```sql
CREATE TABLE node_locks (
    node_id      UUID PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    holder_id    UUID NOT NULL REFERENCES profiles(id),
    reason       TEXT NOT NULL,
    acquired_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL
);
```

### 6.3 Approval Gates Table

```sql
CREATE TABLE approval_gates (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    action       TEXT NOT NULL CHECK (action IN ('node_delete','node_modify_bulk','tree_rename','user_invite','agent_spawn')),
    actor_id     UUID NOT NULL REFERENCES profiles(id),
    target_node  UUID REFERENCES nodes(id) ON DELETE SET NULL,
    payload      JSONB NOT NULL DEFAULT '{}',
    status       INT NOT NULL DEFAULT 0, -- 0=pending, 1=approved, 2=rejected, 3=expired
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    approved_by  UUID REFERENCES profiles(id),
    approved_at  TIMESTAMPTZ,
    reject_reason TEXT
);

CREATE INDEX idx_approval_gates_workspace_pending ON approval_gates(workspace_id, status) WHERE status = 0;
CREATE INDEX idx_approval_gates_expires ON approval_gates(expires_at) WHERE status = 0;
```

### 6.4 Offline Event Queue

```sql
CREATE TABLE offline_events (
    id           BIGSERIAL PRIMARY KEY,
    user_id      UUID NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    event_type   TEXT NOT NULL,
    payload      JSONB NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered    BOOLEAN NOT NULL DEFAULT false
);

CREATE INDEX idx_offline_events_undelivered ON offline_events(user_id, id) WHERE delivered = false;
```

---

## 7. Edge Cases

| # | Edge Case | Handling |
|---|-----------|----------|
| 1 | User disconnects mid-approval | Gate remains `pending`. The agent action blocks until approval or expiry. On reconnect, the user's SSE stream replays the pending gate event. |
| 2 | All editors offline when agent requests approval | Gate remains pending for the full TTL (configurable, default 5m). After expiry, the gate auto-rejects and the agent receives `ErrGateExpired`. |
| 3 | Two agents request approval on the same node simultaneously | Each creates a separate gate with its own ID. The first approved gate's action executes; the second gate finds the state changed and its action may produce a conflict, which returns a conflict error to the agent. |
| 4 | User edits a node while a gate for modifying it is pending | The gate resolves against the state at the time of approval. If the human edit changed the same fields, the agent's approved action receives a conflict response. |
| 5 | Token expires mid-session | The server sends `session_expired` SSE event. The client must re-authenticate. Pending gates are preserved and reassociated on reconnect. |
| 6 | Workspace deleted while members are connected | Each member's SSE stream receives `workspace_deleted` event. The collaboration service is torn down for that workspace ID. |
| 7 | Heartbeat fails for 90s | PresenceTracker.MarkOffline is called. `user_offline` event broadcast. If the user reconnects within the offline backlog retention period, missed events are replayed. |
| 8 | Invitation token used twice | The `invitations.used` flag prevents reuse. The second attempt returns 409 Conflict. |
| 9 | Rate limit exceeded | The endpoint returns 429 Too Many Requests with `Retry-After` header. The response body contains the reset timestamp. |
| 10 | Agent crashes while holding a lock | The lock's TTL (30s) expires automatically. `NodeLockManager.LockCleanupLoop` releases it. No manual recovery needed. |

---

## 8. Test Scenarios

| # | Scenario | Verification |
|---|----------|-------------|
| 1 | User creates a workspace, invites a second user via link, second user joins | Assert: workspace has 2 members, both can see the tree |
| 2 | Viewer tries to delete a node | Assert: 403 Forbidden |
| 3 | Editor changes another editor's role | Assert: 403 Forbidden (only admin can change roles) |
| 4 | Agent acquires a node lock, user tries to edit the locked node | Assert: node returns 423 Locked. User sees `node_locked` SSE event. |
| 5 | Lock expires while agent is idle | Assert: lock auto-released after 30s. User can edit again. `node_unlocked` SSE event sent. |
| 6 | Agent creates an approval gate for `node_delete` | Assert: gate has `status: pending` |
| 7 | Authorised user approves the gate | Assert: gate transitions to `approved`. Action executes. SSE `gate_approved` event sent. |
| 8 | Authorised user rejects the gate with reason | Assert: gate transitions to `rejected`. Reason stored. `gate_rejected` event sent. |
| 9 | Gate expires without action | Assert: after TTL, gate transitions to `expired`. Agent receives error on callback. |
| 10 | User reconnects after 5 minutes offline | Assert: offline events replayed. Missed `gate_created` event arrives. |
| 11 | Two users type in the same node simultaneously | Assert: both see `user_typing` events with the other user's handle. No data loss. |
| 12 | Viewer attempts to approve a gate | Assert: 403 Forbidden. Viewer role is not in the authorised approval set. |
| 13 | Max concurrent gates per workspace (burst test) | Assert: 100 gates created in rapid succession; no data corruption. |
| 14 | Agent spawns, auto-acquires approval, editor approves within 2s | Assert: gate lifecycle completes in <3s. |

---

## 9. Security Considerations

| Concern | Mitigation |
|---------|------------|
| Session token theft | Tokens are opaque, long (64 bytes), and random. Server validates HMAC signature. Token leaks are mitigated by 24h expiry and optional per-workspace token revocation. |
| Unauthorised workspace access | Every API endpoint checks `x-collab-workspace-id` header against the user's membership. No unauthenticated workspace access. |
| Invitation link interception | Tokens are one-time use, expire in 48h, and are scoped to a specific workspace. The workspace owner can regenerate or revoke all outstanding invitations. |
| Lock starvation | An agent that renews its lock indefinitely blocks edits. The 30s TTL with a max 3 renewals (90s total) prevents indefinite locking. For longer operations, the agent must checkpoint and release. |
| Approval gate action injection | The `payload` in a gate is a JSON blob; the approval engine validates that the payload matches the declared `action` type before execution. `node_delete` payload must contain `node_id`; `user_invite` must contain `handle`. |
| Rate limit bypass | Rate limits are enforced per-user-token, not per-IP. A single user with a valid token cannot bypass by rotating IP addresses. |
| SSE injection | SSE event payloads are server-generated JSON. User-supplied text (handle, display name) is escaped. SSE data lines are never concatenated with user input. |
| Offline event queue overflow | Capped at 10,000 events per user. Oldest events are pruned first. The client receives a `queue_overflow` event if events were dropped. |
| Workspace admin abuse | Admin actions are logged in the audit trail (SPEC-DM-03). Removing all admins from a workspace is blocked (at least one admin must remain). |
| Cross-workspace data leakage | Workspace isolation is enforced at the database level: `workspace_id` is a column on every collaboration table and is always part of the query WHERE clause. |

---

## 10. Implementation Phasing

| Phase | Components | Estimated Effort |
|-------|-----------|-----------------|
| P1 — Core | Workspace CRUD, membership, session tokens, SSE routing | 3-5 days (backend) |
| P2 — Presence | Heartbeat loop, presence tracker, typing indicators, presence UI overlay | 2-3 days (backend + frontend) |
| P3 — Locks | Node lock manager, lock cleanup loop, lock UI indicators | 1-2 days (backend + frontend) |
| P4 — Approval Gates | Gate engine, approve/reject/expire lifecycle, approval panel, agent callback | 4-6 days (backend + frontend + agent integration) |
| P5 — Offline Queue | Event queue, replay on reconnect, queue overflow handling | 2-3 days (backend + frontend) |
| P6 — Hardening | Rate limiting, audit trail integration, permission tests, chaos testing | 2-3 days |

**Total estimated: 14-22 days** for a complete collaboration implementation across all layers.

---

## 11. Cross-References

| Reference | Relevance |
|-----------|-----------|
| SPEC-API-05 (§2, §4) | Existing approval endpoint spec; SPEC-FTR-01 extends this to multi-user with gate lifecycle |
| SPEC-API-06 (§2, §3) | Multi-user & profile endpoints; SPEC-FTR-01 ties profile IDs to workspace membership |
| SPEC-DM-03 (§4) | Audit trail DDL; approval gate decisions log here |
| SPEC-DM-04 (§2) | User & profile model; SPEC-FTR-01 workspace membership references profile IDs |
| SPEC-API-01 (§3) | SSE event stream; collaboration events ride on the same SSE transport |
| SPEC-API-04 (§3.2) | Merge endpoints; approval gates trigger merge logic after approval |
| SPEC-TM-03 (§5) | Topic search; shared workspace topics are visible to all members |
| ARCHITECTURE.md §4 | Post-MVP deferred features section; this spec delivers the first deferred feature |
| AGENTS.md §Deferred | "Multi-user collaboration, approval gates" — this spec is the architectural foundation |
