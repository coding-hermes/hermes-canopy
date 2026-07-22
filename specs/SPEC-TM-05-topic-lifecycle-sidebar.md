# SPEC-TM-05 — Topic Lifecycle & Sidebar

> **Status:** Spec | **Blocks:** FE-04 (Topic Sidebar), FE-07 (Context Menu), CLIP-09 (Keyboard Shortcuts)
> **References:** SPEC-TM-01, SPEC-TM-02, SPEC-TM-03, SPEC-TM-04, SPEC-DM-01, SPEC-API-01, SPEC-API-02, ARCHITECTURE.md §3, ARCHITECTURE.md §5

---

## 1. Purpose

Define Canopy's topic lifecycle management and sidebar UI: the sidebar widget showing active and archived topics, per-topic context menus, topic previews on hover, reordering controls, and keyboard shortcuts for rapid topic navigation. A TypeScript/Preact worker reading this spec must implement the `TopicSidebar` component, `TopicContextMenu`, `TopicPreview` tooltip, drag-and-drop ordering, and all keyboard shortcut handlers without clarifying questions. A Go worker reading this spec must implement `TopicLifecycleService` for topic rename/archive/delete/merge/split operations and their SSE event hooks.

The sidebar is the persistent navigational control surface for topics. It transforms the topic overlay defined in SPEC-TM-01 from a data concept into an interactive, always-visible panel where users organize, discover, and act on topics.

---

## 2. Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Sidebar position | Fixed left-side panel, 320px wide, collapsible | Left-side is the convention for IDE navigators (VS Code, Sublime); consistent with tree graph occupying main viewport |
| Default state | Open (visible) on desktop; collapsed on narrow mobile | Desktop has space; mobile needs maximum content area |
| Topic ordering | Default: last_active_at DESC. Sortable by title, created_at, node_count | Users organize differently per context; default to recency matching most frequent use case |
| Archive UX | Collapsed section below active topics with count badge | Common inbox/archive pattern (Gmail, Linear); archive is secondary but accessible |
| Context menu | Right-click on topic card or ⋯ icon | Dual-access model — power users right-click, new users use the icon |
| Topic preview | Inline card rendered on hover (150ms delay) | Instant enough for fluid scanning; long delay prevents accidental triggers during mouse traversal |
| Reordering | Drag-and-drop via HTML5 DnD API | Zero external dependency; Preact's onDragStart/onDragEnd handle state |
| Keyboard shortcuts | Global listener on `document`, scoped by active-panel detection | Shortcuts work from anywhere but only when sidebar or tree is focused |
| SSE events | `topic_renamed`, `topic_archived`, `topic_restored`, `topic_deleted`, `topic_merged`, `topic_split`, `topic_reordered` | Every lifecycle action produces an event that other clients (and the context compiler) can observe |
| Optimistic updates | All sidebar mutations apply locally first, reverted on SSE error | Keeps UI responsive; error state shows inline with "Retry" action on the failed topic card |
| Persistence of ordering | `topics.display_order` column on topics table | POSITION-style column; server assigns gaps (1000 increments) on reorder for collision-free concurrent edits |

---

## 3. PostgreSQL DDL

### 3.1 Display Order Column

```sql
-- 000050_topic_display_order.up.sql

-- Add display_order to topics table for user-customized sidebar ordering.
-- Uses large-gap sequence: 1000, 2000, 3000... for collision-free concurrent insertion.
-- Default sort order for display_order is ASC within each status section.
ALTER TABLE topics
    ADD COLUMN display_order integer NOT NULL DEFAULT 0;

CREATE INDEX idx_topics_tree_display_order
    ON topics(tree_id, status, display_order ASC);
```

### 3.2 Topic Lifecycle Log Table

```sql
-- 000051_topic_lifecycle_log.up.sql

-- Immutable log of all topic lifecycle events for audit trail and undo support.
CREATE TABLE topic_lifecycle_log (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    topic_id        uuid        NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    tree_id         uuid        NOT NULL,
    event_type      text        NOT NULL,  -- 'renamed' | 'archived' | 'restored' | 'deleted' | 'merged' | 'split' | 'reordered'
    previous_state  jsonb,                 -- Snapshot of topic row before the event (for undo)
    new_state       jsonb,                 -- Snapshot of topic row after the event
    performed_by    uuid        NOT NULL REFERENCES profiles(id) ON DELETE SET NULL,
    metadata        jsonb       NOT NULL DEFAULT '{}',  -- e.g., {old_title: "...", new_title: "...", source_topic_id: "...", target_topic_id: "..."}
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX idx_tll_topic_id    ON topic_lifecycle_log(topic_id);
CREATE INDEX idx_tll_tree_id     ON topic_lifecycle_log(tree_id);
CREATE INDEX idx_tll_event_type  ON topic_lifecycle_log(event_type);
CREATE INDEX idx_tll_created_at  ON topic_lifecycle_log(tree_id, created_at DESC);
```

### 3.3 Down Migrations

```sql
-- 000050_topic_display_order.down.sql
ALTER TABLE topics DROP COLUMN IF EXISTS display_order;

-- 000051_topic_lifecycle_log.down.sql
DROP TABLE IF EXISTS topic_lifecycle_log;
```

---

## 4. Go Interfaces

### 4.1 TopicLifecycleService

