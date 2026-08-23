/**
 * Hermes Canopy — Trees Page
 *
 * Full CRUD for conversation trees. Lists all trees, supports
 * creation, deletion, and navigation to tree detail.
 */

import { useState, useEffect, useCallback, useMemo } from 'react';
import {
  Plus,
  Trash2,
  ExternalLink,
  RefreshCw,
  Clock,
  Hash,
  User,
  AlertCircle,
  Inbox,
  Search,
  ChevronDown,
  ChevronRight,
  GitBranch,
} from 'lucide-react';
import { useNavigate } from 'react-router-dom';
import { apiGet, apiPost, apiDelete } from '../lib/api';
import {
  buildTreeSections,
  countGroupedTrees,
  SOURCE_CHIP_CLASSES,
  SOURCE_LABELS,
  normalizeSource,
  type SessionFields,
  type SessionTreeNode,
  type SourceGroup,
} from '../lib/sessionTreeGroups';

// ─── Types ─────────────────────────────────────────────────────────────

interface TreeSummary extends SessionFields {
  owner_id: string;
  owner_display_name: string;
  node_count: number;
  member_count: number;
  root_node_id: string;
  created_at: string;
  updated_at: string;
  role: string;
}

interface TreeDetail extends TreeSummary {
  deleted_at: string | null;
}

interface Pagination {
  nextCursor: string | null;
  hasMore: boolean;
  total: number;
  limit: number;
}

interface ListTreesResponse {
  trees: TreeSummary[];
  pagination: Pagination;
}

// ─── Helpers ───────────────────────────────────────────────────────────

function formatTimeAgo(iso: string): string {
  try {
    const ms = Date.now() - new Date(iso).getTime();
    const sec = Math.floor(ms / 1000);
    if (sec < 60) return `${sec}s ago`;
    const min = Math.floor(sec / 60);
    if (min < 60) return `${min}m ago`;
    const hr = Math.floor(min / 60);
    if (hr < 24) return `${hr}h ago`;
    const days = Math.floor(hr / 24);
    return `${days}d ago`;
  } catch {
    return iso;
  }
}

/**
 * Append an incoming page of trees to the existing list, deduplicating by id.
 * The create/delete handlers mutate the list and load-more can refetch a
 * cursor boundary, so duplicates are possible — keep the first occurrence
 * (the already-loaded/newer one) and preserve existing order.
 */
export function appendTrees<T extends { id: string }>(existing: T[], incoming: T[]): T[] {
  const seen = new Set(existing.map((t) => t.id));
  const merged = existing.slice();
  for (const tree of incoming) {
    if (!seen.has(tree.id)) {
      seen.add(tree.id);
      merged.push(tree);
    }
  }
  return merged;
}

/**
 * Build the POST /trees body from dialog fields.
 *
 * The backend (GAP-008 contract) requires a root message with non-empty
 * content AND a valid nodeType ('message' | 'announcement') — a title-only
 * payload 400s with VALIDATION_ERROR 'root message content is required'.
 * Returns `null` when the required fields are missing (callers show a
 * validation error instead of hitting the API).
 */
export function buildCreateTreePayload(
  title: string,
  description: string,
  rootContent: string,
): Record<string, unknown> | null {
  if (!title.trim() || !rootContent.trim()) return null;
  const body: Record<string, unknown> = {
    title: title.trim(),
    rootMessage: {
      content: rootContent.trim(),
      contentFormat: 'markdown',
      nodeType: 'message',
    },
  };
  if (description.trim()) body.description = description.trim();
  return body;
}

// ─── Create Tree Dialog ────────────────────────────────────────────────

function CreateTreeDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (tree: TreeDetail) => void;
}) {
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [rootContent, setRootContent] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleCreate = async () => {
    if (!title.trim()) {
      setError('Title is required');
      return;
    }
    if (!rootContent.trim()) {
      setError('Root message is required');
      return;
    }
    setLoading(true);
    setError(null);
    try {
      const body = buildCreateTreePayload(title, description, rootContent);
      if (!body) {
        setError('Title and root message are required');
        return;
      }
      const tree = await apiPost<TreeDetail>('/trees', body);
      onCreated(tree);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create tree');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center pt-[10vh]">
      <div className="absolute inset-0 bg-black/60" onClick={onClose} />
      <div className="relative glass-raised rounded-xl w-full max-w-md mx-4">
        <div className="px-5 py-4 border-b border-line-subtle">
          <h2 className="text-sm font-medium text-content-primary">Create Tree</h2>
        </div>
        <div className="px-5 py-4 space-y-3">
          {error && (
            <div
              id="create-tree-error"
              className="flex items-center gap-2 p-2 rounded bg-rose-500/10 border border-rose-500/30 text-status-danger text-xs"
              role="alert"
            >
              <AlertCircle className="w-3.5 h-3.5 flex-shrink-0" aria-hidden="true" />
              {error}
            </div>
          )}
          <div>
            <label htmlFor="create-tree-title" className="block text-xs text-content-muted mb-1">Title *</label>
            <input
              id="create-tree-title"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              className="w-full bg-surface-input border border-line-subtle rounded-lg px-3 py-2 text-sm text-content-primary placeholder-content-faint focus:outline-none focus:ring-2 focus:ring-accent/60 focus:border-accent"
              placeholder="My Conversation Tree"
              autoFocus
              onKeyDown={(e) => e.key === 'Enter' && handleCreate()}
              aria-required="true"
              aria-invalid={!!error && !title.trim()}
              aria-describedby={error ? "create-tree-error" : undefined}
            />
          </div>
          <div>
            <label htmlFor="create-tree-desc" className="block text-xs text-content-muted mb-1">Description</label>
            <textarea
              id="create-tree-desc"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={2}
              className="w-full bg-surface-input border border-line-subtle rounded-lg px-3 py-2 text-sm text-content-primary placeholder-content-faint resize-none focus:outline-none focus:ring-2 focus:ring-accent/60 focus:border-accent"
              placeholder="Optional description..."
            />
          </div>
          <div>
            <label htmlFor="create-tree-root-msg" className="block text-xs text-content-muted mb-1">Root Message *</label>
            <textarea
              id="create-tree-root-msg"
              value={rootContent}
              onChange={(e) => setRootContent(e.target.value)}
              rows={3}
              className="w-full bg-surface-input border border-line-subtle rounded-lg px-3 py-2 text-sm text-content-primary placeholder-content-faint resize-none focus:outline-none focus:ring-2 focus:ring-accent/60 focus:border-accent"
              placeholder="Initial message content..."
              aria-required="true"
              aria-invalid={!!error && !rootContent.trim()}
              aria-describedby={error ? "create-tree-error" : undefined}
            />
          </div>
        </div>
        <div className="px-5 py-3 border-t border-line-subtle flex items-center justify-end gap-2">
          <button
            onClick={onClose}
            className="px-3 py-1.5 text-xs font-medium text-content-muted hover:text-content-primary rounded-lg hover:bg-surface-hover transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={handleCreate}
            disabled={loading || !title.trim()}
            className="px-4 py-1.5 text-xs font-semibold text-white bg-accent-2-600 hover:bg-accent-2-500 rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {loading ? 'Creating...' : 'Create'}
          </button>
        </div>
      </div>
    </div>
  );
}

// ─── Delete Confirm Dialog ─────────────────────────────────────────────

function DeleteConfirmDialog({
  treeTitle,
  onConfirm,
  onCancel,
}: {
  treeTitle: string;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const [loading, setLoading] = useState(false);

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center pt-[15vh]">
      <div className="absolute inset-0 bg-black/60" onClick={onCancel} />
      <div className="relative glass-raised rounded-xl w-full max-w-sm mx-4">
        <div className="px-5 py-4 border-b border-line-subtle">
          <h2 className="text-sm font-medium text-content-primary">Delete Tree</h2>
        </div>
        <div className="px-5 py-4">
          <p className="text-sm text-content-secondary">
            Are you sure you want to delete <span className="text-content-primary font-medium">"{treeTitle}"</span>?
            This action cannot be undone.
          </p>
        </div>
        <div className="px-5 py-3 border-t border-line-subtle flex items-center justify-end gap-2">
          <button
            onClick={onCancel}
            className="px-3 py-1.5 text-xs font-medium text-content-muted hover:text-content-primary rounded-lg hover:bg-surface-hover transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={() => {
              setLoading(true);
              onConfirm();
            }}
            disabled={loading}
            className="px-4 py-1.5 text-xs font-semibold text-white bg-rose-600 hover:bg-rose-500 rounded-lg transition-colors disabled:opacity-50"
          >
            {loading ? 'Deleting...' : 'Delete'}
          </button>
        </div>
      </div>
    </div>
  );
}

