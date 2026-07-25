/**
 * Hermes Canopy — Nodes Page
 *
 * Browse and manage nodes across conversation trees. Select a tree
 * to view its nodes, edit content, and delete nodes.
 * Uses the graph subtree endpoint to list nodes for a given tree.
 */

import { useState, useEffect, useCallback } from 'react';
import {
  RefreshCw,
  AlertCircle,
  Inbox,
  Hash,
  Clock,
  User,
  MessageSquare,
  GitMerge,
  FileText,
  Trash2,
  Edit3,
  ChevronDown,
  Search,
  X,
} from 'lucide-react';
import { apiGet, apiPatch, apiDelete } from '../lib/api';

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

interface SubtreeResult {
  nodes: NodeDetail[];
  edges: unknown[];
}

interface ListTreesResponse {
  trees: TreeSummary[];
  pagination: { total: number };
}

// ─── Helpers ───────────────────────────────────────────────────────────

function nodeTypeLabel(t: string): string {
  switch (t) {
    case 'message': return 'Message';
    case 'synthesis': return 'Synthesis';
    case 'system': return 'System';
    case 'card': return 'Card';
    case 'topic': return 'Topic';
    default: return t;
  }
}

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
      return <MessageSquare className="w-3.5 h-3.5 text-gray-400" />;
  }
}

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
      <div className="relative bg-gray-900 border border-gray-700 rounded-xl shadow-2xl w-full max-w-lg mx-4">
        <div className="flex items-center justify-between px-5 py-4 border-b border-gray-800">
          <h3 className="text-sm font-medium text-gray-200">Edit Node</h3>
          <button
            onClick={onClose}
            className="p-1 rounded-md text-gray-500 hover:text-gray-300 hover:bg-gray-800"
          >
            <X className="w-4 h-4" />
          </button>
        </div>
        <div className="px-5 py-4 space-y-3">
          {error && (
            <div className="flex items-center gap-2 p-2 rounded bg-red-500/10 border border-red-500/30 text-red-400 text-xs">
              <AlertCircle className="w-3.5 h-3.5 flex-shrink-0" />
              {error}
            </div>
          )}
          <div className="flex items-center gap-2 text-xs text-gray-500">
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
            className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 placeholder-gray-600 resize-none focus:outline-none focus:ring-2 focus:ring-purple-500/50 focus:border-purple-500"
            placeholder="Node content..."
          />
        </div>
        <div className="px-5 py-3 border-t border-gray-800 flex items-center justify-end gap-2">
          <button
            onClick={onClose}
            className="px-3 py-1.5 text-xs font-medium text-gray-400 hover:text-gray-200 rounded-lg hover:bg-gray-800 transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={handleSave}
            disabled={loading}
            className="px-4 py-1.5 text-xs font-semibold text-white bg-purple-600 hover:bg-purple-700 rounded-lg transition-colors disabled:opacity-50"
          >
            {loading ? 'Saving...' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  );
}

// ─── Node Row ──────────────────────────────────────────────────────────

function NodeRow({
  node,
  onEdit,
  onDelete,
}: {
  node: NodeDetail;
  onEdit: () => void;
  onDelete: () => void;
}) {
  return (
    <div className="flex items-start gap-3 px-4 py-3 border-b border-gray-800/50 last:border-b-0 hover:bg-gray-800/30 transition-colors group">
      <div className="flex-shrink-0 mt-0.5">
        {nodeTypeIcon(node.nodeType)}
      </div>
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2 mb-0.5">
          <span className="text-xs font-medium text-gray-400 uppercase">
            {nodeTypeLabel(node.nodeType)}
          </span>
          <span className="text-[10px] text-gray-600 font-mono">
            {node.id.slice(0, 8)}
          </span>
          <span className="text-[10px] text-gray-600">
            depth {node.depth} · {node.childCount} children
          </span>
        </div>
        <p className="text-sm text-gray-300 line-clamp-2">
          {node.content}
        </p>
        <div className="flex items-center gap-3 mt-1 text-[11px] text-gray-600">
          <span className="flex items-center gap-1">
            <User className="w-3 h-3" />
            {node.authorDisplayName || node.authorId.slice(0, 8)}
          </span>
          <span className="flex items-center gap-1">
            <Clock className="w-3 h-3" />
            {formatTimeAgo(node.createdAt)}
          </span>
          {node.editedAt && (
            <span className="text-amber-500/70">(edited)</span>
          )}
        </div>
      </div>
      <div className="flex items-center gap-1 flex-shrink-0 opacity-0 group-hover:opacity-100 transition-opacity">
        <button
          onClick={onEdit}
          className="p-1.5 rounded-md text-gray-600 hover:text-purple-400 hover:bg-purple-500/10"
          title="Edit content"
          aria-label={`Edit node ${node.id.slice(0, 8)}`}
        >
          <Edit3 className="w-3.5 h-3.5" />
        </button>
        <button
          onClick={onDelete}
          className="p-1.5 rounded-md text-gray-600 hover:text-red-400 hover:bg-red-500/10"
          title="Delete node"
          aria-label={`Delete node ${node.id.slice(0, 8)}`}
        >
          <Trash2 className="w-3.5 h-3.5" />
        </button>
      </div>
    </div>
  );
}

