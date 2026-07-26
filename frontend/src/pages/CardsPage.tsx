/**
 * Hermes Canopy — Cards Page
 *
 * Full CRUD for cards. Cards are graph nodes with structured data
 * and interactive behavior. Three types: compact, expanded, iteration.
 */

import { useState, useEffect, useCallback } from 'react';
import {
  Plus,
  Trash2,
  RefreshCw,
  Clock,
  AlertCircle,
  Inbox,
  ChevronDown,
  Search,
  X,
  FileText,
  Layout,
  RotateCw,
} from 'lucide-react';
import { apiGet, apiPost, apiDelete } from '../lib/api';

// ─── Types ─────────────────────────────────────────────────────────────

interface CardSummary {
  id: string;
  tree_id: string;
  node_id: string;
  app_id: string;
  type: string;
  status: string;
  context_hash: string;
  data: unknown;
  actions: unknown[];
  last_event_seq: number;
  created_at: string;
}

interface ListCardsResponse {
  cards: CardSummary[];
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

function cardTypeIcon(t: string) {
  switch (t) {
    case 'compact':
      return <FileText className="w-3.5 h-3.5 text-blue-400" />;
    case 'expanded':
      return <Layout className="w-3.5 h-3.5 text-purple-400" />;
    case 'iteration':
      return <RotateCw className="w-3.5 h-3.5 text-amber-400" />;
    default:
      return <FileText className="w-3.5 h-3.5 text-gray-400" />;
  }
}

function cardTypeLabel(t: string): string {
  switch (t) {
    case 'compact': return 'Compact';
    case 'expanded': return 'Expanded';
    case 'iteration': return 'Iteration';
    default: return t;
  }
}

const STATUS_STYLES: Record<string, string> = {
  active: 'bg-green-400',
  dismissed: 'bg-gray-600',
  archived: 'bg-gray-500',
};

const CARD_TYPES = ['compact', 'expanded', 'iteration'] as const;

// ─── Create Card Dialog ────────────────────────────────────────────────

function CreateCardDialog({
  trees,
  treeId,
  onClose,
  onCreated,
}: {
  trees: TreeSummary[];
  treeId: string;
  onClose: () => void;
  onCreated: (card: CardSummary) => void;
}) {
  const [cardType, setCardType] = useState<string>('compact');
  const [nodeId, setNodeId] = useState('');
  const [appId, setAppId] = useState('canopy');
  const [dataJson, setDataJson] = useState('{}');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleCreate = async () => {
    if (!treeId) {
      setError('Tree is required');
      return;
    }
    if (!nodeId.trim()) {
      setError('Node ID is required');
      return;
    }
    let parsed;
    try {
      parsed = JSON.parse(dataJson);
    } catch {
      setError('Data must be valid JSON');
      return;
    }
    setLoading(true);
    setError(null);
    try {
      const card = await apiPost<CardSummary>('/cards', {
        treeId,
        nodeId: nodeId.trim(),
        appId: appId.trim() || 'canopy',
        cardType,
        data: parsed,
      });
      onCreated(card);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create card');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center pt-[10vh]">
      <div className="absolute inset-0 bg-black/60" onClick={onClose} />
      <div className="relative bg-gray-900 border border-gray-700 rounded-xl shadow-2xl w-full max-w-md mx-4">
        <div className="px-5 py-4 border-b border-gray-800">
          <h3 className="text-sm font-medium text-gray-200">Create Card</h3>
        </div>
        <div className="px-5 py-4 space-y-3">
          {error && (
            <div className="flex items-center gap-2 p-2 rounded bg-red-500/10 border border-red-500/30 text-red-400 text-xs">
              <AlertCircle className="w-3.5 h-3.5 flex-shrink-0" />
              {error}
            </div>
          )}
          <div>
            <label className="block text-xs text-gray-400 mb-1">Tree</label>
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
            <label className="block text-xs text-gray-400 mb-1">Card Type</label>
            <div className="flex gap-1">
              {CARD_TYPES.map((ct) => (
                <button
                  key={ct}
                  onClick={() => setCardType(ct)}
                  className={`px-3 py-1.5 rounded-lg text-xs font-medium capitalize transition-colors ${
                    cardType === ct
                      ? 'bg-purple-600 text-white'
                      : 'bg-gray-800 text-gray-400 hover:text-gray-200'
                  }`}
                >
                  {ct}
                </button>
              ))}
            </div>
          </div>
          <div>
            <label className="block text-xs text-gray-400 mb-1">Node ID *</label>
            <input
              value={nodeId}
              onChange={(e) => setNodeId(e.target.value)}
              className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 placeholder-gray-600 font-mono focus:outline-none focus:ring-2 focus:ring-purple-500/50 focus:border-purple-500"
              placeholder="UUID of the node to attach card to"
            />
          </div>
          <div>
            <label className="block text-xs text-gray-400 mb-1">App ID</label>
            <input
              value={appId}
              onChange={(e) => setAppId(e.target.value)}
              className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 placeholder-gray-600 focus:outline-none focus:ring-2 focus:ring-purple-500/50 focus:border-purple-500"
              placeholder="canopy"
            />
          </div>
          <div>
            <label className="block text-xs text-gray-400 mb-1">Data (JSON) *</label>
            <textarea
              value={dataJson}
              onChange={(e) => setDataJson(e.target.value)}
              rows={4}
              className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 placeholder-gray-600 font-mono resize-none focus:outline-none focus:ring-2 focus:ring-purple-500/50 focus:border-purple-500"
              placeholder='{"key": "value"}'
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
            disabled={loading || !nodeId.trim()}
            className="px-4 py-1.5 text-xs font-semibold text-white bg-purple-600 hover:bg-purple-700 rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {loading ? 'Creating...' : 'Create'}
          </button>
        </div>
      </div>
    </div>
  );
}

// ─── Card Row ──────────────────────────────────────────────────────────

function CardRow({
  card,
  onDelete,
}: {
  card: CardSummary;
  onDelete: () => void;
}) {
  const dotColor = STATUS_STYLES[card.status] ?? 'bg-gray-500';

  return (
    <div className="rounded-lg border border-gray-800 bg-gray-900/50 p-4 hover:border-gray-600 group transition-colors">
      <div className="flex items-start justify-between gap-3">
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 mb-1">
            <div className={`w-2 h-2 rounded-full flex-shrink-0 ${dotColor}`} />
            {cardTypeIcon(card.type)}
            <h3 className="text-sm font-medium text-gray-200">
              {cardTypeLabel(card.type)} Card
            </h3>
            <span className="text-[10px] text-gray-600 uppercase bg-gray-800 rounded px-1.5 py-0.5">
              {card.status}
            </span>
          </div>
          <div className="flex items-center gap-3 mt-1 text-[11px] text-gray-600">
            <span className="font-mono text-[10px]">
              node: {card.node_id.slice(0, 8)}...
            </span>
            <span className="text-gray-700">|</span>
            <span>{card.app_id}</span>
            <span className="flex items-center gap-1">
              <Clock className="w-3 h-3" />
              {formatTimeAgo(card.created_at)}
            </span>
          </div>
          {card.data != null ? (
            <pre className="mt-2 text-[10px] text-gray-500 font-mono bg-gray-800/50 rounded p-2 overflow-x-auto line-clamp-3">
              {String(JSON.stringify(card.data, null, 2))}
            </pre>
          ) : null}
        </div>
        <div className="flex items-center gap-1 flex-shrink-0">
          <button
            onClick={(e) => {
              e.stopPropagation();
              onDelete();
            }}
            className="p-1.5 rounded-md text-gray-600 hover:text-red-400 hover:bg-red-500/10 transition-colors"
            title="Archive card"
            aria-label={`Archive card`}
          >
            <Trash2 className="w-4 h-4" />
          </button>
        </div>
      </div>
    </div>
  );
}

// ─── Main Component ────────────────────────────────────────────────────

export default function CardsPage() {
  const [trees, setTrees] = useState<TreeSummary[]>([]);
  const [selectedTreeId, setSelectedTreeId] = useState<string>('');
  const [cards, setCards] = useState<CardSummary[]>([]);
  const [cardsLoading, setCardsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [searchQuery, setSearchQuery] = useState('');
  const [typeFilter, setTypeFilter] = useState<string>('');
  const [showCreate, setShowCreate] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<CardSummary | null>(null);

  const fetchTrees = useCallback(async () => {
    // Loading state handled by cards array being empty
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

  const fetchCards = useCallback(async (treeId: string, cardType?: string) => {
    if (!treeId) return;
    setCardsLoading(true);
    setError(null);
    try {
      const params = new URLSearchParams({ tree_id: treeId, limit: '100' });
      if (cardType) params.set('card_type', cardType);
      const data = await apiGet<ListCardsResponse>(`/cards?${params.toString()}`);
      setCards(data.cards);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load cards');
    } finally {
      setCardsLoading(false);
    }
  }, []);

  const handleTreeSelect = (treeId: string) => {
    setSelectedTreeId(treeId);
    if (treeId) void fetchCards(treeId, typeFilter || undefined);
  };

  const handleTypeFilter = (t: string) => {
    setTypeFilter(t);
    if (selectedTreeId) void fetchCards(selectedTreeId, t || undefined);
  };

  const handleDelete = async () => {
    if (!deleteTarget) return;
    try {
      await apiDelete(`/cards/${deleteTarget.id}`);
      setCards((prev) => prev.filter((c) => c.id !== deleteTarget.id));
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to archive card');
    } finally {
      setDeleteTarget(null);
    }
  };

  const handleCreated = (card: CardSummary) => {
    setCards((prev) => [card, ...prev]);
    setShowCreate(false);
  };

  const filteredCards = searchQuery
    ? cards.filter(
        (c) =>
          c.app_id.toLowerCase().includes(searchQuery.toLowerCase()) ||
          c.type.toLowerCase().includes(searchQuery.toLowerCase()) ||
          c.node_id.includes(searchQuery) ||
          JSON.stringify(c.data).toLowerCase().includes(searchQuery.toLowerCase()),
      )
    : cards;

  return (
    <div className="p-6">
      {/* Header */}
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-bold text-gray-900">Cards</h1>
          <p className="text-sm text-gray-500 mt-1">
            Graph nodes with structured data and interactive behavior
          </p>
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={() => selectedTreeId && fetchCards(selectedTreeId, typeFilter || undefined)}
            disabled={!selectedTreeId || cardsLoading}
            className="flex items-center gap-2 px-3 py-2 rounded-lg text-xs font-medium bg-gray-800 hover:bg-gray-700 text-gray-300 transition-colors disabled:opacity-50"
          >
            <RefreshCw className={`w-3.5 h-3.5 ${cardsLoading ? 'animate-spin' : ''}`} />
            Refresh
          </button>
          <button
            onClick={() => setShowCreate(true)}
            disabled={!selectedTreeId}
            className="flex items-center gap-2 px-3 py-2 rounded-lg text-xs font-semibold text-white bg-purple-600 hover:bg-purple-700 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            <Plus className="w-3.5 h-3.5" />
            New Card
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
        <label htmlFor="cards-tree-select" className="block text-xs text-gray-500 mb-2">Select Tree</label>
        <div className="relative max-w-md">
          <select
            id="cards-tree-select"
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
            Cards are attached to nodes within conversation trees. Select a tree above to browse its cards.
          </p>
        </div>
      )}

      {/* Filters & Search */}
      {selectedTreeId && (
        <div className="flex flex-wrap items-center gap-3 mb-4">
          <div className="relative flex-1 min-w-[200px] max-w-md">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-gray-500" />
            <input
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="w-full bg-gray-800 border border-gray-700 rounded-lg pl-9 pr-3 py-2 text-sm text-gray-200 placeholder-gray-600 focus:outline-none focus:ring-2 focus:ring-purple-500/50 focus:border-purple-500"
              placeholder="Search cards..."
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

          {/* Type filter tabs */}
          <div className="flex items-center gap-1 p-1 rounded-lg bg-gray-800/50">
            <button
              onClick={() => handleTypeFilter('')}
              className={`px-2.5 py-1 rounded-md text-xs font-medium capitalize transition-colors ${
                typeFilter === ''
                  ? 'bg-purple-600 text-white'
                  : 'text-gray-400 hover:text-gray-200'
              }`}
            >
              All
            </button>
            {CARD_TYPES.map((ct) => (
              <button
                key={ct}
                onClick={() => handleTypeFilter(ct)}
                className={`px-2.5 py-1 rounded-md text-xs font-medium capitalize transition-colors ${
                  typeFilter === ct
                    ? 'bg-purple-600 text-white'
                    : 'text-gray-400 hover:text-gray-200'
                }`}
              >
                {ct}
              </button>
            ))}
          </div>

          <span className="text-xs text-gray-600">{filteredCards.length} cards</span>
        </div>
      )}

      {/* Loading */}
      {cardsLoading && (
        <div className="space-y-3">
          {[1, 2, 3].map((i) => (
            <div key={i} className="rounded-lg border border-gray-800 p-4 animate-pulse">
              <div className="h-4 bg-gray-800 rounded w-32 mb-2" />
              <div className="h-3 bg-gray-800 rounded w-48 mb-2" />
              <div className="h-3 bg-gray-800 rounded w-64" />
            </div>
          ))}
        </div>
      )}

      {/* Empty */}
      {selectedTreeId && !cardsLoading && cards.length === 0 && (
        <div className="rounded-lg border border-gray-800 bg-gray-900 p-12 text-center">
          <Inbox className="w-10 h-10 text-gray-700 mx-auto mb-3" />
          <h3 className="text-sm font-medium text-gray-400 mb-1">No cards found</h3>
          <p className="text-xs text-gray-600 mb-4">
            {typeFilter
              ? `No ${typeFilter} cards in this tree.`
              : 'Create a card by attaching structured data to a node.'}
          </p>
          <button
            onClick={() => setShowCreate(true)}
            className="inline-flex items-center gap-2 px-4 py-2 rounded-lg text-xs font-semibold text-white bg-purple-600 hover:bg-purple-700 transition-colors"
          >
            <Plus className="w-3.5 h-3.5" />
            Create Card
          </button>
        </div>
      )}

      {/* Card rows */}
      {selectedTreeId && !cardsLoading && filteredCards.length > 0 && (
        <div className="space-y-3">
          {filteredCards.map((card) => (
            <CardRow
              key={card.id}
              card={card}
              onDelete={() => setDeleteTarget(card)}
            />
          ))}
        </div>
      )}

      {/* Create dialog */}
      {showCreate && selectedTreeId && (
        <CreateCardDialog
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
              <h3 className="text-sm font-medium text-gray-200">Archive Card</h3>
            </div>
            <div className="px-5 py-4">
              <p className="text-sm text-gray-400">
                Archive this {cardTypeLabel(deleteTarget.type)} card?
                This will soft-delete the card.
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