```go
package topic

import (
    "context"
    "time"
)

// TopicLifecycleEvent represents an SSE event for a topic lifecycle action.
type TopicLifecycleEvent struct {
    EventType string `json:"event_type"` // "topic_renamed" | "topic_archived" | "topic_restored" | "topic_deleted" | "topic_merged" | "topic_split" | "topic_reordered"
    TopicID   string `json:"topic_id"`
    TreeID    string `json:"tree_id"`
    Metadata  map[string]any `json:"metadata,omitempty"`
    Timestamp time.Time `json:"timestamp"`
}

// RenameTopicInput carries the request to rename a topic.
type RenameTopicInput struct {
    TopicID     string `json:"topic_id"`
    NewTitle    string `json:"new_title"`
    PerformedBy string `json:"performed_by"`
}

// RenameTopicOutput carries the result of a rename.
type RenameTopicOutput struct {
    Topic       *Topic             `json:"topic"`
    OldSlug     string             `json:"old_slug"`
    NewSlug     string             `json:"new_slug"`
    SSEEvent    TopicLifecycleEvent `json:"sse_event"`
}

// ArchiveTopicInput carries the request to archive or restore a topic.
type ArchiveTopicInput struct {
    TopicID     string `json:"topic_id"`
    PerformedBy string `json:"performed_by"`
}

// ArchiveTopicOutput carries the result of an archive/restore.
type ArchiveTopicOutput struct {
    Topic    *Topic             `json:"topic"`
    SSEEvent TopicLifecycleEvent `json:"sse_event"`
}

// DeleteTopicInput carries the request to soft-delete a topic.
type DeleteTopicInput struct {
    TopicID     string `json:"topic_id"`
    PerformedBy string `json:"performed_by"`
}

// DeleteTopicOutput carries the result of a soft-delete.
type DeleteTopicOutput struct {
    DeletedAt time.Time          `json:"deleted_at"`
    SSEEvent  TopicLifecycleEvent `json:"sse_event"`
}

// MergeTopicsInput carries the request to merge source topic into target topic.
type MergeTopicsInput struct {
    SourceTopicID string `json:"source_topic_id"`
    TargetTopicID string `json:"target_topic_id"`
    MergeStrategy string `json:"merge_strategy"` // "append" | "overwrite" | "interleave"
    PerformedBy   string `json:"performed_by"`
}

// MergeTopicsOutput carries the result of a merge.
type MergeTopicsOutput struct {
    TargetTopic *Topic             `json:"target_topic"`
    SourceTopic *Topic             `json:"source_topic"` // Now status = "deleted"
    SSEEvent    TopicLifecycleEvent `json:"sse_event"`
}

// SplitTopicInput carries the request to split a topic from a node within its scope.
type SplitTopicInput struct {
    ParentTopicID string `json:"parent_topic_id"`
    RootNodeID    string `json:"root_node_id"`
    NewTitle      string `json:"new_title"`
    PerformedBy   string `json:"performed_by"`
}

// SplitTopicOutput carries the result of a split.
type SplitTopicOutput struct {
    ParentTopic *Topic             `json:"parent_topic"`
    ChildTopic  *Topic             `json:"child_topic"`
    SSEEvent    TopicLifecycleEvent `json:"sse_event"`
}

// ReorderTopicsInput carries the request to reorder topics in the sidebar.
type ReorderTopicsInput struct {
    TreeID      string              `json:"tree_id"`
    TopicOrder  []TopicOrderEntry   `json:"topic_order"`     // Complete ordered list of topic IDs
    PerformedBy string              `json:"performed_by"`
}

// TopicOrderEntry represents a single topic's position in the sidebar order.
type TopicOrderEntry struct {
    TopicID string `json:"topic_id"`
    Position int   `json:"position"`      // 0-based display position
    Status   string `json:"status"`       // "active" or "archived"
}

// ReorderTopicsOutput carries the result of a reorder.
type ReorderTopicsOutput struct {
    UpdatedTopics []*Topic          `json:"updated_topics"`
    SSEEvent      TopicLifecycleEvent `json:"sse_event"`
}

// TopicLifecycleService defines all topic lifecycle operations.
type TopicLifecycleService interface {
    // Rename changes a topic's title. Regenerates the slug.
    // Returns error if new title duplicates an existing topic's title in the same tree.
    Rename(ctx context.Context, input RenameTopicInput) (*RenameTopicOutput, error)

    // Archive sets a topic's status to "archived". Node content is preserved.
    Archive(ctx context.Context, input ArchiveTopicInput) (*ArchiveTopicOutput, error)

    // Restore sets a topic's status back to "active". Clears archived_at.
    Restore(ctx context.Context, input ArchiveTopicInput) (*ArchiveTopicOutput, error)

    // SoftDelete sets deleted_at. Topic no longer appears in sidebar or search results.
    SoftDelete(ctx context.Context, input DeleteTopicInput) (*DeleteTopicOutput, error)

    // HardDelete permanently removes the topic row. Only allowed if topic has no child topics.
    // Use SoftDelete for general use; HardDelete is for admin/cleanup only.
    HardDelete(ctx context.Context, topicID string) error

    // Merge combines the source topic's scope into the target topic.
    // Source topic is soft-deleted after merge. Merge strategy determines how nodes from both
    // scopes are combined:
    //   "append" — both scopes combined; union of all nodes (default)
    //   "interleave" — node order interleaved by created_at
    // Returns error if merge would create a circular parent-child dependency.
    Merge(ctx context.Context, input MergeTopicsInput) (*MergeTopicsOutput, error)

    // Split creates a child topic rooted at the specified node within the parent's scope.
    // The child topic's scope includes the root node and all descendants.
    Split(ctx context.Context, input SplitTopicInput) (*SplitTopicOutput, error)

    // Reorder updates the display_order column for all topics in a tree.
    // Accepts the complete ordered list — servers assigns 1000-gap positions.
    Reorder(ctx context.Context, input ReorderTopicsInput) (*ReorderTopicsOutput, error)
}
```

### 4.2 TopicSidebarData (Response for Sidebar)

