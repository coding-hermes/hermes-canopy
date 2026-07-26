/**
 * Hermes Canopy — Topics Page
 *
 * Full CRUD for topics. Topics are named, searchable subgraphs
 * anchored to a specific root node within a conversation tree.
 */

import { useState, useEffect, useCallback } from 'react';
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

// ─── Types ─────────────────────────────────────────────────────────────

interface TopicSummary {
  id: string;
  tree_id: string;
  root_node_id: string;
  title: string;
  description: string;
  slug: string;
  status: string;
  node_count: number;
  created_at: string;
}

interface ListTopicsResponse {
  topics: TopicSummary[];
}

interface TreeSummary {
  id: string;
  title: string;
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
  archived: 'bg-gray-600',
  draft: 'bg-amber-400',
};

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
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

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
      <div className="relative bg-gray-900 border border-gray-700 rounded-xl shadow-2xl w-full max-w-md mx-4">
        <div className="px-5 py-4 border-b border-gray-800">
          <h3 className="text-sm font-medium text-gray-200">Create Topic</h3>
        </div>
        <div className="px-5 py-4 space-y-3">
          {error && (
            <div className="flex items-center gap-2 p-2 rounded bg-red-500/10 border border-red-500/30 text-red-400 text-xs">
              <AlertCircle className="w-3.5 h-3.5 flex-shrink-0" />
              {error}
            </div>
          )}
          <div>
            <label className="block text-xs text-gray-400 mb-1">Subject Tree</label>
            <select
              value={treeId}
              disabled
              className="w-full bg-gray-800/50 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-400"
            >
              <option value={treeId}>
                {trees.find((t) => t.id === treeId)?.title ?? treeId}
              </option>
            </select>
          </div>
          <div>
            <label className="block text-xs text-gray-400 mb-1">Root Node ID *</label>
            <input
              value={rootNodeId}
              onChange={(e) => setRootNodeId(e.target.value)}
              className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 placeholder-gray-600 font-mono focus:outline-none focus:ring-2 focus:ring-purple-500/50 focus:border-purple-500"
              placeholder="UUID of the root node"
            />
          </div>
          <div>
            <label className="block text-xs text-gray-400 mb-1">Title *</label>
            <input
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 placeholder-gray-600 focus:outline-none focus:ring-2 focus:ring-purple-500/50 focus:border-purple-500"
              placeholder="Topic title"
              autoFocus
              onKeyDown={(e) => e.key === 'Enter' && handleCreate()}
            />
          </div>
          <div>
            <label className="block text-xs text-gray-400 mb-1">Description</label>
            <textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={2}
              className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 placeholder-gray-600 resize-none focus:outline-none focus:ring-2 focus:ring-purple-500/50 focus:border-purple-500"
              placeholder="Optional description..."
            />
          </div>
        </div>
        <div className="px-5 py-3 border-t border-gray-800 flex items-center justify-end gap-2">
          <button
            onClick={onClose}
            className="px-3 py-1.5 text-xs font-medium text-gray-400 hover:text-gray-200 rounded-lg hover:bg-gray-800 transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={handleCreate}
            disabled={loading || !title.trim() || !rootNodeId.trim()}
            className="px-4 py-1.5 text-xs font-semibold text-white bg-purple-600 hover:bg-purple-700 rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
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
  onDelete,
}: {
  topic: TopicSummary;
  onDelete: () => void;
}) {
  const dotColor = STATUS_STYLES[topic.status] ?? 'bg-gray-500';

  return (
    <div className="rounded-lg border border-gray-800 bg-gray-900/50 p-4 hover:border-gray-600 group transition-colors">
      <div className="flex items-start justify-between gap-3">
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 mb-1">
            <div className={`w-2 h-2 rounded-full flex-shrink-0 ${dotColor}`} />
            <h3 className="text-sm font-medium text-gray-200 truncate">
              {topic.title}
            </h3>
            <span className="text-[10px] text-gray-600 uppercase bg-gray-800 rounded px-1.5 py-0.5">
              {topic.status}
            </span>
          </div>
          {topic.description && (
            <p className="text-xs text-gray-500 mt-1 line-clamp-2">
              {topic.description}
            </p>
          )}
          <div className="flex items-center gap-3 mt-2 text-[11px] text-gray-600">
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
            className="p-1.5 rounded-md text-gray-600 hover:text-red-400 hover:bg-red-500/10 transition-colors"
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
  const [trees, setTrees] = useState<TreeSummary[]>([]);
  const [selectedTreeId, setSelectedTreeId] = useState<string>('');
  const [topics, setTopics] = useState<TopicSummary[]>([]);
  const [topicsLoading, setTopicsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [searchQuery, setSearchQuery] = useState('');
  const [showCreate, setShowCreate] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<TopicSummary | null>(null);

  const fetchTrees = useCallback(async () => {
    // Loading state handled by trees array being empty
    try {
      const data = await apiGet<ListTreesResponse>('/trees?limit=100');
      setTrees(data.trees);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load trees');
    }
  }, []);

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

  const handleTreeSelect = (treeId: string) => {
    setSelectedTreeId(treeId);
    if (treeId) void fetchTopics(treeId);
  };

  const handleDelete = async () => {
    if (!deleteTarget) return;
    try {
      await apiDelete(`/topics/${deleteTarget.id}`);
      setTopics((prev) => prev.filter((t) => t.id !== deleteTarget.id));
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to archive topic');
    } finally {
      setDeleteTarget(null);
    }
  };

  const handleCreated = (topic: TopicSummary) => {
    setTopics((prev) => [topic, ...prev]);
    setShowCreate(false);
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
          <h1 className="text-2xl font-bold text-gray-900">Topics</h1>
          <p className="text-sm text-gray-500 mt-1">
            Named, searchable subgraphs with #references
          </p>
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={() => selectedTreeId && fetchTopics(selectedTreeId)}
            disabled={!selectedTreeId || topicsLoading}
            className="flex items-center gap-2 px-3 py-2 rounded-lg text-xs font-medium bg-gray-800 hover:bg-gray-700 text-gray-300 transition-colors disabled:opacity-50"
          >
            <RefreshCw className={`w-3.5 h-3.5 ${topicsLoading ? 'animate-spin' : ''}`} />
            Refresh
          </button>
          <button
            onClick={() => setShowCreate(true)}
            disabled={!selectedTreeId}
            className="flex items-center gap-2 px-3 py-2 rounded-lg text-xs font-semibold text-white bg-purple-600 hover:bg-purple-700 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            <Plus className="w-3.5 h-3.5" />
            New Topic
          </button>
        </div>
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
        <label htmlFor="topics-tree-select" className="block text-xs text-gray-500 mb-2">Select Tree</label>
        <div className="relative max-w-md">
          <select
            id="topics-tree-select"
            value={selectedTreeId}
            onChange={(e) => handleTreeSelect(e.target.value)}
            className="w-full appearance-none bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 focus:outline-none focus:ring-2 focus:ring-purple-500/50 focus:border-purple-500 cursor-pointer"
          >
            <option value="">Choose a tree...</option>
            {trees.map((t) => (
              <option key={t.id} value={t.id}>
                {t.title}
              </option>
            ))}
          </select>
          <ChevronDown className="absolute right-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-500 pointer-events-none" />
        </div>
      </div>

      {/* No tree selected */}
      {!selectedTreeId && (
        <div className="rounded-lg border border-gray-800 bg-gray-900 p-12 text-center">
          <Inbox className="w-10 h-10 text-gray-700 mx-auto mb-3" />
          <h3 className="text-sm font-medium text-gray-400 mb-1">Select a tree</h3>
          <p className="text-xs text-gray-600">
            Topics are scoped to conversation trees. Select a tree above to browse its topics.
          </p>
        </div>
      )}

      {/* Search */}
      {selectedTreeId && (
        <div className="flex items-center gap-3 mb-4">
          <div className="relative flex-1 max-w-md">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-gray-500" />
            <input
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="w-full bg-gray-800 border border-gray-700 rounded-lg pl-9 pr-3 py-2 text-sm text-gray-200 placeholder-gray-600 focus:outline-none focus:ring-2 focus:ring-purple-500/50 focus:border-purple-500"
              placeholder="Search topics by title, description, or slug..."
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
            {filteredTopics.length} topics
          </span>
        </div>
      )}

      {/* Loading */}
      {topicsLoading && (
        <div className="space-y-3">
          {[1, 2, 3].map((i) => (
            <div key={i} className="rounded-lg border border-gray-800 p-4 animate-pulse">
              <div className="h-4 bg-gray-800 rounded w-48 mb-2" />
              <div className="h-3 bg-gray-800 rounded w-72" />
            </div>
          ))}
        </div>
      )}

      {/* Empty */}
      {selectedTreeId && !topicsLoading && topics.length === 0 && (
        <div className="rounded-lg border border-gray-800 bg-gray-900 p-12 text-center">
          <Inbox className="w-10 h-10 text-gray-700 mx-auto mb-3" />
          <h3 className="text-sm font-medium text-gray-400 mb-1">No topics found</h3>
          <p className="text-xs text-gray-600 mb-4">
            Create topic subgraphs from within the Tree View, or add one manually here.
          </p>
          <button
            onClick={() => setShowCreate(true)}
            className="inline-flex items-center gap-2 px-4 py-2 rounded-lg text-xs font-semibold text-white bg-purple-600 hover:bg-purple-700 transition-colors"
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
          onClose={() => setShowCreate(false)}
          onCreated={handleCreated}
        />
      )}

      {/* Delete confirmation */}
      {deleteTarget && (
        <div className="fixed inset-0 z-50 flex items-start justify-center pt-[15vh]">
          <div className="absolute inset-0 bg-black/60" onClick={() => setDeleteTarget(null)} />
          <div className="relative bg-gray-900 border border-gray-700 rounded-xl shadow-2xl w-full max-w-sm mx-4">
            <div className="px-5 py-4 border-b border-gray-800">
              <h3 className="text-sm font-medium text-gray-200">Archive Topic</h3>
            </div>
            <div className="px-5 py-4">
              <p className="text-sm text-gray-400">
                Archive <span className="text-gray-200 font-medium">"{deleteTarget.title}"</span>?
                This will soft-delete the topic.
              </p>
            </div>
            <div className="px-5 py-3 border-t border-gray-800 flex items-center justify-end gap-2">
              <button
                onClick={() => setDeleteTarget(null)}
                className="px-3 py-1.5 text-xs font-medium text-gray-400 hover:text-gray-200 rounded-lg hover:bg-gray-800 transition-colors"
              >
                Cancel
              </button>
              <button
                onClick={handleDelete}
                className="px-4 py-1.5 text-xs font-semibold text-white bg-red-600 hover:bg-red-700 rounded-lg transition-colors"
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