// ─── Main Component ────────────────────────────────────────────────────

export default function NodesPage() {
  const [trees, setTrees] = useState<TreeSummary[]>([]);
  const [selectedTreeId, setSelectedTreeId] = useState<string | null>(null);
  const [nodes, setNodes] = useState<NodeDetail[]>([]);
  const [nodesLoading, setNodesLoading] = useState(false);
  const [treesLoading, setTreesLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [searchQuery, setSearchQuery] = useState('');

  // Edit/delete state
  const [editNode, setEditNode] = useState<NodeDetail | null>(null);
  const [deleteNodeId, setDeleteNodeId] = useState<string | null>(null);

  // Fetch trees for dropdown
  const fetchTrees = useCallback(async () => {
    setTreesLoading(true);
    try {
      const data = await apiGet<ListTreesResponse>('/trees?limit=100&sort=created_desc');
      setTrees(data.trees);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load trees');
    } finally {
      setTreesLoading(false);
    }
  }, []);

  useEffect(() => {
    void fetchTrees();
  }, [fetchTrees]);

  // Fetch nodes when tree is selected
  const fetchNodes = useCallback(async (treeId: string) => {
    setNodesLoading(true);
    setError(null);
    try {
      // Get tree detail first to find root node
      const tree = await apiGet<TreeSummary>(`/trees/${treeId}?include_stats=false`);
      if (!tree.root_node_id) {
        setNodes([]);
        return;
      }
      // Use graph subtree endpoint to get all nodes
      const result = await apiGet<SubtreeResult>(
        `/graph/trees/${treeId}/subtree/${tree.root_node_id}?max_depth=0`,
      );
      setNodes(Array.isArray(result.nodes) ? result.nodes : []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load nodes');
    } finally {
      setNodesLoading(false);
    }
  }, []);

  const handleTreeSelect = (treeId: string) => {
    setSelectedTreeId(treeId);
    void fetchNodes(treeId);
  };

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

  // Filter nodes by search
  const filteredNodes = searchQuery
    ? nodes.filter(
        (n) =>
          n.content.toLowerCase().includes(searchQuery.toLowerCase()) ||
          n.nodeType.toLowerCase().includes(searchQuery.toLowerCase()) ||
          n.id.includes(searchQuery),
      )
    : nodes;

  const selectedTree = trees.find((t) => t.id === selectedTreeId);

  return (
    <div className="p-6">
      {/* Header */}
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-bold text-gray-100">Nodes</h1>
          <p className="text-sm text-gray-500 mt-1">
            Browse and manage nodes across conversation trees
          </p>
        </div>
        <button
          onClick={fetchTrees}
          disabled={treesLoading}
          className="flex items-center gap-2 px-3 py-2 rounded-lg text-xs font-medium bg-gray-800 hover:bg-gray-700 text-gray-300 transition-colors disabled:opacity-50"
        >
          <RefreshCw className={`w-3.5 h-3.5 ${treesLoading ? 'animate-spin' : ''}`} />
          Refresh
        </button>
      </div>

      {/* Error banner */}
      {error && (
        <div
          className="flex items-center gap-2 mb-4 p-3 rounded-lg bg-red-500/10 border border-red-500/30 text-red-400 text-sm"
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
        <label htmlFor="nodes-tree-select" className="block text-xs text-gray-500 mb-2">Select Tree</label>
        <div className="relative">
          <select
            id="nodes-tree-select"
            value={selectedTreeId ?? ''}
            onChange={(e) => handleTreeSelect(e.target.value)}
            className="w-full max-w-md appearance-none bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 focus:outline-none focus:ring-2 focus:ring-purple-500/50 focus:border-purple-500 cursor-pointer"
          >
            <option value="">Choose a tree...</option>
            {trees.map((t) => (
              <option key={t.id} value={t.id}>
                {t.title} ({t.node_count} nodes)
              </option>
            ))}
          </select>
          <ChevronDown className="absolute right-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-500 pointer-events-none" />
        </div>
        {!treesLoading && trees.length === 0 && (
          <p className="text-xs text-gray-600 mt-1">
            No trees available. Create one first on the Trees page.
          </p>
        )}
      </div>

      {/* Search within nodes */}
      {selectedTree && (
        <div className="flex items-center gap-3 mb-4">
          <div className="relative flex-1 max-w-md">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-gray-500" />
            <input
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="w-full bg-gray-800 border border-gray-700 rounded-lg pl-9 pr-3 py-2 text-sm text-gray-200 placeholder-gray-600 focus:outline-none focus:ring-2 focus:ring-purple-500/50 focus:border-purple-500"
              placeholder="Search nodes..."
            />
            {searchQuery && (
              <button
                onClick={() => setSearchQuery('')}
                className="absolute right-3 top-1/2 -translate-y-1/2 text-gray-500 hover:text-gray-300"
              >
                <X className="w-3.5 h-3.5" />
              </button>
            )}
          </div>
          <span className="text-xs text-gray-600">
            {filteredNodes.length} of {nodes.length} nodes
          </span>
        </div>
      )}

      {/* No tree selected */}
      {!selectedTreeId && (
        <div className="rounded-lg border border-gray-800 bg-gray-900 p-12 text-center">
          <Inbox className="w-10 h-10 text-gray-700 mx-auto mb-3" />
          <h3 className="text-sm font-medium text-gray-400 mb-1">No tree selected</h3>
          <p className="text-xs text-gray-600">
            Select a conversation tree above to browse its nodes.
          </p>
        </div>
      )}

      {/* Loading nodes */}
      {nodesLoading && (
        <div className="space-y-2">
          {[1, 2, 3, 4].map((i) => (
            <div key={i} className="rounded-lg p-4 animate-pulse">
              <div className="h-3 bg-gray-800 rounded w-20 mb-2" />
              <div className="h-4 bg-gray-800 rounded w-96 mb-1" />
              <div className="h-3 bg-gray-800 rounded w-32" />
            </div>
          ))}
        </div>
      )}

      {/* Empty nodes */}
      {selectedTree && !nodesLoading && nodes.length === 0 && (
        <div className="rounded-lg border border-gray-800 bg-gray-900 p-12 text-center">
          <Inbox className="w-10 h-10 text-gray-700 mx-auto mb-3" />
          <h3 className="text-sm font-medium text-gray-400 mb-1">No nodes found</h3>
          <p className="text-xs text-gray-600">
            This tree has no nodes yet. Use the Tree View to add messages.
          </p>
        </div>
      )}

      {/* Node list */}
      {selectedTree && !nodesLoading && filteredNodes.length > 0 && (
        <div className="rounded-lg border border-gray-800 bg-gray-900/50 overflow-hidden">
          <div className="px-4 py-3 border-b border-gray-800 flex items-center justify-between">
            <h3 className="text-xs font-medium text-gray-400">
              {selectedTree.title} — {nodes.length} nodes
            </h3>
          </div>
          {filteredNodes.map((node) => (
            <NodeRow
              key={node.id}
              node={node}
              onEdit={() => setEditNode(node)}
              onDelete={() => setDeleteNodeId(node.id)}
            />
          ))}
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
          <div className="relative bg-gray-900 border border-gray-700 rounded-xl shadow-2xl w-full max-w-sm mx-4">
            <div className="px-5 py-4 border-b border-gray-800">
              <h3 className="text-sm font-medium text-gray-200">Delete Node</h3>
            </div>
            <div className="px-5 py-4">
              <p className="text-sm text-gray-400">
                Are you sure you want to delete this node? This will soft-delete it.
              </p>
            </div>
            <div className="px-5 py-3 border-t border-gray-800 flex items-center justify-end gap-2">
              <button
                onClick={() => setDeleteNodeId(null)}
                className="px-3 py-1.5 text-xs font-medium text-gray-400 hover:text-gray-200 rounded-lg hover:bg-gray-800 transition-colors"
              >
                Cancel
              </button>
              <button
                onClick={handleDeleteNode}
                className="px-4 py-1.5 text-xs font-semibold text-white bg-red-600 hover:bg-red-700 rounded-lg transition-colors"
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
          <p className="text-sm text-gray-400">Deleting...</p>
        </div>
      )}
    </div>
  );
}