```go
package topic

// TopicSidebarData is the full server-side data payload for the sidebar. Returned
// by the TopicService when the sidebar is opened or refreshed. Clients cache this
// and update via SSE events.
type TopicSidebarData struct {
    TreeID      string           `json:"tree_id"`
    ActiveCount int              `json:"active_count"`
    ArchiveCount int             `json:"archive_count"`
    ActiveTopics []TopicCard     `json:"active_topics"`   // Ordered by display_order ASC
    ArchivedTopics []TopicCard   `json:"archived_topics"` // Ordered by archived_at DESC
    SortBy      string           `json:"sort_by"`          // Current sort preference: "last_active" | "title" | "created" | "node_count"
}

// TopicCard is the sidebar representation of a single topic.
type TopicCard struct {
    ID              string    `json:"id"`
    Title           string    `json:"title"`
    Slug            string    `json:"slug"`
    Status          string    `json:"status"`       // "active" | "archived"
    NodeCount       int       `json:"node_count"`
    PreviewSnippet  string    `json:"preview_snippet"`    // First ~150 chars of newest node content
    PreviewNodes    int       `json:"preview_nodes"`      // How many nodes in preview (default 3)
    LastActiveAt    time.Time `json:"last_active_at"`
    ArchivedAt      *time.Time `json:"archived_at,omitempty"`
    RefCount        int       `json:"ref_count"`          // Number of #references pointing to this topic
    ParticipantCount int      `json:"participant_count"`
}
```

---

## 5. TypeScript Types

```typescript
// ── Topic Sidebar Data ─────────────────────────────────────────

interface TopicSidebarData {
  treeId: string;
  activeCount: number;
  archiveCount: number;
  activeTopics: TopicCard[];
  archivedTopics: TopicCard[];
  sortBy: 'last_active' | 'title' | 'created' | 'node_count';
}

interface TopicCard {
  id: string;
  title: string;
  slug: string;
  status: 'active' | 'archived';
  nodeCount: number;
  previewSnippet: string;
  previewNodes: number;
  lastActiveAt: string;        // ISO 8601
  archivedAt?: string;
  refCount: number;
  participantCount: number;
}

// ── Sidebar State ─────────────────────────────────────────────

type SidebarPanel = 'sidebar' | 'search' | 'closed';

interface SidebarState {
  panel: SidebarPanel;
  width: number;               // Default 320px, user-resizable
  collapsed: boolean;
  sortBy: 'last_active' | 'title' | 'created' | 'node_count';
  expandedArchived: boolean;   // Whether the Archived section is expanded
}

// ── Context Menu Actions ──────────────────────────────────────

type TopicContextAction =
  | { type: 'rename'; topicId: string }
  | { type: 'archive'; topicId: string }
  | { type: 'restore'; topicId: string }
  | { type: 'delete'; topicId: string }
  | { type: 'merge'; topicId: string }
  | { type: 'split'; topicId: string; fromNodeId: string }
  | { type: 'reorder'; topicId: string; newPosition: number };

// ── Keyboard Shortcut Configuration ───────────────────────────

interface KeyboardShortcuts {
  'Ctrl+K': 'open_search';
  'Ctrl+Shift+N': 'new_topic';
  'Ctrl+Shift+A': 'archive_selected';
  'Ctrl+Shift+R': 'rename_selected';
  'Delete': 'delete_selected';
  'Escape': 'close_sidebar_or_menu';
  'Alt+Up': 'move_topic_up';
  'Alt+Down': 'move_topic_down';
}

// ── SSE Event Types (frontend consumption) ────────────────────

interface TopicLifecycleSSEEvent {
  event_type:
    | 'topic_renamed'
    | 'topic_archived'
    | 'topic_restored'
    | 'topic_deleted'
    | 'topic_merged'
    | 'topic_split'
    | 'topic_reordered';
  topic_id: string;
  tree_id: string;
  metadata?: Record<string, unknown>;
  timestamp: string;
}

// ── Zod Validation ────────────────────────────────────────────

import { z } from 'zod';

const TopicCardSchema = z.object({
  id: z.string().uuid(),
  title: z.string().min(1).max(200),
  slug: z.string().regex(/^[a-z]([a-z0-9-]*[a-z0-9])?$/),
  status: z.enum(['active', 'archived']),
  nodeCount: z.number().int().nonnegative(),
  previewSnippet: z.string(),
  previewNodes: z.number().int().min(1).max(20).default(3),
  lastActiveAt: z.string().datetime(),
  archivedAt: z.string().datetime().optional(),
  refCount: z.number().int().nonnegative(),
  participantCount: z.number().int().nonnegative(),
});

const TopicSidebarDataSchema = z.object({
  treeId: z.string().uuid(),
  activeCount: z.number().int().nonnegative(),
  archiveCount: z.number().int().nonnegative(),
  activeTopics: z.array(TopicCardSchema),
  archivedTopics: z.array(TopicCardSchema),
  sortBy: z.enum(['last_active', 'title', 'created', 'node_count']),
});
```

---

## 6. Sidebar Layout & Behavior

### 6.1 Structure

```
┌─────────────────────────────────┐
│ ☰ Topics (Active: 8)  ⋮ Sort ▼ │  ← Header row
├─────────────────────────────────┤
│ 🔍 Search topics...             │  ← Search bar (Ctrl+K)
├─────────────────────────────────┤
│                                 │
│ ● ● #database-schema   12:34 PM │  ← Active topic card
│   Schema discussion, 45 nodes   │
│                                 │
│ ● #api-design           11:20 AM│
│   REST endpoints, 23 nodes      │
│                                 │
│ ● #data-flow           10:15 AM │
│   Batch processing, 8 nodes     │
│                                 │
│ ...                             │
│                                 │
│ ─── Archived (3) ───    [▼]    │  ← Collapsible archived section
│                                 │
│ ○ #old-notes          Jul 15    │  ← Archived topic card (dimmed)
│   Archived, 12 nodes            │
│                                 │
│ ○ #prototype          Jul 10   │  ← Archived topic card
│   Archived, 5 nodes             │
│                                 │
└─────────────────────────────────┘
```

### 6.2 Active Topic Cards

Each active topic card shows:

| Element | Content | Behavior |
|---------|---------|----------|
| Icon | ● (filled circle, theme color) | Indicates active status |
| **Title** | Topic title as clickable link | Click → navigates to topic root node in tree view |
| **Time** | `last_active_at` formatted as relative time | "12:34 PM" (today), "Yesterday", "Jul 15" |
| Subtitle | Preview snippet (first ~150 chars of newest node) | Truncated with ellipsis; no hover interaction (tooltip provides full info) |
| Node count badge | Small pill showing `N nodes` | Visual cue for topic size |
| Ref count badge | Small pill showing `N refs` | Only visible when > 0 |

### 6.3 Archived Topic Cards

Same structure as active cards but:
- Icon: ○ (hollow circle)
- Title: dimmed to 60% opacity
- Subtitle: shows "Archived N days ago" instead of preview snippet
- Time column shows archive date
- Click on title: navigates to topic root with a yellow banner: "This topic is archived. [Restore]"
- Archived topics are excluded from `#reference` autocomplete (references resolve but show a "referenced topic is archived" tooltip)

### 6.4 Drag-and-Drop Reordering

| Element | Action | Result |
|---------|--------|--------|
| Topic card | Drag | Ghost preview follows cursor |
| Drop on another card | Drop | Topics swap positions |
| Drop between cards | Drop | Topic inserted at position |
| Drop into Archived section | Drop | Topic archived |
| Drop out of Archived section | Drop | Topic restored to "active" status |
| Drag cancel (Escape) | Press Escape | Reverts to original order |

**State management:**
1. On `dragstart`: record original indices of all cards
2. On `drop`: optimistically reorder local state, send `POST /topics/reorder` with complete ordered list
3. On SSE `topic_reordered` error: revert to original order, show inline "Reorder failed — [Retry]" on affected topic card
4. On dragend without drop: no-op (cancelled)

### 6.5 Sort Controls

| Sort Mode | Behavior |
|-----------|----------|
| `last_active` (default) | Topics sorted by `last_active_at` DESC; most recently active at top |
| `title` | Topics sorted alphabetically by title (case-insensitive) |
| `created` | Topics sorted by `created_at` DESC; newest first |
| `node_count` | Topics sorted by `node_count` DESC; largest first |

Click dropdown arrow next to "Topics" header → sort menu appears → selection persists per tree (stored in `topic_sidebar_prefs` local storage).

Sort mode change re-fetches sidebar data from server. Custom drag-and-drop ordering is cleared when sort mode is changed (with confirmation dialog: "Custom order will be discarded. Continue?").

---

## 7. Context Menu

### 7.1 Trigger

| Input | Target |
|-------|--------|
| Right-click | Any topic card in sidebar |
| ⋯ icon click | Topic card subtitle area |
| Keyboard | Select topic → Context Menu key or Shift+F10 |

### 7.2 Menu Items

| Action | Icon | Behavior |
|--------|------|----------|
| **Rename** | ✏️ | Opens inline text field on topic title. Enter to confirm, Escape to cancel. Validates title length (1–200 chars). On success: SSE `topic_renamed`. |
| **Archive** | 📦 | Soft-archive topic. Confirmation: "Archive '#topic-title'? It will be moved to the Archived section." On confirm: SSE `topic_archived`. |
| **Restore** | ↩️ | (Archived topics only) Restores to active status. No confirmation needed. On success: SSE `topic_restored`. |
| **Delete** | 🗑️ | Opens confirmation dialog: "Delete '#topic-title'? This will remove the topic from the sidebar. The underlying messages remain in the tree. This action can be undone by restoring from the lifecycle log." On confirm: SSE `topic_deleted`. |
| **Merge into...** | 🔀 | Opens topic selector (search/typeahead for other topics in tree). Select target → confirmation: "Merge '#source' into '#target'? Nodes from both topics will be combined." On confirm: SSE `topic_merged`. |
| **Split from here** | ✂️ | (Active topic only) Creates a child topic at the selected root node. Opens inline title input. On confirm: SSE `topic_split`. |
| **Copy slug** | 📋 | Copies `#topic-slug` to clipboard. Shows brief "Copied!" toast. |

### 7.3 Context Menu State Machine

```
closed ──(right-click / ⋯ click)──> open
   │                                      │
   │<──(click outside / Escape)───────────│
   │                                      │
   │<──(action selected)──> action_dialog
                                │
                                │<──(confirm / cancel)──> closed
```

### 7.4 Rename Inline Edit

When "Rename" is selected from the context menu:

```
Before:  ● #database-schema        12:34 PM
          Schema discussion, 45 nodes

During:  ● [📝 database-schema________] [✓] [✕]
          Schema discussion, 45 nodes

After:   ● #db-schema              12:35 PM
          Schema discussion, 45 nodes

On Error: ● [#database-schema] (!) Rename failed — [Retry]
           Schema discussion, 45 nodes
```

- Title becomes an input field pre-filled with current title
- Enter or ✓ confirms; Escape or ✕ cancels
- On confirm: sends `POST /topics/{id}/rename` with new title
- Server generates new slug; returns updated topic card
- Server emits SSE `topic_renamed` with `{old_title, new_title, old_slug, new_slug}`
- On error: show inline error with Retry button; title reverts

---

## 8. Topic Preview (Hover Tooltip)

### 8.1 Trigger

Hover over a topic card title for 150ms (debounced). Cancel if mouse leaves before 150ms.

### 8.2 Preview Card Content

```
┌─────────────────────────────────────┐
│ #database-schema           ● Active │
│ Created Jul 20 • 45 nodes • 3 refs  │
│ 2 participants                      │
├─────────────────────────────────────┤
│ 📄 "We decided to use UUIDv7 for    │
│    topic IDs because..."            │  ← Newest node content
│                                     │
│ 📄 "Added indexes for tree_id,      │
│    status, and slug lookups."       │  ← Second newest node
│                                     │
│ 📄 "Topic status lifecycle: active, │
│    archived, deleted (soft)."       │  ← Third newest node
│                                     │
│               [Show all 45 nodes →] │  ← Click to navigate
└─────────────────────────────────────┘
```