// ─── Source chip ───────────────────────────────────────────────────────

/** Colored source tag shown on session tree cards. */
function SourceChip({ source }: { source: SourceGroup }) {
  return (
    <span
      className={`inline-flex items-center px-1.5 py-0.5 rounded border text-[10px] font-medium tracking-wide ${SOURCE_CHIP_CLASSES[source]}`}
    >
      {SOURCE_LABELS[source]}
    </span>
  );
}

// ─── Tree Card ─────────────────────────────────────────────────────────

function TreeCard({
  node,
  onSelect,
  onDelete,
}: {
  node: SessionTreeNode<TreeSummary>;
  onSelect: () => void;
  onDelete: () => void;
}) {
  const { tree, continuations } = node;
  // Workspace trees have no raw source; session trees always carry one.
  const sourceGroup = tree.source ? normalizeSource(tree.source) : null;

  return (
    <div
      data-testid="tree-card"
      className="rounded-lg border border-line-subtle bg-surface-panel p-4 hover:border-accent-2/40 cursor-pointer group transition-colors"
      onClick={onSelect}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="flex-1 min-w-0">
          <h3 className="text-sm font-medium text-content-primary truncate flex items-center gap-2">
            <span className="truncate">{tree.title}</span>
            {sourceGroup && <SourceChip source={sourceGroup} />}
            {continuations.length > 0 && (
              <ContinuationsBadge count={continuations.length} items={continuations} />
            )}
          </h3>
          {tree.description && (
            <p className="text-xs text-content-muted mt-1 line-clamp-2">{tree.description}</p>
          )}
          <div className="flex items-center gap-3 mt-2 text-[11px] text-content-faint">
            <span className="flex items-center gap-1">
              <Hash className="w-3 h-3" />
              {tree.node_count} nodes
            </span>
            <span className="flex items-center gap-1">
              <User className="w-3 h-3" />
              {tree.member_count} members
            </span>
            <span className="flex items-center gap-1">
              <Clock className="w-3 h-3" />
              {formatTimeAgo(tree.created_at)}
            </span>
          </div>
        </div>
        <div className="flex items-center gap-1 flex-shrink-0">
          <button
            onClick={(e) => {
              e.stopPropagation();
              onSelect();
            }}
            className="p-1.5 rounded-md text-content-faint hover:text-accent-2 hover:bg-accent-2/10 transition-colors"
            title="Open tree"
            aria-label={`Open tree: ${tree.title}`}
          >
            <ExternalLink className="w-4 h-4" />
          </button>
          <button
            onClick={(e) => {
              e.stopPropagation();
              onDelete();
            }}
            className="p-1.5 rounded-md text-content-faint hover:text-status-danger hover:bg-rose-500/10 transition-colors"
            title="Delete tree"
            aria-label={`Delete tree: ${tree.title}`}
          >
            <Trash2 className="w-4 h-4" />
          </button>
        </div>
      </div>
    </div>
  );
}

