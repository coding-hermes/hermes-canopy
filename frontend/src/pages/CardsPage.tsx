/**
 * Hermes Canopy — Cards Page
 *
 * Full CRUD for cards. Cards are graph nodes with structured data
 * and interactive behavior. Three types: compact, expanded, iteration.
 */

import { useState, useEffect, useCallback } from 'react';
import {
  Plus,
  RefreshCw,
  AlertCircle,
  Inbox,
  ChevronDown,
  ChevronRight,
  Search,
  X,
} from 'lucide-react';
import { apiGet, apiPost, apiUrl, authInit } from '../lib/api';
import { invokeCardAction } from '../lib/cardActions';
import { resolveCardRenderer } from '../lib/cardRenderers';
import {
  cardFromSummaryWire,
  cardTypeLabel,
  type Card,
  type CardStatus,
  type CardSummaryWire,
} from '../types/card';
import { Link } from 'react-router-dom';
import CardActivityPanel from '../components/CardActivityPanel';

// ─── Types ─────────────────────────────────────────────────────────────

type CardSummary = CardSummaryWire & {
  /** Card PATCH/DELETE optimistic-concurrency token from the service summary. */
  revision: number;
};

interface ListCardsResponse {
  cards: CardSummary[];
}

interface NodeSummary {
  id: string;
  content: string;
}