### 8.3 Preview Sections

| Section | Content | Source |
|---------|---------|--------|
| Header | Slug + status badge | `Topic.title`, `Topic.status` |
| Metadata | Created date, node count, ref count | `Topic`, aggregated |
| Participants | Count of unique authors within scope | Aggregated from nodes |
| Preview nodes | Up to 3 newest node content snippets (first ~200 chars each) | `TopicService.GetPreview(treeID, topicID, 3)` |
| Footer navigation | "Show all N nodes →" | Always visible; clicking navigates to topic root in tree |

### 8.4 Data Source

Preview data is fetched lazily when hover triggers, not preloaded:
- **Fast path:** If the sidebar already loaded the tree's full topic list (via `TopicSidebarData`), the preview uses cached `previewSnippet` and `previewNodes` fields
- **Slow path:** If cached data is older than 30s, `GET /topics/{topic_id}/preview` fetches fresh preview nodes
- **Error state:** Preview card shows "Could not load preview" with Retry link

---

## 9. Keyboard Shortcuts

### 9.1 Shortcut Table

| Shortcut | Action | Scope | Notes |
|----------|--------|-------|-------|
| `Ctrl+K` | Open topic search sidebar | Global | Focuses search input; same as SPEC-TM-03 search |
| `Ctrl+Shift+N` | Create new topic from selected node | Sidebar or tree | Opens inline creation dialog |
| `Ctrl+Shift+A` | Archive selected topic | Sidebar (when topic focused) | Quick archive without context menu |
| `Ctrl+Shift+R` | Rename selected topic | Sidebar (when topic focused) | Enters inline rename mode |
| `Delete` | Delete selected topic | Sidebar (when topic focused) | Shows confirmation dialog |
| `Escape` | Close sidebar / close context menu / cancel rename | Global | Closes highest-priority open element |
| `Alt+↑` / `Alt+↓` | Move selected topic up/down in order | Sidebar | Nudges topic one position (only in custom sort mode) |
| `Ctrl+Tab` / `Ctrl+Shift+Tab` | Navigate between topics | Sidebar | Cycles through active topics in sidebar order |
| `Enter` | Navigate to selected topic | Sidebar | Opens topic root in tree view |
| `Space` | Toggle Archived section expand | Sidebar | Expands/collapses archived topic list |

### 9.2 Scope Detection

Shortcuts are scoped to prevent conflicts:

| When this is open | Shortcuts that work | Conflicts suppressed |
|-------------------|-------------------|---------------------|
| Sidebar focused | All sidebar shortcuts | — |
| Context menu open | Escape, action keys (Enter to select) | All others |
| Rename inline edit | Enter (confirm), Escape (cancel) | All others (no Ctrl+K, etc.) |
| Confirmation dialog | Enter (confirm), Escape (cancel), Tab (focus buttons) | All others |
| Tree composer focused | Only Ctrl+K (search) and Ctrl+Shift+N (new topic) | Alt+↑↓, Delete, Ctrl+Shift+A/R |
| Search sidebar open | All sidebar shortcuts + Ctrl+K toggles back | — |

### 9.3 Preact Implementation Guidance

```typescript
// Keyboard shortcuts should use a composable hook pattern:
function useKeyboardShortcuts(handlers: Map<string, () => void>, scope: string) {
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      const key = [
        e.ctrlKey && 'Ctrl',
        e.shiftKey && 'Shift',
        e.altKey && 'Alt',
        e.key,
      ].filter(Boolean).join('+');

      if (handlers.has(key)) {
        // Check scope: don't fire if modal/dialog/inline-edit is open
        if (isBlockedByActiveElement()) return;
        e.preventDefault();
        handlers.get(key)!();
      }
    };
    document.addEventListener('keydown', handler);
    return () => document.removeEventListener('keydown', handler);
  }, [handlers, scope]);
}
```

---

## 10. API Endpoints

### 10.1 Topic Lifecycle Endpoints

| Method | Path | Input | Output | SSE Event |
|--------|------|-------|--------|-----------|
| `PATCH` | `/topics/{topic_id}/rename` | `RenameTopicInput` | `RenameTopicOutput` | `topic_renamed` |
| `POST` | `/topics/{topic_id}/archive` | `ArchiveTopicInput` | `ArchiveTopicOutput` | `topic_archived` |
| `POST` | `/topics/{topic_id}/restore` | `ArchiveTopicInput` | `ArchiveTopicOutput` | `topic_restored` |
| `DELETE` | `/topics/{topic_id}` | `DeleteTopicInput` (query) | `DeleteTopicOutput` | `topic_deleted` |
| `POST` | `/topics/merge` | `MergeTopicsInput` | `MergeTopicsOutput` | `topic_merged` |
| `POST` | `/topics/split` | `SplitTopicInput` | `SplitTopicOutput` | `topic_split` |
| `PUT` | `/topics/reorder` | `ReorderTopicsInput` | `ReorderTopicsOutput` | `topic_reordered` |

### 10.2 Sidebar Data Endpoint

| Method | Path | Query Params | Response | Description |
|--------|------|-------------|----------|-------------|
| `GET` | `/trees/{tree_id}/sidebar` | `sort_by=last_active`, `include_archived=true` | `TopicSidebarData` | Full sidebar data payload for initial render |

### 10.3 Preview Endpoint

| Method | Path | Query Params | Response | Description |
|--------|------|-------------|----------|-------------|
| `GET` | `/topics/{topic_id}/preview` | `max_nodes=3` | `TopicPreview` | Preview data for hover tooltip |

