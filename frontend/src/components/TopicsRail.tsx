/**
 * Hermes Canopy — Topics sidebar section (UI-02 + sidebar consolidation)
 *
 * Lives INSIDE the main sidebar (`App.tsx` `Layout()`), below the primary
 * navigation buttons, separated by a horizontal rule — ChatGPT-style
 * single-rail layout. Survives route changes (mockup-1.png).
 *
 * Layout, top to bottom:
 *   • header    — "Topics" + count + sort select + `+` create button
 *   • search    — filter box over the topic list (client-side)
 *   • list      — scrollable topic pills: semantic icon, title, count badge
 *   • footer    — pinned settings + refresh controls
 *
 * The backend list endpoint is tree-scoped (`GET /topics?tree_id=…` returns
 * 400 MISSING_TREE_ID otherwise), so the section resolves a tree first via
 * `GET /trees` and remembers the choice in `localStorage` — the same tree
 * the user picked on the Topics page.
 *
 * Clicking a pill deep-links to `/topics?tree=<treeId>&topic=<topicId>`;
 * `TopicsPage` reads those params to preselect the tree and highlight the
 * topic. No new backend endpoints are introduced.
 */

import { useState, useEffect, useCallback, useRef } from 'react';
import { useNavigate, useSearchParams, useLocation } from 'react-router-dom';
import {
  Plus,
  RefreshCw,
  Settings,
  Inbox,
  AlertCircle,
  Search,
  ArrowDownWideNarrow,
} from 'lucide-react';
import { apiGet } from '../lib/api';
import type { TopicSummary, TreeSummary } from '../types/topic';
import { topicIcon, orderTopics } from '../lib/topicIcons';
import {
  ACTIVE_TREE_STORAGE_KEY,
  readStoredTreeId,
  storeTreeId,
} from '../lib/activeTree';
import { DetectionSettings } from './DetectionSettings';

// ─── Types ─────────────────────────────────────────────────────────────

interface ListTopicsResponse {
  topics: TopicSummary[];
}

interface ListTreesResponse {
  trees: TreeSummary[];
}

type SortMode = 'count' | 'name' | 'newest';

// ─── Topic pill ────────────────────────────────────────────────────────

function TopicPill({
  topic,
  active,
  onSelect,
}: {
  topic: TopicSummary;
  active: boolean;
  onSelect: () => void;
}) {
  const Icon = topicIcon(topic.title);
  const count = topic.node_count ?? 0;

  return (
    <button
      type="button"
      onClick={onSelect}
      aria-current={active ? 'true' : undefined}
      title={`${topic.title} — ${count} nodes`}
      className={[
        'group w-full flex items-center gap-2.5 rounded-lg px-2.5 py-2 transition-colors text-left',
        active
          ? 'bg-accent-2/12 ring-1 ring-inset ring-accent-2/35'
          : 'ring-1 ring-inset ring-transparent hover:bg-surface-hover/50',
      ].join(' ')}
    >
      <span
        aria-hidden="true"
        className={[
          'grid h-7 w-7 shrink-0 place-items-center rounded-md ring-1 ring-inset transition-colors',
          active
            ? 'bg-accent-2/20 text-accent-2-300 ring-accent-2/40'
            : 'bg-surface-input text-content-tertiary ring-line-subtle group-hover:text-content-secondary',
        ].join(' ')}
      >
        <Icon className="h-3.5 w-3.5" />
      </span>

      <span
        className={[
          'flex-1 min-w-0 truncate text-sm',
          active
            ? 'font-medium text-content-primary'
            : 'text-content-tertiary group-hover:text-content-primary',
        ].join(' ')}
      >
        {topic.title}
      </span>
      <span
        aria-label={`${count} nodes`}
        className={[
          'shrink-0 rounded-sm px-1.5 py-0.5 text-[11px] font-medium tabular-nums ring-1 ring-inset',
          active
            ? 'bg-accent-2/15 text-accent-2-300 ring-accent-2/30'
            : 'bg-surface-input text-content-muted ring-line-subtle',
        ].join(' ')}
      >
        {count}
      </span>
    </button>
  );
}

// ─── Sidebar section ───────────────────────────────────────────────────