interface ListNodesResponse {
  nodes: NodeSummary[];
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


const CARD_TYPES = ['compact', 'expanded', 'iteration'] as const;

function nodeOptionLabel(node: NodeSummary): string {
  const excerpt = node.content.replace(/\s+/g, ' ').trim();
  const shortened = excerpt.length > 72 ? `${excerpt.slice(0, 72)}…` : excerpt;
  return `${shortened || 'Untitled node'} · ${node.id.slice(0, 8)}`;
}

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
  const [nodes, setNodes] = useState<NodeSummary[]>([]);
  const [nodesLoading, setNodesLoading] = useState(true);
  const [nodesError, setNodesError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setNodes([]);
    setNodeId('');
    setNodesLoading(true);
    setNodesError(null);
    void apiGet<ListNodesResponse>(`/trees/${treeId}/nodes`)
      .then((data) => {
        if (!cancelled) setNodes(data.nodes);
      })
      .catch((err) => {
        if (!cancelled) {
          setNodesError(err instanceof Error ? err.message : 'Failed to load nodes');
        }
      })
      .finally(() => {
        if (!cancelled) setNodesLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [treeId]);

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
      <div className="relative glass-raised rounded-xl w-full max-w-md mx-4">
        <div className="px-5 py-4 border-b border-line-subtle">
          <h2 className="text-sm font-medium text-content-primary">Create Card</h2>
        </div>
        <div className="px-5 py-4 space-y-3">
          {error && (
            <div className="flex items-center gap-2 p-2 rounded bg-rose-500/10 border border-rose-500/30 text-status-danger text-xs">
              <AlertCircle className="w-3.5 h-3.5 flex-shrink-0" />
              {error}
            </div>
          )}
          <div>
            <label className="block text-xs text-content-muted mb-1">Tree</label>
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
            <label className="block text-xs text-content-muted mb-1">Card Type</label>
            <div className="flex gap-1">
              {CARD_TYPES.map((ct) => (
                <button
                  key={ct}
                  onClick={() => setCardType(ct)}
                  className={`px-3 py-1.5 rounded-lg text-xs font-medium capitalize transition-colors ${
                    cardType === ct
                      ? 'bg-purple-600 text-white'
                      : 'bg-surface-input text-content-muted hover:text-content-primary'
                  }`}
                >
                  {ct}
                </button>
              ))}
            </div>
          </div>
          <div>
            <label htmlFor="create-card-node-select" className="block text-xs text-content-muted mb-1">
              Node *
            </label>
            <select
              id="create-card-node-select"
              data-testid="create-card-node-select"
              value={nodeId}
              onChange={(e) => setNodeId(e.target.value)}
              disabled={nodesLoading || nodes.length === 0}
              className="w-full bg-surface-input border border-line-subtle rounded-lg px-3 py-2 text-sm text-content-primary focus:outline-none focus:ring-2 focus:ring-accent/60 focus:border-accent disabled:opacity-60"
            >
              <option value="">
                {nodesLoading ? 'Loading nodes...' : nodes.length === 0 ? 'No nodes in this tree' : 'Choose a node...'}
              </option>
              {nodes.map((node) => (
                <option key={node.id} value={node.id}>
                  {nodeOptionLabel(node)}
                </option>
              ))}
            </select>
            {nodesError && <p className="mt-1 text-xs text-status-danger">{nodesError}</p>}
            {!nodesLoading && !nodesError && nodes.length === 0 && (
              <p className="mt-1 text-xs text-content-muted">This tree has no nodes to attach a card to.</p>
            )}
          </div>
          <div>
            <label className="block text-xs text-content-muted mb-1">App ID</label>
            <input
              value={appId}
              onChange={(e) => setAppId(e.target.value)}
              className="w-full bg-surface-input border border-line-subtle rounded-lg px-3 py-2 text-sm text-content-primary placeholder-content-faint focus:outline-none focus:ring-2 focus:ring-accent/60 focus:border-accent"
              placeholder="canopy"
            />
          </div>
          <div>
            <label className="block text-xs text-content-muted mb-1">Data (JSON) *</label>
            <textarea
              value={dataJson}
              onChange={(e) => setDataJson(e.target.value)}
              rows={4}
              className="w-full bg-surface-input border border-line-subtle rounded-lg px-3 py-2 text-sm text-content-primary placeholder-content-faint font-mono resize-none focus:outline-none focus:ring-2 focus:ring-accent/60 focus:border-accent"
              placeholder='{"key": "value"}'
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
            disabled={loading || !nodeId.trim()}
            className="px-4 py-1.5 text-xs font-semibold text-white bg-accent-2-600 hover:bg-accent-2-500 rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
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
  isSelected,
  statusUpdating,
  onOpen,
  onStatusChange,
}: {
  card: CardSummary;
  isSelected: boolean;
  statusUpdating: boolean;
  onOpen: () => void;
  onStatusChange: (status: CardStatus) => void;
}) {
  const parsed = cardFromSummaryWire(card);
  if (!parsed.ok) {
    return (
      <div role="alert" className="rounded-lg border border-rose-500/30 bg-rose-500/10 p-4 text-xs text-status-danger">
        Card payload rejected: {parsed.issues.join('; ')}
      </div>
    );
  }

  const canonical: Card = parsed.value;
  const Renderer = resolveCardRenderer(canonical.appId, canonical.cardType);

  return (
    <div
      // §9: clicking a row opens that card's live activity panel (one
      // subscription at a time — the panel subscribes, not the row).
      onClick={onOpen}
      className={`rounded-lg border bg-surface-panel p-4 group transition-colors cursor-pointer ${
        isSelected
          ? 'border-accent-2/60 ring-1 ring-inset ring-accent-2/30'
          : 'border-line-subtle hover:border-accent-2/40'
      }`}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="flex-1 min-w-0">
          <Renderer
            card={canonical}
            events={[]}
            isLive={false}
            invokeAction={(handler, payload) => invokeCardAction(canonical.id, handler, payload)}
          />
        </div>
        <div className="flex items-center gap-1 flex-shrink-0">
          <button
            onClick={(event) => {
              event.stopPropagation();
              onOpen();
            }}
            className="p-1.5 rounded-md text-content-faint hover:text-content-primary hover:bg-surface-hover transition-colors"
            title="Card activity"
            aria-label={`Card activity for ${cardTypeLabel(canonical.cardType)} card`}
            aria-expanded={isSelected}
          >
            <ChevronRight className="w-4 h-4" />
          </button>
          {canonical.status === 'active' && (
            <>
              <button
                type="button"
                onClick={(event) => {
                  event.stopPropagation();
                  onStatusChange('dismissed');
                }}
                disabled={statusUpdating}
                className="px-2 py-1 rounded-md text-[11px] font-medium text-content-muted hover:text-content-primary hover:bg-surface-hover transition-colors disabled:opacity-50"
                aria-label="Dismiss card"
              >
                Dismiss
              </button>
              <button
                type="button"
                onClick={(event) => {
                  event.stopPropagation();
                  onStatusChange('archived');
                }}
                disabled={statusUpdating}
                className="px-2 py-1 rounded-md text-[11px] font-medium text-status-danger hover:bg-rose-500/10 transition-colors disabled:opacity-50"
                aria-label="Archive card"
              >
                Archive
              </button>
            </>
          )}
          {canonical.status === 'dismissed' && (
            <>
              <button
                type="button"
                onClick={(event) => {
                  event.stopPropagation();
                  onStatusChange('active');
                }}
                disabled={statusUpdating}
                className="px-2 py-1 rounded-md text-[11px] font-medium text-content-muted hover:text-content-primary hover:bg-surface-hover transition-colors disabled:opacity-50"
                aria-label="Restore card"
              >
                Restore
              </button>
              <button
                type="button"
                onClick={(event) => {
                  event.stopPropagation();
                  onStatusChange('archived');
                }}
                disabled={statusUpdating}
                className="px-2 py-1 rounded-md text-[11px] font-medium text-status-danger hover:bg-rose-500/10 transition-colors disabled:opacity-50"
                aria-label="Archive card"
              >
                Archive
              </button>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

async function patchCardStatus(card: CardSummary, status: CardStatus): Promise<CardSummary> {
  const response = await fetch(
    apiUrl(`/cards/${encodeURIComponent(card.id)}`),
    authInit({
      method: 'PATCH',
      headers: {
        'Content-Type': 'application/json',
        'If-Match': String(card.revision),
      },
      body: JSON.stringify({ status }),
    }),
  );
  if (!response.ok) {
    const text = await response.text();
    let message = text || `HTTP ${response.status}`;
    try {
      const parsed = JSON.parse(text) as { error?: { message?: unknown } | unknown };
      const apiError = parsed.error;
      if (typeof apiError === 'object' && apiError !== null && 'message' in apiError) {
        const detail = (apiError as { message?: unknown }).message;
        if (typeof detail === 'string' && detail) message = detail;
      } else if (typeof apiError === 'string' && apiError) {
        message = apiError;
      }
    } catch {
      // Keep the plain response text for non-JSON failures.
    }
    throw new Error(message);
  }
  return response.json() as Promise<CardSummary>;
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
  const [statusUpdatingId, setStatusUpdatingId] = useState<string | null>(null);
  // The card whose live activity panel is open (null = closed). Exactly one
  // at a time: the panel owns the stream subscription (SPEC-PL-03 §9).
  const [selectedCardId, setSelectedCardId] = useState<string | null>(null);

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
    setSelectedCardId(null);
    if (treeId) void fetchCards(treeId, typeFilter || undefined);
  };

  const handleTypeFilter = (t: string) => {
    setTypeFilter(t);
    if (selectedTreeId) void fetchCards(selectedTreeId, t || undefined);
  };

  const handleCardStatus = async (card: CardSummary, status: CardStatus) => {
    if (statusUpdatingId) return;
    setStatusUpdatingId(card.id);
    setError(null);
    try {
      const updated = await patchCardStatus(card, status);
      setCards((prev) => prev.map((current) => (current.id === updated.id ? updated : current)));
    } catch (err) {
      setError(err instanceof Error ? err.message : `Failed to set card status to ${status}`);
    } finally {
      setStatusUpdatingId(null);
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
          <h1 className="text-2xl font-bold tracking-tight text-content-primary">Cards</h1>
          <p className="text-sm text-content-muted mt-1">
            Graph nodes with structured data and interactive behavior
          </p>
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={() => selectedTreeId && fetchCards(selectedTreeId, typeFilter || undefined)}
            disabled={!selectedTreeId || cardsLoading}
            className="flex items-center gap-2 px-3 py-2 rounded-lg text-xs font-medium bg-surface-input hover:bg-surface-hover text-content-secondary ring-1 ring-inset ring-line-subtle transition-colors disabled:opacity-50"
          >
            <RefreshCw className={`w-3.5 h-3.5 ${cardsLoading ? 'animate-spin' : ''}`} />
            Refresh
          </button>
          <button
            onClick={() => setShowCreate(true)}
            disabled={!selectedTreeId}
            className="flex items-center gap-2 px-3 py-2 rounded-lg text-xs font-semibold text-white bg-accent-2-600 hover:bg-accent-2-500 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            <Plus className="w-3.5 h-3.5" />
            New Card
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
        <label htmlFor="cards-tree-select" className="block text-xs text-content-muted mb-2">Select Tree</label>
        <div className="relative max-w-md">
          <select
            id="cards-tree-select"
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
            Cards are attached to nodes within conversation trees. Select a tree above to browse its cards.
          </p>
          <p className="text-xs text-content-muted mt-2" data-testid="cards-no-trees-hint">
            No trees yet?{' '}
            <Link to="/trees" className="underline text-accent-2-300 hover:text-accent-2-200">
              Create your first tree
            </Link>{' '}
            (or import one) on the Trees page.
          </p>
        </div>
      )}

      {/* Filters & Search */}
      {selectedTreeId && (
        <div className="flex flex-wrap items-center gap-3 mb-4">
          <div className="relative flex-1 min-w-[200px] max-w-md">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-content-muted" />
            <input
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="w-full bg-surface-input border border-line-subtle rounded-lg pl-9 pr-3 py-2 text-sm text-content-primary placeholder-content-faint focus:outline-none focus:ring-2 focus:ring-accent/60 focus:border-accent"
              placeholder="Search cards..."
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

          {/* Type filter tabs */}
          <div className="flex items-center gap-1 p-1 rounded-lg bg-surface-input/70 ring-1 ring-inset ring-line-subtle">
            <button
              onClick={() => handleTypeFilter('')}
              className={`px-2.5 py-1 rounded-md text-xs font-medium capitalize transition-colors ${
                typeFilter === ''
                  ? 'bg-purple-600 text-white'
                  : 'text-content-muted hover:text-content-primary'
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
                    : 'text-content-muted hover:text-content-primary'
                }`}
              >
                {ct}
              </button>
            ))}
          </div>

          <span className="text-xs text-content-muted">{filteredCards.length} cards</span>
        </div>
      )}

      {/* Loading */}
      {cardsLoading && (
        <div className="space-y-3">
          {[1, 2, 3].map((i) => (
            <div key={i} className="rounded-lg border border-line-subtle p-4 animate-pulse">
              <div className="h-4 bg-surface-input rounded w-32 mb-2" />
              <div className="h-3 bg-surface-input rounded w-48 mb-2" />
              <div className="h-3 bg-surface-input rounded w-64" />
            </div>
          ))}
        </div>
      )}

      {/* Empty */}
      {selectedTreeId && !cardsLoading && cards.length === 0 && (
        <div className="rounded-xl border border-line-subtle bg-surface-panel p-12 text-center">
          <Inbox className="w-10 h-10 text-content-faint/50 mx-auto mb-3" />
          <h2 className="text-sm font-medium text-content-secondary mb-1">No cards found</h2>
          <p className="text-xs text-content-muted mb-4">
            {typeFilter
              ? `No ${typeFilter} cards in this tree.`
              : 'Create a card by attaching structured data to a node.'}
          </p>
          <button
            onClick={() => setShowCreate(true)}
            className="inline-flex items-center gap-2 px-4 py-2 rounded-lg text-xs font-semibold text-white bg-accent-2-600 hover:bg-accent-2-500 transition-colors"
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
              isSelected={card.id === selectedCardId}
              statusUpdating={statusUpdatingId === card.id}
              onOpen={() => setSelectedCardId(card.id)}
              onStatusChange={(status) => void handleCardStatus(card, status)}
            />
          ))}
        </div>
      )}

      {/* Live card activity panel (SPEC-PL-03 §6.1 + §9) — one subscription */}
      {selectedCardId && (
        <CardActivityPanel
          cardId={selectedCardId}
          onClose={() => setSelectedCardId(null)}
        />
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
    </div>
  );
}
