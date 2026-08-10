/**
 * Hermes Canopy — Nodes Page
 *
 * Browse and manage nodes across conversation trees. Select a tree
 * to view its nodes as an indented hierarchy, edit content, follow a
 * node into its tree, and act on a selection in bulk.
 * Uses the tree-scoped node list endpoint (BUG-026).
 */

import { useState, useEffect, useCallback, useMemo } from 'react';
import {
  RefreshCw,
  AlertCircle,
  Inbox,
  Hash,
  MessageSquare,
  GitMerge,
  FileText,
  ChevronDown,
  Search,
  X,
} from 'lucide-react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { apiGet, apiPatch, apiDelete } from '../lib/api';
import { readStoredTreeId } from '../lib/activeTree';
import { NodeTreeRow, type TreeRowNode } from '../components/NodeTreeRow';
import { BulkActionBar } from '../components/BulkActionBar';
import RelatedPanel from '../components/RelatedPanel';
import {
  indexTopicTitles,
  nodeAuthorNames,
  nodeTypeLabel,
} from '../lib/nodeMeta';
import { buildHierarchy, filterHierarchy } from '../lib/nodeHierarchy';
import { disambiguateNodeIds } from '../lib/nodeShortId';
import {
  clearSelection,
  isBulkBarVisible,
  pruneSelection,
  selectAllState,
  toggleAllVisible,
  toggleSelection,
  type BulkActionId,
} from '../lib/nodeSelection';
import { countLabel, filteredCountLabel } from '../lib/pluralize';
import type { TopicSummary } from '../types/topic';

// ─── Types ─────────────────────────────────────────────────────────────

interface TreeSummary {
  id: string;
  title: string;
  description: string;
  node_count: number;
  root_node_id: string;
  created_at: string;
}

interface NodeDetail {
  id: string;
  treeId: string;
  parentId: string | null;
  authorId: string;
  authorDisplayName: string;
  content: string;
  contentFormat: string;
  nodeType: string;
  sequenceNum: number;
  metadata: unknown;
  depth: number;
  childCount: number;
  createdAt: string;
  editedAt: string | null;
  deletedAt: string | null;
}

interface ListNodesResult {
  nodes: NodeDetail[];
}

interface ListTreesResponse {
  trees: TreeSummary[];
  pagination: { total: number };
}

interface ListTopicsResponse {
  topics: TopicSummary[];
}

// ─── Helpers ───────────────────────────────────────────────────────────

function nodeTypeIcon(t: string) {
  switch (t) {
    case 'synthesis':
      return <GitMerge className="w-3.5 h-3.5 text-amber-400" />;
    case 'system':
      return <Hash className="w-3.5 h-3.5 text-blue-400" />;
    case 'card':
      return <FileText className="w-3.5 h-3.5 text-green-400" />;
    case 'topic':
      return <Hash className="w-3.5 h-3.5 text-purple-400" />;
    default:
      return <MessageSquare className="w-3.5 h-3.5 text-content-muted" />;
  }
}

// ─── Edit Node Dialog ──────────────────────────────────────────────────