export default function TopicsRail() {
  const navigate = useNavigate();
  const location = useLocation();
  const [searchParams] = useSearchParams();

  // Depend on the PARAM VALUES, never the `searchParams` object — its
  // identity changes on every render, which would re-create `load` and
  // re-fetch in a loop.
  const treeParam = searchParams.get('tree') ?? '';
  const topicParam = searchParams.get('topic') ?? '';

  const [treeId, setTreeId] = useState<string>(() => readStoredTreeId());
  const [topics, setTopics] = useState<TopicSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState('');
  const [sortMode, setSortMode] = useState<SortMode>('count');

  // Latest requested tree, so an out-of-order response can't overwrite a
  // newer one (StrictMode double-invokes effects in dev).
  const requestedTree = useRef<string>('');

  const activeTopicId = location.pathname === '/topics' ? topicParam : '';

  /**
   * Resolve a tree to scope the list to: URL param → stored → first tree.
   *
   * `explicit` distinguishes a tree the USER chose (deep link or an
   * earlier selection on the Topics page) from one the rail merely picked
   * so it has something to show. Only an explicit choice is persisted —
   * otherwise the rail's fallback would silently answer the Topics page's
   * "Select a tree" prompt on the user's behalf.
   */
  const resolveTree = useCallback(async (): Promise<{
    id: string;
    explicit: boolean;
  }> => {
    if (treeParam) return { id: treeParam, explicit: true };

    const stored = readStoredTreeId();
    if (stored) return { id: stored, explicit: true };

    // Fallback: the API returns trees newest-first, so a naive trees[0]
    // picks whatever tree was created LAST — E2E test trees (T265/T267/
    // T268) top the list and carry no topics, rendering the rail empty.
    // Prefer the seeded demo tree by label (VREG-001 parity with the
    // nodes/topics page selectors); fall back to trees[0] when absent.
    const data = await apiGet<ListTreesResponse>('/trees?limit=50');
    const demo = data.trees.find((t) => t.title.startsWith('UI-02 Rail Demo'));
    if (demo) return { id: demo.id, explicit: false };

    // Pagination (PAG-001/002): with 3,600+ trees the demo tree predates
    // the first page — find it through the API title search instead.
    try {
      const found = await apiGet<ListTreesResponse>(
        `/trees?search=${encodeURIComponent('UI-02 Rail Demo')}&limit=1`,
      );
      const hit = found.trees.find((t) => t.title.startsWith('UI-02 Rail Demo'));
      if (hit) return { id: hit.id, explicit: false };
    } catch {
      // Fall through to trees[0] — search is best-effort here.
    }
    return { id: data.trees[0]?.id ?? '', explicit: false };
  }, [treeParam]);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const { id, explicit } = await resolveTree();
      requestedTree.current = id;
      if (!id) {
        setTreeId('');
        setTopics([]);
        return;
      }
      setTreeId(id);
      if (explicit) storeTreeId(id);

      const data = await apiGet<ListTopicsResponse>(
        `/topics?tree_id=${encodeURIComponent(id)}&limit=100`,
      );
      if (requestedTree.current !== id) return; // superseded
      setTopics(orderTopics(data.topics ?? []));
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load topics');
      setTopics([]);
    } finally {
      setLoading(false);
    }
  }, [resolveTree]);

  useEffect(() => {
    void load();
  }, [load]);

  // Re-sync when another surface (TopicsPage tree selector, create dialog)
  // changes the active tree or the topic set.
  useEffect(() => {
    const onChanged = () => void load();
    window.addEventListener(ACTIVE_TREE_STORAGE_KEY, onChanged);
    return () => window.removeEventListener(ACTIVE_TREE_STORAGE_KEY, onChanged);
  }, [load]);

  const openTopic = (topic: TopicSummary) => {
    const params = new URLSearchParams({
      tree: topic.tree_id || treeId,
      topic: topic.id,
    });
    navigate(`/topics?${params.toString()}`);
  };

  const openCreate = () => {
    const params = new URLSearchParams({ new: '1' });
    if (treeId) params.set('tree', treeId);
    navigate(`/topics?${params.toString()}`);
  };

  // Client-side filter + sort (search + sort controls, ChatGPT-style rail)
  const q = query.trim().toLowerCase();
  const visible = topics
    .filter((t) => !q || t.title.toLowerCase().includes(q))
    .sort((a, b) => {
      switch (sortMode) {
        case 'name':
          return a.title.localeCompare(b.title);
        case 'newest':
          return b.created_at.localeCompare(a.created_at);
        case 'count':
        default:
          return (
            (b.node_count ?? 0) - (a.node_count ?? 0) ||
            a.title.localeCompare(b.title)
          );
      }
    });

  return (
    <section
      aria-label="Topics"
      data-testid="topics-rail"
      className="flex min-h-0 flex-1 flex-col border-t border-line-subtle"
    >
      {/* Header — title + count, sort, new */}
      <div className="flex shrink-0 items-center gap-1.5 px-4 pt-3 pb-2">
        <h2 className="flex-1 min-w-0 text-sm font-semibold tracking-tight text-content-primary">
          Topics
        </h2>
        <span
          aria-hidden="true"
          className="shrink-0 rounded-sm bg-surface-input px-1.5 py-0.5 text-[11px] tabular-nums text-content-muted ring-1 ring-inset ring-line-subtle"
        >
          {topics.length}
        </span>
        <label className="sr-only" htmlFor="topics-sort">
          Sort topics
        </label>
        <div className="relative shrink-0">
          <ArrowDownWideNarrow
            className="pointer-events-none absolute left-1.5 top-1/2 h-3 w-3 -translate-y-1/2 text-content-muted"
            aria-hidden="true"
          />
          <select
            id="topics-sort"
            value={sortMode}
            onChange={(e) => setSortMode(e.target.value as SortMode)}
            aria-label="Sort topics"
            className="h-7 appearance-none rounded-md bg-surface-input pl-6 pr-5 text-[11px] font-medium text-content-secondary ring-1 ring-inset ring-line-subtle transition-colors hover:bg-surface-hover focus:outline-none focus-visible:ring-2 focus-visible:ring-accent"
          >
            <option value="count">Count</option>
            <option value="name">A–Z</option>
            <option value="newest">Newest</option>
          </select>
        </div>
        <button
          type="button"
          onClick={openCreate}
          aria-label="New topic"
          title="New topic"
          className="grid h-7 w-7 shrink-0 place-items-center rounded-md bg-accent-2/15 text-accent-2-300 ring-1 ring-inset ring-accent-2/30 transition-colors hover:bg-accent-2/25"
        >
          <Plus className="h-3.5 w-3.5" aria-hidden="true" />
        </button>
      </div>

      {/* Search */}
      <div className="shrink-0 px-3 pb-2">
        <label className="sr-only" htmlFor="topics-search">
          Search topics
        </label>
        <div className="relative">
          <Search
            className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-content-muted"
            aria-hidden="true"
          />
          <input
            id="topics-search"
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search topics…"
            className="w-full rounded-md bg-surface-input py-1.5 pl-8 pr-3 text-[13px] text-content-primary placeholder:text-content-faint ring-1 ring-inset ring-line-subtle transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-accent"
          />
        </div>
      </div>

      {/* Topic list — scrollable */}
      <div className="min-h-0 flex-1 space-y-1 overflow-y-auto px-3 py-1">
        {loading && (
          <div className="space-y-1" aria-hidden="true">
            {[0, 1, 2, 3, 4].map((i) => (
              <div
                key={i}
                className="h-11 animate-pulse rounded-lg bg-surface-input/70"
              />
            ))}
          </div>
        )}

        {!loading && error && (
          <div
            role="alert"
            className="flex items-start gap-2 rounded-lg border border-rose-500/30 bg-rose-500/10 p-2.5 text-[11px] text-status-danger"
          >
            <AlertCircle
              className="mt-px h-3.5 w-3.5 shrink-0"
              aria-hidden="true"
            />
            <span className="min-w-0 break-words">{error}</span>
          </div>
        )}

        {!loading && !error && topics.length === 0 && (
          <div className="rounded-lg border border-line-subtle bg-surface-panel px-3 py-6 text-center">
            <Inbox
              className="mx-auto mb-2 h-6 w-6 text-content-faint"
              aria-hidden="true"
            />
            <p className="text-xs font-medium text-content-secondary">
              No topics yet
            </p>
            <p className="mt-1 text-[11px] text-content-muted">
              Create one to group a subgraph.
            </p>
          </div>
        )}

        {!loading &&
          !error &&
          topics.length > 0 &&
          visible.length === 0 && (
            <div className="rounded-lg border border-line-subtle bg-surface-panel px-3 py-6 text-center">
              <Search
                className="mx-auto mb-2 h-6 w-6 text-content-faint"
                aria-hidden="true"
              />
              <p className="text-xs font-medium text-content-secondary">
                No topics match “{query}”
              </p>
            </div>
          )}

        {!loading &&
          visible.map((topic) => (
            <TopicPill
              key={topic.id}
              topic={topic}
              active={topic.id === activeTopicId}
              onSelect={() => openTopic(topic)}
            />
          ))}

        {/* Ghost "New topic" button — mockup places it below the list */}
        {!loading && (
          <button
            type="button"
            onClick={openCreate}
            className="mt-1 flex w-full items-center gap-2.5 rounded-lg border border-dashed border-accent-2/35 px-2.5 py-2 text-sm text-accent-2-300 transition-colors hover:border-accent-2/60 hover:bg-accent-2/10"
          >
            <span
              aria-hidden="true"
              className="grid h-7 w-7 shrink-0 place-items-center"
            >
              <Plus className="h-3.5 w-3.5" />
            </span>
            New topic
          </button>
        )}
      </div>

      {/* Pinned footer — settings + refresh */}
      <div className="flex shrink-0 items-center gap-1 border-t border-line-subtle px-3 py-2">
        <DetectionSettings treeId={treeId} />
        <button
          type="button"
          onClick={() =>
            navigate(
              treeId
                ? `/topics?tree=${encodeURIComponent(treeId)}`
                : '/topics',
            )
          }
          aria-label="Manage topics"
          title="Manage topics"
          className="grid h-8 w-8 place-items-center rounded-md text-content-muted transition-colors hover:bg-surface-hover hover:text-content-primary"
        >
          <Settings className="h-4 w-4" aria-hidden="true" />
        </button>
        <button
          type="button"
          onClick={() => void load()}
          disabled={loading}
          aria-label="Refresh topics"
          title="Refresh topics"
          className="grid h-8 w-8 place-items-center rounded-md text-content-muted transition-colors hover:bg-surface-hover hover:text-content-primary disabled:opacity-50"
        >
          <RefreshCw
            className={`h-4 w-4 ${loading ? 'animate-spin' : ''}`}
            aria-hidden="true"
          />
        </button>
        <span className="ml-auto text-[11px] tabular-nums text-content-muted">
          {visible.length}/{topics.length} topics
        </span>
      </div>
    </section>
  );
}