```go
// TopicPreview is the response for the preview hover endpoint.
type TopicPreview struct {
    TopicID       string            `json:"topic_id"`
    Title         string            `json:"title"`
    Slug          string            `json:"slug"`
    Status        string            `json:"status"`
    CreatedAt     time.Time         `json:"created_at"`
    NodeCount     int               `json:"node_count"`
    RefCount      int               `json:"ref_count"`
    Participants  int               `json:"participants"`
    PreviewNodes  []PreviewNode     `json:"preview_nodes"`
}

// PreviewNode is a single node in the topic preview.
type PreviewNode struct {
    ID        string    `json:"id"`
    Content   string    `json:"content"`     // First ~200 chars
    CreatedAt time.Time `json:"created_at"`
    AuthorName string   `json:"author_name"`
}
```

---

## 11. Error Catalog

### 11.1 Lifecycle Errors

| Error Code | HTTP Status | Condition |
|-----------|-------------|-----------|
| `TOPIC_TITLE_TOO_SHORT` | 400 | Title length < 1 character |
| `TOPIC_TITLE_TOO_LONG` | 400 | Title length > 200 characters |
| `TOPIC_TITLE_ALREADY_EXISTS` | 409 | New title duplicates an existing topic title (case-insensitive) in the same tree |
| `TOPIC_NOT_FOUND` | 404 | Topic ID does not exist in the tree |
| `TOPIC_ALREADY_ARCHIVED` | 409 | Attempt to archive a topic that is already archived |
| `TOPIC_ALREADY_ACTIVE` | 409 | Attempt to restore a topic that is already active |
| `TOPIC_ALREADY_DELETED` | 409 | Attempt to modify a soft-deleted topic |
| `TOPIC_MERGE_CIRCULAR` | 409 | Merge would create a circular parent-child dependency |
| `TOPIC_MERGE_SAME_TOPIC` | 400 | Source and target topic IDs are identical |
| `TOPIC_SPLIT_INVALID_NODE` | 400 | Root node is not within the parent topic's scope |
| `TOPIC_SPLIT_NODE_DELETED` | 400 | Root node has been soft-deleted |
| `TOPIC_DELETE_HAS_CHILDREN` | 409 | HardDelete called on a topic with child topics (use SoftDelete instead) |
| `TOPIC_REORDER_OUT_OF_BOUNDS` | 400 | Topic order list length doesn't match active count (or archived count) |
| `TOPIC_REORDER_TOPIC_NOT_FOUND` | 404 | One or more topic IDs in the order list don't belong to the tree |

### 11.2 SSE Error Events

In addition to HTTP error responses, lifecycle SSE events carry an error variant:

| SSE Event | Payload | Meaning |
|-----------|---------|---------|
| `topic_lifecycle_error` | `{action: string, topic_id: string, error_code: string, message: string}` | A lifecycle action failed. Client should revert optimistic update. |

---

## 12. Edge Cases

| Case | Expected Behavior |
|------|------------------|
| **Archive with open preview** | Preview tooltip closes; SSE `topic_archived` updates sidebar; card moves from Active to Archived section |
| **Delete topic with active #references** | References remain but render with "topic deleted" tooltip; user sees stale reference warning on hover |
| **Merge topic with divergent branches** | Both scopes are combined as a union (append strategy). No content is lost. Conflict detection is a future enhancement. |
| **Rename to same title** | No-op; server returns success with same slug; SSE event still sent for consistency |
| **Reorder during search** | If user has search results open and reorders, the reorder applies to the underlying sidebar order, not the search results. On search close, sidebar reflects new order. |
| **Rapid consecutive reorders** | Each reorder is sent as a complete list. Last-write-wins. Server updates display_order with 1000-gap increments for each batch. |
| **Offline reorder attempt** | Optimistic update applied locally but grey overlay shows "Changes will sync when online." On reconnect, pending reorder is sent. |
| **Delete topic with child topics** | SoftDelete cascades to child topics (all set `deleted_at`). HardDelete blocked by `TOPIC_DELETE_HAS_CHILDREN`. |
| **Split at root node of parent topic** | Child topic's root node is identical to parent's root node. Scope is the entire tree — effectively a clone. Allowed but shows warning: "This will duplicate the entire topic scope." |
| **Topic card with very long title** | Title truncated to one line with ellipsis (`text-overflow: ellipsis`). Full title visible in hover preview. |
| **Many archived topics (100+)** | Archived section paginates: show first 20, "Show all N →" link. |
| **Drag to reorder when filter/sort is active** | Dropping on a sorted view switches to custom sort mode with confirmation dialog. |

---

## 13. SSE Event Specifications

### 13.1 Topic Lifecycle Events

All topic lifecycle SSE events follow the format from SPEC-API-01.