/** "N continuations" badge with an inline expandable segment list. */
function ContinuationsBadge({
  count,
  items,
}: {
  count: number;
  items: TreeSummary[];
}) {
  const [open, setOpen] = useState(false);
  return (
    <span className="inline-flex relative" onClick={(e) => e.stopPropagation()}>
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation();
          setOpen((v) => !v);
        }}
        aria-expanded={open}
        title={`${count} continuation segment${count === 1 ? '' : 's'} of this session`}
        className="inline-flex items-center gap-0.5 px-1.5 py-0.5 rounded bg-accent/10 text-accent-300 border border-accent/30 text-[10px] font-medium hover:bg-accent/20 transition-colors"
      >
        <GitBranch className="w-3 h-3" />
        {count} cont.
      </button>
      {open && (
        <ul className="absolute left-0 top-full mt-1 z-10 min-w-[220px] max-w-xs rounded-lg border border-line-subtle bg-surface-raised p-1 shadow-lg">
          {items.map((c) => (
            <li key={c.id}>
              <span
                role="link"
                tabIndex={0}
                onClick={() => setOpen(false)}
                onKeyDown={(e) => e.key === 'Enter' && setOpen(false)}
                className="block px-2 py-1 rounded text-[11px] text-content-secondary hover:bg-surface-hover truncate"
                title={c.title}
              >
                {formatTimeAgo(c.created_at)} · {c.title}
              </span>
            </li>
          ))}
        </ul>
      )}
    </span>
  );
}

// ─── Main Component ────────────────────────────────────────────────────