function EditNodeDialog({
  node,
  onClose,
  onSaved,
}: {
  node: NodeDetail;
  onClose: () => void;
  onSaved: (updated: NodeDetail) => void;
}) {
  const [content, setContent] = useState(node.content);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleSave = async () => {
    if (!content.trim()) {
      setError('Content cannot be empty');
      return;
    }
    setLoading(true);
    setError(null);
    try {
      const updated = await apiPatch<NodeDetail>(`/nodes/${node.id}`, {
        content: content.trim(),
      });
      onSaved(updated);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update node');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center pt-[8vh]">
      <div className="absolute inset-0 bg-black/60" onClick={onClose} />
      <div className="relative glass-raised rounded-xl w-full max-w-lg mx-4">
        <div className="flex items-center justify-between px-5 py-4 border-b border-line-subtle">
          <h2 className="text-sm font-medium text-content-primary">Edit Node</h2>
          <button
            onClick={onClose}
            className="p-1 rounded-md text-content-muted hover:text-content-primary hover:bg-surface-hover"
          >
            <X className="w-4 h-4" />
          </button>
        </div>
        <div className="px-5 py-4 space-y-3">
          {error && (
            <div className="flex items-center gap-2 p-2 rounded bg-rose-500/10 border border-rose-500/30 text-status-danger text-xs">
              <AlertCircle className="w-3.5 h-3.5 flex-shrink-0" />
              {error}
            </div>
          )}
          <div className="flex items-center gap-2 text-xs text-content-muted">
            {nodeTypeIcon(node.nodeType)}
            <span>{nodeTypeLabel(node.nodeType)}</span>
            <span>·</span>
            <span className="font-mono">{node.id.slice(0, 8)}</span>
            <span>·</span>
            <span>Depth {node.depth}</span>
          </div>
          <textarea
            value={content}
            onChange={(e) => setContent(e.target.value)}
            rows={8}
            className="w-full bg-surface-input border border-line-subtle rounded-lg px-3 py-2 text-sm text-content-primary placeholder-content-faint resize-none focus:outline-none focus:ring-2 focus:ring-accent/60 focus:border-accent"
            placeholder="Node content..."
          />
        </div>
        <div className="px-5 py-3 border-t border-line-subtle flex items-center justify-end gap-2">
          <button
            onClick={onClose}
            className="px-3 py-1.5 text-xs font-medium text-content-muted hover:text-content-primary rounded-lg hover:bg-surface-hover transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={handleSave}
            disabled={loading}
            className="px-4 py-1.5 text-xs font-semibold text-white bg-accent-2-600 hover:bg-accent-2-500 rounded-lg transition-colors disabled:opacity-50"
          >
            {loading ? 'Saving...' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  );
}

// ─── Main Component ────────────────────────────────────────────────────

export default function NodesPage() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const treeParam = searchParams.get('tree') ?? '';
  const [trees, setTrees] = useState<TreeSummary[]>([]);
  // Preselect from deep link or the persisted active tree (TopicsPage/
  // TopicsRail parity — PAG-001/002 pagination means the newest page may
  // not contain a stored tree; the select is still corrected below).
  const [selectedTreeId, setSelectedTreeId] = useState<string | null>(
    () => treeParam || readStoredTreeId() || null,
  );
  const [nodes, setNodes] = useState<NodeDetail[]>([]);
  const [topics, setTopics] = useState<TopicSummary[]>([]);
  const [nodesLoading, setNodesLoading] = useState(false);
  const [treesLoading, setTreesLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [searchQuery, setSearchQuery] = useState('');

  // Edit/delete state
  const [editNode, setEditNode] = useState<NodeDetail | null>(null);
  const [deleteNodeId, setDeleteNodeId] = useState<string | null>(null);

  // Bulk selection (UI-08) — ids, so a re-render or refetch cannot strand
  // a stale node object in the selection. Pruned whenever the list moves.
  const [selection, setSelection] = useState<ReadonlySet<string>>(
    () => new Set<string>(),
  );
  const [bulkDeleting, setBulkDeleting] = useState(false);
  const [confirmBulkDelete, setConfirmBulkDelete] = useState(false);

  // Fetch trees for dropdown
  const fetchTrees = useCallback(async () => {
    setTreesLoading(true);
    try {
      const data = await apiGet<ListTreesResponse>('/trees?limit=100&sort=created_desc');
      let list = data.trees;
      // Pagination (PAG-001/002): a deep-linked/stored tree may predate the
      // first page — the select must still be able to display the active
      // tree, so fetch it by id and prepend it (VREG-001 durability).
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
    } finally {
      setTreesLoading(false);
    }
  }, [selectedTreeId]);

  useEffect(() => {
    void fetchTrees();
  }, [fetchTrees]);

  // Fetch nodes/topics for the initial preselected tree (deep link or
  // persisted active tree). Later changes flow through handleTreeSelect.
  useEffect(() => {
    if (selectedTreeId) {
      void fetchNodes(selectedTreeId);
      void fetchTopics(selectedTreeId);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Fetch nodes when tree is selected
  const fetchNodes = useCallback(async (treeId: string) => {
    setNodesLoading(true);
    setError(null);
    try {
      const result = await apiGet<ListNodesResult>(`/trees/${treeId}/nodes`);
      setNodes(Array.isArray(result.nodes) ? result.nodes : []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load nodes');
    } finally {
      setNodesLoading(false);
    }
  }, []);

  /**
   * Topic titles for the cards' `#topic` pills.
   *
   * A node's metadata may reference a topic by id or slug; this resolves
   * that to the real title. Best-effort — a failure here must not surface
   * an error banner or block the node list, the pills simply fall back to
   * the slug the metadata carried.
   */
  const fetchTopics = useCallback(async (treeId: string) => {
    try {
      const data = await apiGet<ListTopicsResponse>(
        `/topics?tree_id=${encodeURIComponent(treeId)}&limit=100`,
      );
      setTopics(Array.isArray(data.topics) ? data.topics : []);
    } catch {
      setTopics([]);
    }
  }, []);

  const handleTreeSelect = useCallback(
    (treeId: string) => {
      setSelectedTreeId(treeId);
      void fetchNodes(treeId);
      if (treeId) void fetchTopics(treeId);
      else setTopics([]);
    },
    [fetchNodes, fetchTopics],
  );

  const handleDeleteNode = async () => {
    if (!deleteNodeId) return;
    try {
      await apiDelete(`/nodes/${deleteNodeId}`);
      setNodes((prev) => prev.filter((n) => n.id !== deleteNodeId));
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete node');
    } finally {
      setDeleteNodeId(null);
    }
  };

  const handleNodeSaved = (updated: NodeDetail) => {
    setNodes((prev) => prev.map((n) => (n.id === updated.id ? { ...n, content: updated.content } : n)));
    setEditNode(null);
  };

  /*
   * Hierarchy (UI-08). `parentId` is on every row of the list payload, so
   * the tree structure is derived here rather than fetched — no extra
   * request, and it stays correct while a search narrows the list.
   *
   * Searching keeps a hit's ANCESTORS visible (dimmed) so a match is
   * shown in the branch it belongs to instead of as a context-free row;
   * `matched` distinguishes real hits from that context.
   */
  const query = searchQuery.trim().toLowerCase();

  const { rows, matched } = useMemo(() => {
    const source = nodes as TreeRowNode[];
    if (!query) {
      return { rows: buildHierarchy(source), matched: null as Set<string> | null };
    }
    const result = filterHierarchy(
      source,
      (n) =>
        n.content.toLowerCase().includes(query) ||
        n.nodeType.toLowerCase().includes(query) ||
        n.id.toLowerCase().includes(query),
    );
    return { rows: result.rows, matched: result.matched };
  }, [nodes, query]);

  const visibleIds = useMemo(() => rows.map((r) => r.id), [rows]);

  /*
   * Short ids that are actually distinguishing. A UUIDv7's first group is
   * the top 32 bits of a millisecond timestamp, so nodes seeded within
   * ~65s of each other share it — `019fb0c2` appeared on four rows of the
   * demo tree for exactly that reason. See lib/nodeShortId.
   */
  const shortIds = useMemo(
    () => disambiguateNodeIds(nodes.map((n) => n.id)),
    [nodes],
  );

  // Selection must survive re-renders but never outlive its rows.
  useEffect(() => {
    setSelection((prev) => {
      const next = pruneSelection(prev, nodes.map((n) => n.id));
      return next.size === prev.size ? prev : next;
    });
  }, [nodes]);

  const handleToggleSelect = useCallback((id: string) => {
    setSelection((prev) => toggleSelection(prev, id));
  }, []);

  const handleToggleAll = useCallback(() => {
    setSelection((prev) => toggleAllVisible(prev, visibleIds));
  }, [visibleIds]);

  const headerSelectState = selectAllState(selection, visibleIds);

  /**
   * Bulk delete. Deletes are issued per node against the one endpoint
   * that exists (`DELETE /nodes/{id}`, soft delete) — there is no bulk
   * route. Failures are collected rather than aborting the run, so a
   * single 404 does not strand the rest of the selection; whatever did
   * succeed is removed from the list.
   */
  const handleBulkDelete = useCallback(async () => {
    const ids = [...selection];
    if (ids.length === 0) return;

    setBulkDeleting(true);
    const deleted: string[] = [];
    const failed: string[] = [];

    for (const id of ids) {
      try {
        await apiDelete(`/nodes/${id}`);
        deleted.push(id);
      } catch {
        failed.push(id);
      }
    }

    if (deleted.length > 0) {
      const gone = new Set(deleted);
      setNodes((prev) => prev.filter((n) => !gone.has(n.id)));
    }
    setSelection(failed.length > 0 ? new Set(failed) : clearSelection());
    if (failed.length > 0) {
      setError(`Failed to delete ${countLabel(failed.length, 'node')}.`);
    }
    setBulkDeleting(false);
    setConfirmBulkDelete(false);
  }, [selection]);

  const handleBulkAction = useCallback((action: BulkActionId) => {
    // Merge and tag are rendered disabled (no endpoint) — see
    // lib/nodeSelection.bulkActions. Only delete can reach here.
    if (action === 'delete') setConfirmBulkDelete(true);
  }, []);

  // Author identities and topic titles are derived once per fetch, not
  // per card — every card would otherwise rebuild the same two maps.
  const authorNames = useMemo(() => nodeAuthorNames(nodes), [nodes]);
  const topicTitles = useMemo(() => indexTopicTitles(topics), [topics]);

  const openTopic = useCallback(
    (topicId: string | null) => {
      const params = new URLSearchParams();
      if (selectedTreeId) params.set('tree', selectedTreeId);
      if (topicId) params.set('topic', topicId);
      navigate(`/topics?${params.toString()}`);
    },
    [navigate, selectedTreeId],
  );

  /**
   * Related-panel drill-down (UI-REL-001). A parent/child session click
   * switches the page to that tree through the same selection mechanism
   * as the dropdown — the tree view, node list and related panel all
   * follow. The title is passed through so the panel can surface a tree
   * that is not in the page's list (e.g. a session imported elsewhere).
   */
  const handleNavigateToTree = useCallback(
    (treeId: string, title?: string) => {
      if (title) {
        setTrees((prev) =>
          prev.some((t) => t.id === treeId)
            ? prev
            : [
                ...prev,
                {
                  id: treeId,
                  title,
                  description: '',
                  node_count: 0,
                  root_node_id: '',
                  created_at: '',
                },
              ],
        );
      }
      handleTreeSelect(treeId);
    },
    [handleTreeSelect],
  );

  const selectedTree = trees.find((t) => t.id === selectedTreeId);

  return (
    <div className="p-6">
      {/* Header */}
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-content-primary">Nodes</h1>
          <p className="text-sm text-content-muted mt-1">
            Browse and manage nodes across conversation trees
          </p>
        </div>
        <button
          onClick={fetchTrees}
          disabled={treesLoading}
          className="flex items-center gap-2 px-3 py-2 rounded-lg text-xs font-medium bg-surface-input hover:bg-surface-hover text-content-secondary ring-1 ring-inset ring-line-subtle transition-colors disabled:opacity-50"
        >
          <RefreshCw className={`w-3.5 h-3.5 ${treesLoading ? 'animate-spin' : ''}`} />
          Refresh
        </button>
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
        <label htmlFor="nodes-tree-select" className="block text-xs text-content-muted mb-2">Select Tree</label>
        <div className="relative">
          <select
            id="nodes-tree-select"
            value={selectedTreeId ?? ''}
            onChange={(e) => handleTreeSelect(e.target.value)}
            className="w-full max-w-md appearance-none bg-surface-input border border-line-subtle rounded-lg px-3 py-2 text-sm text-content-primary focus:outline-none focus:ring-2 focus:ring-accent/60 focus:border-accent cursor-pointer"
          >
            <option value="">Choose a tree...</option>
            {trees.map((t) => (
              <option key={t.id} value={t.id}>
                {t.title} ({countLabel(t.node_count, 'node')})
              </option>
            ))}
          </select>
          <ChevronDown className="absolute right-3 top-1/2 -translate-y-1/2 w-4 h-4 text-content-muted pointer-events-none" />
        </div>
        {!treesLoading && trees.length === 0 && (
          <p className="text-xs text-content-muted mt-1">
            No trees available. Create one first on the Trees page.
          </p>
        )}
      </div>

      {/* Related panel (UI-REL-001) — session lineage for the selected
          tree: parent/child sessions, task/project/commit chips and
          delegation goals. Fetches GET /trees/{id} itself; renders
          nothing with no selection, compact empty state otherwise. */}
      {selectedTreeId && (
        <div className="mb-4 max-w-md">
          <RelatedPanel
            treeId={selectedTreeId}
            onNavigateToTree={handleNavigateToTree}
          />
        </div>
      )}

      {/* Search within nodes */}
      {selectedTree && (
        <div className="flex items-center gap-3 mb-4">
          <div className="relative flex-1 max-w-md">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-content-muted" />
            <input
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="w-full bg-surface-input border border-line-subtle rounded-lg pl-9 pr-3 py-2 text-sm text-content-primary placeholder-content-faint focus:outline-none focus:ring-2 focus:ring-accent/60 focus:border-accent"
              placeholder="Search nodes..."
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
            {filteredCountLabel(rows.length, nodes.length, 'node')}
          </span>
        </div>
      )}

      {/* No tree selected */}
      {!selectedTreeId && (
        <div className="rounded-xl border border-line-subtle bg-surface-panel p-12 text-center">
          <Inbox className="w-10 h-10 text-content-faint/50 mx-auto mb-3" />
          <h2 className="text-sm font-medium text-content-secondary mb-1">No tree selected</h2>
          <p className="text-xs text-content-muted">
            Select a conversation tree above to browse its nodes.
          </p>
        </div>
      )}

      {/* Loading nodes */}
      {nodesLoading && (
        <div className="space-y-3">
          {[1, 2, 3, 4].map((i) => (
            <div
              key={i}
              className="animate-pulse rounded-xl border border-line-subtle bg-surface-panel p-4"
            >
              <div className="flex items-start gap-3">
                <div className="h-7 w-7 shrink-0 rounded-full bg-surface-input" />
                <div className="flex-1">
                  <div className="mb-2 h-3.5 w-32 rounded bg-surface-input" />
                  <div className="h-3 w-48 rounded bg-surface-input" />
                </div>
              </div>
              <div className="mt-3 h-4 w-full max-w-lg rounded bg-surface-input" />
            </div>
          ))}
        </div>
      )}

      {/* Empty nodes */}
      {selectedTree && !nodesLoading && nodes.length === 0 && (
        <div className="rounded-xl border border-line-subtle bg-surface-panel p-12 text-center">
          <Inbox className="w-10 h-10 text-content-faint/50 mx-auto mb-3" />
          <h2 className="text-sm font-medium text-content-secondary mb-1">No nodes found</h2>
          <p className="text-xs text-content-muted">
            This tree has no nodes yet. Use the Tree View to add messages.
          </p>
        </div>
      )}

      {/* Node list — indented hierarchy (UI-08) */}
      {selectedTree && !nodesLoading && rows.length > 0 && (
        <section aria-label={`Nodes in ${selectedTree.title}`}>
          <div className="mb-3 flex items-center gap-3">
            <label className="flex cursor-pointer items-center gap-2 text-xs text-content-muted">
              <input
                type="checkbox"
                checked={headerSelectState === 'all'}
                ref={(el) => {
                  // Indeterminate is a DOM property, not an attribute —
                  // React cannot set it through JSX.
                  if (el) el.indeterminate = headerSelectState === 'some';
                }}
                onChange={handleToggleAll}
                data-testid="node-select-all"
                className="h-3.5 w-3.5 cursor-pointer accent-[var(--color-accent-2)]"
              />
              Select all
            </label>

            <h2 className="flex items-center gap-2 text-xs font-medium text-content-tertiary">
              {nodeTypeIcon('message')}
              {selectedTree.title} — {countLabel(nodes.length, 'node')}
            </h2>
          </div>

          <ul role="tree" aria-label="Node hierarchy" className="space-y-3">
            {rows.map((row) => (
              <NodeTreeRow
                key={row.id}
                row={row}
                shortId={shortIds.get(row.id) ?? row.id}
                selected={selection.has(row.id)}
                onToggleSelect={handleToggleSelect}
                isMatch={matched === null || matched.has(row.id)}
                searching={matched !== null}
                authorNames={authorNames}
                topicTitles={topicTitles}
                onEdit={() => setEditNode(row.node as NodeDetail)}
                onDelete={() => setDeleteNodeId(row.id)}
                onOpenTopic={openTopic}
              />
            ))}
          </ul>

          {isBulkBarVisible(selection) && (
            <BulkActionBar
              count={selection.size}
              onClear={() => setSelection(clearSelection())}
              onAction={handleBulkAction}
            />
          )}
        </section>
      )}

      {/* No search results — distinct from "this tree has no nodes" */}
      {selectedTree && !nodesLoading && nodes.length > 0 && rows.length === 0 && (
        <div className="rounded-xl border border-line-subtle bg-surface-panel p-12 text-center">
          <Search className="mx-auto mb-3 h-10 w-10 text-content-faint/50" />
          <h2 className="mb-1 text-sm font-medium text-content-secondary">
            No matching nodes
          </h2>
          <p className="text-xs text-content-muted">
            Nothing in this tree matches “{searchQuery}”.
          </p>
        </div>
      )}

      {/* Edit dialog */}
      {editNode && (
        <EditNodeDialog
          node={editNode}
          onClose={() => setEditNode(null)}
          onSaved={handleNodeSaved}
        />
      )}

      {/* Delete confirmation */}
      {deleteNodeId && (
        <div className="fixed inset-0 z-50 flex items-start justify-center pt-[15vh]">
          <div className="absolute inset-0 bg-black/60" onClick={() => setDeleteNodeId(null)} />
          <div className="relative glass-raised rounded-xl w-full max-w-sm mx-4">
            <div className="px-5 py-4 border-b border-line-subtle">
              <h2 className="text-sm font-medium text-content-primary">Delete Node</h2>
            </div>
            <div className="px-5 py-4">
              <p className="text-sm text-content-secondary">
                Are you sure you want to delete this node? This will soft-delete it.
              </p>
            </div>
            <div className="px-5 py-3 border-t border-line-subtle flex items-center justify-end gap-2">
              <button
                onClick={() => setDeleteNodeId(null)}
                className="px-3 py-1.5 text-xs font-medium text-content-muted hover:text-content-primary rounded-lg hover:bg-surface-hover transition-colors"
              >
                Cancel
              </button>
              <button
                onClick={handleDeleteNode}
                className="px-4 py-1.5 text-xs font-semibold text-white bg-rose-600 hover:bg-rose-500 rounded-lg transition-colors"
              >
                Delete
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Delete loading state */}
      {deleteNodeId && (
        <div className="fixed inset-0 z-[51] flex items-center justify-center bg-black/60">
          <p className="text-sm text-content-secondary">Deleting...</p>
        </div>
      )}

      {/* Bulk delete confirmation (UI-08) */}
      {confirmBulkDelete && (
        <div
          className="fixed inset-0 z-50 flex items-start justify-center pt-[15vh]"
          role="dialog"
          aria-modal="true"
          aria-label="Delete selected nodes"
        >
          <div
            className="absolute inset-0 bg-black/60"
            onClick={() => !bulkDeleting && setConfirmBulkDelete(false)}
          />
          <div className="glass-raised relative mx-4 w-full max-w-sm rounded-xl">
            <div className="border-b border-line-subtle px-5 py-4">
              <h2 className="text-sm font-medium text-content-primary">
                Delete {countLabel(selection.size, 'node')}
              </h2>
            </div>
            <div className="px-5 py-4">
              <p className="text-sm text-content-secondary">
                {bulkDeleting
                  ? 'Deleting…'
                  : `This will soft-delete ${countLabel(selection.size, 'node')}. Replies to a deleted node are not removed.`}
              </p>
            </div>
            <div className="flex items-center justify-end gap-2 border-t border-line-subtle px-5 py-3">
              <button
                onClick={() => setConfirmBulkDelete(false)}
                disabled={bulkDeleting}
                className="rounded-lg px-3 py-1.5 text-xs font-medium text-content-muted transition-colors hover:bg-surface-hover hover:text-content-primary disabled:opacity-50"
              >
                Cancel
              </button>
              <button
                onClick={handleBulkDelete}
                disabled={bulkDeleting}
                data-testid="bulk-delete-confirm"
                className="rounded-lg bg-rose-600 px-4 py-1.5 text-xs font-semibold text-white transition-colors hover:bg-rose-500 disabled:opacity-50"
              >
                {bulkDeleting ? 'Deleting…' : 'Delete'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
