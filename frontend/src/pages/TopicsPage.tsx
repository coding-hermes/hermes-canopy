/**
 * Hermes Canopy — Topics Page
 *
 * Full CRUD for topics. Topics are named, searchable subgraphs
 * anchored to a specific root node within a conversation tree.
 */

import { useState, useEffect, useCallback } from 'react';
import { useSearchParams } from 'react-router-dom';
import {
  Plus,
  Trash2,
  RefreshCw,
  Hash,
  Clock,
  AlertCircle,
  Inbox,
  ChevronDown,
  Search,
  X,
} from 'lucide-react';
import { apiGet, apiPost, apiDelete } from '../lib/api';
import type { TopicSummary, TreeSummary } from '../types/topic';
import type { TreeDetail } from '../types/tree';
import { readStoredTreeId, storeTreeId, notifyTopicsChanged } from '../lib/activeTree';

// ─── Types ─────────────────────────────────────────────────────────────

interface ListTopicsResponse {
  topics: TopicSummary[];
}

interface ListTreesResponse {
  trees: TreeSummary[];
  pagination: { total: number };
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
    return `${Math.floor(hr / 24)}d ago`;
  } catch { return iso; }
}

const STATUS_STYLES: Record<string, string> = {
  active: 'bg-green-400',
  archived: 'bg-content-faint',
  draft: 'bg-amber-400',
};

/** The nil-UUID sentinel a treeless root serializes to (GAP-044). */
const ZERO_UUID = '00000000-0000-0000-0000-000000000000';

// ─── Create Topic Dialog ───────────────────────────────────────────────