export default function TreesPage() {
  const navigate = useNavigate();
  const [trees, setTrees] = useState<TreeSummary[]>([]);
  const [pagination, setPagination] = useState<Pagination | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [errorMore, setErrorMore] = useState<string | null>(null);
  const [showCreate, setShowCreate] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<TreeSummary | null>(null);
  const [search, setSearch] = useState('');
  // Collapsed source groups (default EXPANDED: the list is small enough
  // that hiding content hurts more than it helps; collapse is opt-in).
  const [collapsedGroups, setCollapsedGroups] = useState<Set<SourceGroup>>(new Set());

  const sections = useMemo(() => buildTreeSections(trees, search), [trees, search]);
  const visibleCount = countGroupedTrees(sections);
  const toggleGroup = useCallback((source: SourceGroup) => {
    setCollapsedGroups((prev) => {
      const next = new Set(prev);
      if (next.has(source)) {
        next.delete(source);
      } else {
        next.add(source);
      }
      return next;
    });
  }, []);

  const fetchTrees = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await apiGet<ListTreesResponse>('/trees?limit=50');
      setTrees(data.trees);
      setPagination(data.pagination);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load trees');
    } finally {
      setLoading(false);
    }
  }, []);

  const loadMore = useCallback(async () => {
    if (!pagination?.hasMore || !pagination.nextCursor || loadingMore) return;
    setLoadingMore(true);
    setErrorMore(null);
    try {
      const data = await apiGet<ListTreesResponse>(
        `/trees?limit=50&cursor=${encodeURIComponent(pagination.nextCursor)}`,
      );
      setTrees((prev) => appendTrees(prev, data.trees));
      setPagination(data.pagination);
    } catch (err) {
      setErrorMore(err instanceof Error ? err.message : 'Failed to load more trees');
    } finally {
      setLoadingMore(false);
    }
  }, [pagination, loadingMore]);

  useEffect(() => {
    void fetchTrees();
  }, [fetchTrees]);

  const handleDelete = async () => {
    if (!deleteTarget) return;
    try {
      await apiDelete(`/trees/${deleteTarget.id}`);
      setTrees((prev) => prev.filter((t) => t.id !== deleteTarget.id));
      setDeleteTarget(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete tree');
      setDeleteTarget(null);
    }
  };

  const handleCreated = (tree: TreeDetail) => {
    setTrees((prev) => [tree, ...prev]);
    setShowCreate(false);
  };

  return (
    <div className="p-6">
      {/* Header */}
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-content-primary">Trees</h1>
          <p className="text-sm text-content-muted mt-1">
            {pagination ? `${pagination.total} conversation trees` : 'Manage conversation trees'}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={fetchTrees}
            disabled={loading}
            className="flex items-center gap-2 px-3 py-2 rounded-lg text-xs font-medium bg-surface-input hover:bg-surface-hover text-content-secondary ring-1 ring-inset ring-line-subtle transition-colors disabled:opacity-50"
          >
            <RefreshCw className={`w-3.5 h-3.5 ${loading ? 'animate-spin' : ''}`} />
            Refresh
          </button>
          <button
            onClick={() => setShowCreate(true)}
            className="flex items-center gap-2 px-3 py-2 rounded-lg text-xs font-semibold text-white bg-accent-2-600 hover:bg-accent-2-500 transition-colors"
          >
            <Plus className="w-3.5 h-3.5" />
            New Tree
          </button>
        </div>
      </div>

      {/* Error banner */}
      {error && (
        <div
          className="flex items-center gap-2 mb-4 p-3 rounded-lg bg-rose-500/10 border border-rose-500/30 text-status-danger text-sm"
          role="alert"
        >
          <AlertCircle className="w-4 h-4 flex-shrink-0" aria-hidden="true" />
          <span>{error}</span>
          <button onClick={fetchTrees} className="ml-auto text-xs underline hover:text-red-300">
            Retry
          </button>
        </div>
      )}

      {/* Search (filters title/description; groups stay intact) */}
      {!loading && !error && trees.length > 0 && (
        <div className="relative mb-4">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-content-faint pointer-events-none" />
          <input
            id="trees-search"
            type="search"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Filter trees by title or description…"
            className="w-full bg-surface-input border border-line-subtle rounded-lg pl-9 pr-3 py-2 text-sm text-content-primary placeholder-content-faint focus:outline-none focus:ring-2 focus:ring-accent/60 focus:border-accent"
          />
        </div>
      )}

      {/* Loading state */}
      {loading && (
        <div className="space-y-3">
          {[1, 2, 3].map((i) => (
            <div key={i} className="rounded-lg border border-line-subtle p-4 animate-pulse">
              <div className="h-4 bg-surface-input rounded w-48 mb-2" />
              <div className="h-3 bg-surface-input rounded w-72 mb-2" />
              <div className="h-3 bg-surface-input rounded w-32" />
            </div>
          ))}
        </div>
      )}

      {/* Empty state */}
      {!loading && !error && trees.length === 0 && (
        <div className="rounded-xl border border-line-subtle bg-surface-panel p-12 text-center">
          <Inbox className="w-10 h-10 text-content-faint/50 mx-auto mb-3" />
          <h2 className="text-sm font-medium text-content-secondary mb-1">No trees yet</h2>
          <p className="text-xs text-content-muted mb-4">
            Create your first conversation tree to get started.
          </p>
          <button
            onClick={() => setShowCreate(true)}
            className="inline-flex items-center gap-2 px-4 py-2 rounded-lg text-xs font-semibold text-white bg-accent-2-600 hover:bg-accent-2-500 transition-colors"
          >
            <Plus className="w-3.5 h-3.5" />
            Create Tree
          </button>
        </div>
      )}

      {/* Grouped tree cards (session trees by source; workspace ungrouped) */}
      {!loading && !error && trees.length > 0 && (
        <div className="space-y-5">
          {visibleCount === 0 && (
            <div
              className="rounded-lg border border-line-subtle bg-surface-panel p-8 text-center text-sm text-content-muted"
              data-testid="trees-search-empty"
            >
              No trees match &quot;{search.trim()}&quot;.
            </div>
          )}
          {sections.groups.map((group) => {
            const collapsed = collapsedGroups.has(group.source);
            return (
              <section key={group.source} aria-label={`${group.label} sessions`}>
                <button
                  type="button"
                  onClick={() => toggleGroup(group.source)}
                  aria-expanded={!collapsed}
                  className="flex items-center gap-2 w-full mb-2 px-1 py-1 text-left rounded hover:bg-surface-hover/60 transition-colors"
                >
                  {collapsed ? (
                    <ChevronRight className="w-3.5 h-3.5 text-content-faint" />
                  ) : (
                    <ChevronDown className="w-3.5 h-3.5 text-content-faint" />
                  )}
                  <span className="text-xs font-semibold uppercase tracking-wider text-content-secondary">
                    {group.label}
                  </span>
                  <SourceChip source={group.source} />
                  <span className="text-[11px] text-content-faint">{group.trees.length}</span>
                  <span className="flex-1 border-t border-line-subtle ml-2" aria-hidden="true" />
                </button>
                {!collapsed && (
                  <div className="space-y-3">
                    {group.trees.map((node) => (
                      <TreeCard
                        key={node.tree.id}
                        node={node}
                        onSelect={() => navigate(`/tree/${node.tree.id}`)}
                        onDelete={() => setDeleteTarget(node.tree)}
                      />
                    ))}
                  </div>
                )}
              </section>
            );
          })}
          {sections.ungrouped.length > 0 && (
            <section aria-label="Workspace trees">
              <div className="flex items-center gap-2 w-full mb-2 px-1">
                <span className="text-xs font-semibold uppercase tracking-wider text-content-secondary">
                  Workspace
                </span>
                <span className="text-[11px] text-content-faint">{sections.ungrouped.length}</span>
                <span className="flex-1 border-t border-line-subtle" aria-hidden="true" />
              </div>
              <div className="space-y-3">
                {sections.ungrouped.map((tree) => (
                  <TreeCard
                    key={tree.id}
                    node={{ tree, continuations: [] }}
                    onSelect={() => navigate(`/tree/${tree.id}`)}
                    onDelete={() => setDeleteTarget(tree)}
                  />
                ))}
              </div>
            </section>
          )}
        </div>
      )}

      {/* Load more (cursor pagination) */}
      {!loading && !error && pagination?.hasMore === true && (
        <div className="mt-6 flex flex-col items-center gap-2">
          <button
            id="load-more-trees"
            onClick={loadMore}
            disabled={loadingMore}
            className="flex items-center gap-2 px-4 py-2 rounded-lg text-xs font-medium bg-surface-input hover:bg-surface-hover text-content-secondary ring-1 ring-inset ring-line-subtle transition-colors disabled:opacity-50"
          >
            <RefreshCw className={`w-3.5 h-3.5 ${loadingMore ? 'animate-spin' : ''}`} />
            {loadingMore ? 'Loading…' : 'Load more'}
          </button>
          {errorMore && (
            <div
              className="flex items-center gap-2 p-2 rounded bg-rose-500/10 border border-rose-500/30 text-status-danger text-xs"
              role="alert"
            >
              <AlertCircle className="w-3.5 h-3.5 flex-shrink-0" aria-hidden="true" />
              <span>{errorMore}</span>
              <button onClick={loadMore} className="ml-auto text-xs underline hover:text-red-300">
                Retry
              </button>
            </div>
          )}
        </div>
      )}

      {/* Create dialog */}
      {showCreate && (
        <CreateTreeDialog
          onClose={() => setShowCreate(false)}
          onCreated={handleCreated}
        />
      )}

      {/* Delete confirmation */}
      {deleteTarget && (
        <DeleteConfirmDialog
          treeTitle={deleteTarget.title}
          onConfirm={handleDelete}
          onCancel={() => setDeleteTarget(null)}
        />
      )}
    </div>
  );
}