```typescript
// Event: topic_renamed
// Fired when a topic title is changed.
{
  event: 'topic_renamed',
  data: {
    topic_id: "0194f1a2-...",
    tree_id: "0194f1a0-...",
    old_title: "database-schema",
    new_title: "Database Schema V2",
    old_slug: "database-schema",
    new_slug: "database-schema-v2",
    performed_by: "0194f19e-...",
    timestamp: "2026-07-21T23:00:00Z"
  }
}

// Event: topic_archived
// Fired when a topic is archived.
{
  event: 'topic_archived',
  data: {
    topic_id: "0194f1a2-...",
    tree_id: "0194f1a0-...",
    archived_at: "2026-07-21T23:05:00Z",
    performed_by: "0194f19e-...",
    timestamp: "2026-07-21T23:05:00Z"
  }
}

// Event: topic_restored
// Fired when an archived topic is restored to active.
{
  event: 'topic_restored',
  data: {
    topic_id: "0194f1a2-...",
    tree_id: "0194f1a0-...",
    restored_at: "2026-07-21T23:10:00Z",
    performed_by: "0194f19e-...",
    timestamp: "2026-07-21T23:10:00Z"
  }
}

// Event: topic_deleted
// Fired when a topic is soft-deleted.
{
  event: 'topic_deleted',
  data: {
    topic_id: "0194f1a2-...",
    tree_id: "0194f1a0-...",
    deleted_at: "2026-07-21T23:15:00Z",
    performed_by: "0194f19e-...",
    timestamp: "2026-07-21T23:15:00Z"
  }
}

// Event: topic_merged
// Fired when source topic is merged into target topic.
{
  event: 'topic_merged',
  data: {
    source_topic_id: "0194f1a2-...",
    target_topic_id: "0194f1a5-...",
    tree_id: "0194f1a0-...",
    merge_strategy: "append",
    performed_by: "0194f19e-...",
    timestamp: "2026-07-21T23:20:00Z"
  }
}

// Event: topic_split
// Fired when a child topic is split from a parent topic.
{
  event: 'topic_split',
  data: {
    parent_topic_id: "0194f1a2-...",
    child_topic_id: "0194f1a8-...",
    tree_id: "0194f1a0-...",
    split_root_node_id: "0194f1a6-...",
    performed_by: "0194f19e-...",
    timestamp: "2026-07-21T23:25:00Z"
  }
}

// Event: topic_reordered
// Fired when the sidebar topic order is updated.
{
  event: 'topic_reordered',
  data: {
    tree_id: "0194f1a0-...",
    topic_order: [
      {topic_id: "0194f1a2-...", position: 0, status: "active"},
      {topic_id: "0194f1a5-...", position: 1, status: "active"},
      {topic_id: "0194f1a8-...", position: 2, status: "active"}
    ],
    performed_by: "0194f19e-...",
    timestamp: "2026-07-21T23:30:00Z"
  }
}
```

### 13.2 Client-Side SSE Consumption

On receiving a lifecycle event, the client:

1. **Optimistic confirmation:** If the event matches a pending optimistic action (same `topic_id`, same `event_type`, same `performed_by`), the optimistic state is confirmed — the "pending" indicator on the topic card is removed.
2. **Remote update:** If the event is from another client/session, the local state is updated to match: the topic card's position in the sidebar changes (or the card is removed if deleted).
3. **Conflict detection:** If the event contradicts local state (e.g., topic was archived locally but SSE says it was renamed), local state wins with "Pending sync" indicator. A full refresh via `GET /sidebar` is triggered.

---

## 14. Sidebar State Persistence

### 14.1 Local Storage (Client)

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `sidebar.width` | number | 320 | User-resized sidebar width |
| `sidebar.collapsed` | boolean | false | Sidebar collapsed state |
| `sidebar.expandedArchived` | boolean | false | Archived section expanded |
| `sidebar.sortBy` | string | `last_active` | Current sort mode |
| `sidebar.tree_{id}.displayOrder` | string[] | — | Per-tree custom display order (last successful reorder) |

### 14.2 Server State

The `topics.display_order` column (see §3.1) persists the user's custom ordering. On initial sidebar load, active topics are ordered by `display_order ASC` (within the active status). When sort mode is changed, `display_order` is recalculated to match the new sort order so custom sorting always reflects the last explicit action.

---

## 15. Testing

### 15.1 Backend Test Scenarios

| # | Scenario | Setup | Expected |
|---|----------|-------|----------|
| 1 | Rename topic | Topic exists. Call Rename with new title. | Topic returned with new title and slug. Old slug archived. SSE `topic_renamed` emitted. |
| 2 | Rename to duplicate title | Topic T1 "Data". Topic T2 "Data". | Error: `TOPIC_TITLE_ALREADY_EXISTS`. No SSE event emitted. |
| 3 | Rename deleted topic | Topic with deleted_at set. Call Rename. | Error: `TOPIC_ALREADY_DELETED`. |
| 4 | Archive active topic | Active topic. Call Archive. | Status = "archived". archived_at set. SSE `topic_archived`. |
| 5 | Archive already-archived topic | Archived topic. Call Archive. | Error: `TOPIC_ALREADY_ARCHIVED`. |
| 6 | Restore archived topic | Archived topic. Call Restore. | Status = "active". archived_at cleared. SSE `topic_restored`. |
| 7 | Soft-delete topic | Active topic. Call SoftDelete. | deleted_at set. GetByID returns nil. SSE `topic_deleted`. |
| 8 | Hard-delete topic with children | Topic with child topics. Call HardDelete. | Error: `TOPIC_DELETE_HAS_CHILDREN`. |
| 9 | Merge two active topics | Topic A (5 nodes), Topic B (3 nodes). Call Merge(A→B). | Topic B has 8 nodes. Topic A soft-deleted. SSE `topic_merged`. |
| 10 | Merge same topic | Merge(A→A). | Error: `TOPIC_MERGE_SAME_TOPIC`. |
| 11 | Split topic at inner node | Topic T1 with scope of 10 nodes. Split at node N5. | Child topic created with root=N5, scope=5 nodes (N5-N10). SSE `topic_split`. |
| 12 | Split topic at invalid node | Node outside T1's scope. Call Split. | Error: `TOPIC_SPLIT_INVALID_NODE`. |
| 13 | Reorder topics | 5 active topics. Send new order. | All 5 topics updated with display_order + SSE `topic_reordered`. |
| 14 | Reorder with missing topic | 5 topics but send order list with 4. | Error: `TOPIC_REORDER_OUT_OF_BOUNDS`. |
| 15 | Get sidebar data | Tree with 3 active + 2 archived topics. | SidebarData returns 3 active (ordered), 2 archived (ordered). Counts correct. |
| 16 | Get topic preview | Topic with 10 nodes. Call GetPreview. | 3 newest nodes returned. Content truncated to ~200 chars. |
| 17 | Get preview for empty topic | Topic with only root node (no content). | Preview returns single node with empty content. |
| 18 | Lifecycle log entries | Perform 5 different lifecycle actions on same topic. | `topic_lifecycle_log` contains 5 entries with correct event_types and metadata snapshots. |
| 19 | Concurrent merge conflicts | Two requests simultaneously merge Topic A into B and Topic A into C. | Exactly one succeeds. Other gets `TOPIC_ALREADY_DELETED` (A is already deleted by the first merge). |
| 20 | Reorder during SSE disconnect | Client reorders while offline. Reconnect sends reorder. | Server processes reorder. SSE `topic_reordered` sent to all clients. |