function CreateTopicDialog({
  trees,
  treeId,
  onClose,
  onCreated,
}: {
  trees: TreeSummary[];
  treeId: string;
  onClose: () => void;
  onCreated: (topic: TopicSummary) => void;
}) {
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [rootNodeId, setRootNodeId] = useState('');
  const [resolvingRoot, setResolvingRoot] = useState(true);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // GAP-044: auto-resolve the tree's root node so creating a topic never
  // requires hand-typing a UUID. The tree-detail endpoint serializes
  // RootNodeID as `root_node_id` (snake_case — internal/service
  // TreeSummary `json:"root_node_id"`), which is the key that actually
  // arrives on the wire; the POST body below stays camelCase per the
  // topics API contract (docs/API.md §Topics).
  useEffect(() => {
    let cancelled = false;
    setResolvingRoot(true);
    setError(null);
    void (async () => {
      try {
        const detail = await apiGet<TreeDetail>(`/trees/${treeId}`);
        if (cancelled) return;
        const resolved = detail.root_node_id;
        if (!resolved || resolved === ZERO_UUID) {
          setRootNodeId('');
          setError('Could not resolve a root node for this tree.');
        } else {
          setRootNodeId(resolved);
        }
      } catch (err) {
        if (cancelled) return;
        setRootNodeId('');
        setError(
          `Could not resolve the root node: ${err instanceof Error ? err.message : 'tree detail unavailable'}`,
        );
      } finally {
        if (!cancelled) setResolvingRoot(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [treeId]);

  const handleCreate = async () => {
    if (!title.trim()) {
      setError('Title is required');
      return;
    }
    if (!treeId) {
      setError('Tree is required');
      return;
    }
    if (!rootNodeId.trim()) {
      setError('Root node ID is required');
      return;
    }
    setLoading(true);
    setError(null);
    try {
      const topic = await apiPost<TopicSummary>('/topics', {
        treeId,
        rootNodeId: rootNodeId.trim(),
        title: title.trim(),
        description: description.trim() || undefined,
      });
      onCreated(topic);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create topic');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center pt-[10vh]">
      <div className="absolute inset-0 bg-black/60" onClick={onClose} />
      <div className="relative glass-raised rounded-xl w-full max-w-md mx-4">
        <div className="px-5 py-4 border-b border-line-subtle">
          <h2 className="text-sm font-medium text-content-primary">Create Topic</h2>
        </div>
        <div className="px-5 py-4 space-y-3">
          {error && (
            <div
              data-testid="create-topic-error"
              className="flex items-center gap-2 p-2 rounded bg-rose-500/10 border border-rose-500/30 text-status-danger text-xs"
            >
              <AlertCircle className="w-3.5 h-3.5 flex-shrink-0" />
              {error}
            </div>
          )}
          <div>
            <label className="block text-xs text-content-muted mb-1">Subject Tree</label>
            <select
              value={treeId}
              disabled
              className="w-full bg-surface-input/60 border border-line-subtle rounded-lg px-3 py-2 text-sm text-content-muted"
            >
              <option value={treeId}>
                {trees.find((t) => t.id === treeId)?.title ?? treeId}
              </option>
            </select>
          </div>
          <div>
            <label className="block text-xs text-content-muted mb-1">Root Node</label>
            {resolvingRoot ? (
              <div className="w-full bg-surface-input/60 border border-line-subtle rounded-lg px-3 py-2 text-sm text-content-faint flex items-center gap-2">
                <RefreshCw className="w-3.5 h-3.5 animate-spin" />
                Resolving root node...
              </div>
            ) : (
              <div
                data-testid="create-topic-root-node"
                className="w-full bg-surface-input/60 border border-line-subtle rounded-lg px-3 py-2 text-sm text-content-muted font-mono break-all"
              >
                {rootNodeId || 'Unavailable'}
              </div>
            )}
            <p className="text-[11px] text-content-faint mt-1">
              Auto-resolved from the tree's root node — no UUID needed.
            </p>
          </div>
          <div>
            <label className="block text-xs text-content-muted mb-1">Title *</label>
            <input
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              className="w-full bg-surface-input border border-line-subtle rounded-lg px-3 py-2 text-sm text-content-primary placeholder-content-faint focus:outline-none focus:ring-2 focus:ring-accent/60 focus:border-accent"
              placeholder="Topic title"
              autoFocus
              onKeyDown={(e) => e.key === 'Enter' && handleCreate()}
            />
          </div>
          <div>
            <label className="block text-xs text-content-muted mb-1">Description</label>
            <textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={2}
              className="w-full bg-surface-input border border-line-subtle rounded-lg px-3 py-2 text-sm text-content-primary placeholder-content-faint resize-none focus:outline-none focus:ring-2 focus:ring-accent/60 focus:border-accent"
              placeholder="Optional description..."
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
            data-testid="create-topic-submit"
            onClick={handleCreate}
            disabled={loading || resolvingRoot || !title.trim() || !rootNodeId.trim()}
            className="px-4 py-1.5 text-xs font-semibold text-white bg-accent-2-600 hover:bg-accent-2-500 rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {loading ? 'Creating...' : 'Create'}
          </button>
        </div>
      </div>
    </div>
  );
}

// ─── Topic Card ────────────────────────────────────────────────────────

function TopicCard({
  topic,
  highlighted,
  onDelete,
}: {
  topic: TopicSummary;
  highlighted?: boolean;
  onDelete: () => void;
}) {
  const dotColor = STATUS_STYLES[topic.status] ?? 'bg-content-muted';

  return (
    <div
      id={`topic-${topic.id}`}
      aria-current={highlighted ? 'true' : undefined}
      className={`rounded-lg border bg-surface-panel p-4 group transition-colors ${
        highlighted
          ? 'border-accent-2/60 ring-1 ring-inset ring-accent-2/30'
          : 'border-line-subtle hover:border-accent-2/40'
      }`}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 mb-1">
            <div className={`w-2 h-2 rounded-full flex-shrink-0 ${dotColor}`} />
            <h3 className="text-sm font-medium text-content-primary truncate">
              {topic.title}
            </h3>
            <span className="text-[10px] text-content-secondary uppercase tracking-wide bg-surface-input ring-1 ring-inset ring-line-subtle rounded-xs px-1.5 py-0.5">
              {topic.status}
            </span>
          </div>
          {topic.description && (
            <p className="text-xs text-content-muted mt-1 line-clamp-2">
              {topic.description}
            </p>
          )}
          <div className="flex items-center gap-3 mt-2 text-[11px] text-content-faint">
            <span className="flex items-center gap-1">
              <Hash className="w-3 h-3" />
              {topic.node_count} nodes
            </span>
            <span className="flex items-center gap-1">
              <Clock className="w-3 h-3" />
              {formatTimeAgo(topic.created_at)}
            </span>
            <span className="font-mono text-[10px]">
              slug: #{topic.slug}
            </span>
          </div>
        </div>
        <div className="flex items-center gap-1 flex-shrink-0">
          <button
            onClick={(e) => {
              e.stopPropagation();
              onDelete();
            }}
            className="p-1.5 rounded-md text-content-faint hover:text-status-danger hover:bg-rose-500/10 transition-colors"
            title="Archive topic"
            aria-label={`Archive topic: ${topic.title}`}
          >
            <Trash2 className="w-4 h-4" />
          </button>
        </div>
      </div>
    </div>
  );
}

// ─── Main Component ────────────────────────────────────────────────────

export default function TopicsPage() {
  // Deep links from the persistent topics rail (UI-02):
  //   /topics?tree=<treeId>          — preselect a tree
  //   /topics?tree=…&topic=<topicId> — preselect + highlight a topic
  //   /topics?new=1                  — open the create dialog
  const [searchParams, setSearchParams] = useSearchParams();
  const treeParam = searchParams.get('tree') ?? '';
  const topicParam = searchParams.get('topic') ?? '';
  const wantsCreate = searchParams.get('new') === '1';

  const [trees, setTrees] = useState<TreeSummary[]>([]);
  const [selectedTreeId, setSelectedTreeId] = useState<string>(
    () => treeParam || readStoredTreeId(),
  );
  const [topics, setTopics] = useState<TopicSummary[]>([]);
  const [topicsLoading, setTopicsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [searchQuery, setSearchQuery] = useState('');
  const [showCreate, setShowCreate] = useState(wantsCreate);
  const [deleteTarget, setDeleteTarget] = useState<TopicSummary | null>(null);

  const fetchTrees = useCallback(async () => {
    // Loading state handled by trees array being empty
    try {
      const data = await apiGet<ListTreesResponse>('/trees?limit=100');
      let list = data.trees;
      // Pagination (PAG-001/002): a deep-linked/stored tree may predate the
      // first page — the select must still be able to display the active
      // tree, so fetch it by id and prepend it (TM-03 / VREG-001 durability).
      if (selectedTreeId && !list.some((t) => t.id === selectedTreeId)) {
        try {
          const single = await apiGet<TreeSummary>(`/trees/${selectedTreeId}`);
          list = [single, ...list];
        } catch {
          // Active tree missing/forbidden — keep the paged list as-is.
        }
      }
      setTrees(list);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load trees');
    }
  }, [selectedTreeId]);

  useEffect(() => {
    void fetchTrees();
  }, [fetchTrees]);

  const fetchTopics = useCallback(async (treeId: string) => {
    if (!treeId) return;
    setTopicsLoading(true);
    setError(null);
    try {
      const data = await apiGet<ListTopicsResponse>(
        `/topics?tree_id=${encodeURIComponent(treeId)}&limit=100`,
      );
      setTopics(data.topics);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load topics');
    } finally {
      setTopicsLoading(false);
    }
  }, []);

  // Load topics whenever the scoped tree changes (initial mount included —
  // the tree may arrive from the ?tree= deep link or localStorage).
  useEffect(() => {
    if (selectedTreeId) void fetchTopics(selectedTreeId);
    else setTopics([]);
  }, [selectedTreeId, fetchTopics]);

  // Follow the rail: a ?tree= change navigates without remounting the page.
  useEffect(() => {
    if (treeParam && treeParam !== selectedTreeId) setSelectedTreeId(treeParam);
  }, [treeParam, selectedTreeId]);

  // Reopen the dialog when the rail deep-links with ?new=1.
  useEffect(() => {
    if (wantsCreate) setShowCreate(true);
  }, [wantsCreate]);

  // Bring a rail-deep-linked topic into view. Highlighting alone is not
  // enough — with a long list the target card is usually below the fold,
  // so the click appears to do nothing in the content area.
  useEffect(() => {
    if (!topicParam || topicsLoading) return;
    const card = document.getElementById(`topic-${topicParam}`);
    if (!card) return;
    const reduceMotion = window.matchMedia?.(
      '(prefers-reduced-motion: reduce)',
    ).matches;
    card.scrollIntoView({
      behavior: reduceMotion ? 'auto' : 'smooth',
      block: 'center',
    });
  }, [topicParam, topicsLoading, topics]);

  const handleTreeSelect = (treeId: string) => {
    setSelectedTreeId(treeId);
    storeTreeId(treeId); // keep the rail in sync
    const next = new URLSearchParams(searchParams);
    if (treeId) next.set('tree', treeId);
    else next.delete('tree');
    next.delete('topic');
    next.delete('new');
    setSearchParams(next, { replace: true });
  };

  const closeCreate = () => {
    setShowCreate(false);
    if (searchParams.has('new')) {
      const next = new URLSearchParams(searchParams);
      next.delete('new');
      setSearchParams(next, { replace: true });
    }
  };

  const handleDelete = async () => {
    if (!deleteTarget) return;
    try {
      await apiDelete(`/topics/${deleteTarget.id}`);
      setTopics((prev) => prev.filter((t) => t.id !== deleteTarget.id));
      notifyTopicsChanged(); // refresh the rail
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to archive topic');
    } finally {
      setDeleteTarget(null);
    }
  };

  const handleCreated = (topic: TopicSummary) => {
    setTopics((prev) => [topic, ...prev]);
    closeCreate();
    notifyTopicsChanged(); // refresh the rail
  };

  const filteredTopics = searchQuery
    ? topics.filter(
        (t) =>
          t.title.toLowerCase().includes(searchQuery.toLowerCase()) ||
          t.description?.toLowerCase().includes(searchQuery.toLowerCase()) ||
          t.slug.toLowerCase().includes(searchQuery.toLowerCase()),
      )
    : topics;

  return (
    <div className="p-6">
      {/* Header */}
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-content-primary">Topics</h1>
          <p className="text-sm text-content-muted mt-1">
            Named, searchable subgraphs with #references
          </p>
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={() => selectedTreeId && fetchTopics(selectedTreeId)}
            disabled={!selectedTreeId || topicsLoading}
            className="flex items-center gap-2 px-3 py-2 rounded-lg text-xs font-medium bg-surface-input hover:bg-surface-hover text-content-secondary ring-1 ring-inset ring-line-subtle transition-colors disabled:opacity-50"
          >
            <RefreshCw className={`w-3.5 h-3.5 ${topicsLoading ? 'animate-spin' : ''}`} />
            Refresh
          </button>
          <button
            onClick={() => setShowCreate(true)}
            disabled={!selectedTreeId}
            className="flex items-center gap-2 px-3 py-2 rounded-lg text-xs font-semibold text-white bg-accent-2-600 hover:bg-accent-2-500 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            <Plus className="w-3.5 h-3.5" />
            New Topic
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
          <button onClick={() => setError(null)} className="ml-auto">
            <X className="w-4 h-4" />
          </button>
        </div>
      )}

      {/* Tree Selector */}
      <div className="mb-6">
        <label htmlFor="topics-tree-select" className="block text-xs text-content-muted mb-2">Select Tree</label>
        <div className="relative max-w-md">
          <select
            id="topics-tree-select"
            value={selectedTreeId}
            onChange={(e) => handleTreeSelect(e.target.value)}
            className="w-full appearance-none bg-surface-input border border-line-subtle rounded-lg px-3 py-2 text-sm text-content-primary focus:outline-none focus:ring-2 focus:ring-accent/60 focus:border-accent cursor-pointer"
          >
            <option value="">Choose a tree...</option>
            {trees.map((t) => (
              <option key={t.id} value={t.id}>
                {t.title}
              </option>
            ))}
          </select>
          <ChevronDown className="absolute right-3 top-1/2 -translate-y-1/2 w-4 h-4 text-content-muted pointer-events-none" />
        </div>
      </div>

      {/* No tree selected */}
      {!selectedTreeId && (
        <div className="rounded-xl border border-line-subtle bg-surface-panel p-12 text-center">
          <Inbox className="w-10 h-10 text-content-faint/50 mx-auto mb-3" />
          <h2 className="text-sm font-medium text-content-secondary mb-1">Select a tree</h2>
          <p className="text-xs text-content-muted">
            Topics are scoped to conversation trees. Select a tree above to browse its topics.
          </p>
        </div>
      )}

      {/* Search */}
      {selectedTreeId && (
        <div className="flex items-center gap-3 mb-4">
          <div className="relative flex-1 max-w-md">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-content-muted" />
            <input
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="w-full bg-surface-input border border-line-subtle rounded-lg pl-9 pr-3 py-2 text-sm text-content-primary placeholder-content-faint focus:outline-none focus:ring-2 focus:ring-accent/60 focus:border-accent"
              placeholder="Search topics by title, description, or slug..."
            />
            {searchQuery && (
              <button
                onClick={() => setSearchQuery('')}
                className="absolute right-3 top-1/2 -translate-y-1/2 text-content-muted hover:text-content-primary"
              >
                <X className="w-3.5 h-3.5" />
              </button>
            )}
          </div>
          <span className="text-xs text-content-muted">
            {filteredTopics.length} topics
          </span>
        </div>
      )}

      {/* Loading */}
      {topicsLoading && (
        <div className="space-y-3">
          {[1, 2, 3].map((i) => (
            <div key={i} className="rounded-lg border border-line-subtle p-4 animate-pulse">
              <div className="h-4 bg-surface-input rounded w-48 mb-2" />
              <div className="h-3 bg-surface-input rounded w-72" />
            </div>
          ))}
        </div>
      )}

      {/* Empty */}
      {selectedTreeId && !topicsLoading && topics.length === 0 && (
        <div className="rounded-xl border border-line-subtle bg-surface-panel p-12 text-center">
          <Inbox className="w-10 h-10 text-content-faint/50 mx-auto mb-3" />
          <h2 className="text-sm font-medium text-content-secondary mb-1">No topics found</h2>
          <p className="text-xs text-content-muted mb-4">
            Create topic subgraphs from within the Tree View, or add one manually here.
          </p>
          <button
            onClick={() => setShowCreate(true)}
            className="inline-flex items-center gap-2 px-4 py-2 rounded-lg text-xs font-semibold text-white bg-accent-2-600 hover:bg-accent-2-500 transition-colors"
          >
            <Plus className="w-3.5 h-3.5" />
            Create Topic
          </button>
        </div>
      )}

      {/* Topic cards */}
      {selectedTreeId && !topicsLoading && filteredTopics.length > 0 && (
        <div className="space-y-3">
          {filteredTopics.map((topic) => (
            <TopicCard
              key={topic.id}
              topic={topic}
              highlighted={topic.id === topicParam}
              onDelete={() => setDeleteTarget(topic)}
            />
          ))}
        </div>
      )}

      {/* Create dialog */}
      {showCreate && selectedTreeId && (
        <CreateTopicDialog
          trees={trees}
          treeId={selectedTreeId}
          onClose={closeCreate}
          onCreated={handleCreated}
        />
      )}

      {/* Delete confirmation */}
      {deleteTarget && (
        <div className="fixed inset-0 z-50 flex items-start justify-center pt-[15vh]">
          <div className="absolute inset-0 bg-black/60" onClick={() => setDeleteTarget(null)} />
          <div className="relative glass-raised rounded-xl w-full max-w-sm mx-4">
            <div className="px-5 py-4 border-b border-line-subtle">
              <h2 className="text-sm font-medium text-content-primary">Archive Topic</h2>
            </div>
            <div className="px-5 py-4">
              <p className="text-sm text-content-secondary">
                Archive <span className="text-content-primary font-medium">"{deleteTarget.title}"</span>?
                This will soft-delete the topic.
              </p>
            </div>
            <div className="px-5 py-3 border-t border-line-subtle flex items-center justify-end gap-2">
              <button
                onClick={() => setDeleteTarget(null)}
                className="px-3 py-1.5 text-xs font-medium text-content-muted hover:text-content-primary rounded-lg hover:bg-surface-hover transition-colors"
              >
                Cancel
              </button>
              <button
                onClick={handleDelete}
                className="px-4 py-1.5 text-xs font-semibold text-white bg-rose-600 hover:bg-rose-500 rounded-lg transition-colors"
              >
                Archive
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