### 15.2 Frontend Test Scenarios

| # | Scenario | Expected |
|---|----------|----------|
| 1 | Sidebar renders active topics | Active section shows topic cards sorted by default (last_active). Each card shows title, snippet, time, node count. |
| 2 | Sidebar renders archived topics | Archived section exists, collapsed by default, count badge shows correct number. |
| 3 | Click topic title | Tree view navigates to topic root node. Topic is highlighted in sidebar. |
| 4 | Right-click topic | Context menu opens with all actions (Rename, Archive, Delete, Merge, Split, Copy slug). |
| 5 | Rename via context menu | Inline edit activates on title. Type new name, Enter → topic renamed. SSE confirms. |
| 6 | Rename cancel (Escape) | Inline edit reverts to original title. No API call made. |
| 7 | Archive via context menu | Confirmation dialog → confirm → topic moves to Archived section. SSE confirms. |
| 8 | Restore from Archived section | Click restore button or context menu → topic moves to Active section. SSE confirms. |
| 9 | Delete with confirmation | Confirmation dialog → confirm → topic dims and is removed from sidebar. SSE confirms. |
| 10 | Merge two topics | Context menu → "Merge into..." → type target name → select → confirm → topics merged. SSE confirms. |
| 11 | Hover over topic (150ms) | Preview tooltip appears: shows slug, status, node count, 3 newest node snippets. |
| 12 | Hover and leave before 150ms | No preview shown. |
| 13 | Drag topic to new position | Ghost preview follows cursor. On drop, topic reorders. SSE confirms. |
| 14 | Drag topic to Archived section | Topic archived. Moves to Archived section. SSE confirms. |
| 15 | Ctrl+K opens search | Search sidebar opens with focus on search input. |
| 16 | Ctrl+Shift+N | "New Topic" dialog opens for selected node. |
| 17 | Escape closes context menu | Context menu dismissed. No action taken. |
| 18 | SSE `topic_renamed` from remote | Sidebar updates topic card title and slug without user action. |
| 19 | SSE `topic_archived` from remote | Topic card slides from Active to Archived section with animation. |
| 20 | Offline optimistic archive | Topic moves to Archived section immediately. Shows "Pending sync" badge. On reconnect, SSE confirms or reverts. |
| 21 | Sort mode change | Click sort dropdown, select "title" → topics reorder alphabetically. Custom order discarded with confirmation. |
| 22 | Resize sidebar | Drag sidebar border → width changes. Persists on reload. |
| 23 | Collapse sidebar | Click collapse icon → sidebar closed. Tree view expands to full width. |
| 24 | Keyboard navigation (Ctrl+Tab) | Focus cycles through active topic cards. Highlight moves with each press. |
| 25 | Split topic from context menu | "Split from here" → enter title → confirm → new child topic appears in sidebar. Parent topic still shows original scope. |

---

## 16. Hilo Impact

### What depends on this component:

| Component | Depends On | Reason |
|-----------|-----------|--------|
| FE-04 (Topic Sidebar) | This spec | Implements the sidebar widget, topic cards, archived section, drag-and-drop, responsive layout |
| FE-07 (Context Menu) | This spec | Right-click menu, all lifecycle actions, inline rename, confirmation dialogs |
| FE-05 (Topic Preview Tooltip) | This spec | Hover preview card with lazy-loaded node snippets |
| CLIP-09 (Keyboard Shortcuts) | This spec | Global shortcut registration, scope detection, conflict resolution |
| AGENT-02 (Context Compiler) | This spec | Lifecycle SSE events inform the compiler when topic structure changes |
| BE-06 (Topic Service) | This spec | `TopicLifecycleService` implementation |

### What this component depends on:

| Component | Required By | Reason |
|-----------|------------|--------|
| SPEC-TM-01 (Topic Data Model) | This spec | `Topic` table, `TopicCard`, slug format, status lifecycle values |
| SPEC-TM-02 (Auto-Topic Detection) | This spec | Detected topics appear in sidebar for review/confirmation |
| SPEC-TM-03 (Topic Search & One-Button Context) | This spec | Ctrl+K opens the search sidebar; "Add to Context" button |
| SPEC-TM-04 (#Reference Resolution) | This spec | `ref_count` on topic cards; archived topics excluded from autocomplete |
| SPEC-API-01 (SSE Event Stream) | This spec | All lifecycle events delivered via SSE |
| SPEC-API-02 (Tree CRUD Endpoints) | This spec | Tree existence validation for topic lifecycle |
| SPEC-DM-01 (Tree Node & Edge DDL) | This spec | `nodes` table for preview queries; edge types for merge/split scope computation |
| SPEC-DM-04 (User & Profile Model) | This spec | `performed_by` linking to profiles |

### Hilo Dependency Graph (relevant subset)

```
SPEC-TM-01 ──> SPEC-TM-02 ──> SPEC-TM-03 ──> SPEC-TM-04 ──> SPEC-TM-05
                                                                  │
                                              ┌───────────────────┤
                                              │                   │
                                         FE-04 (Sidebar)    BE-06 (Lifecycle Service)
                                         FE-07 (Menu)       CLIP-09 (Shortcuts)
                                         FE-05 (Preview)
```

---

## 17. Version History

| Version | Date | Author | Changes |
|---------|------|--------|---------|
| 1.0 | 2026-07-21 | Hermes Foreman | Initial spec. Sidebar layout, context menu, preview, drag-and-drop, keyboard shortcuts, API endpoints, SSE events, 25 frontend + 20 backend test scenarios. |
