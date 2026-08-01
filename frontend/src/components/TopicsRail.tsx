/**
 * Hermes Canopy — Topics Rail (UI-02, Phase 11 Mockup Parity)
 *
 * A persistent left rail listing the topics of the active tree, mounted
 * in `App.tsx` `Layout()` so it survives route changes (mockup-1.png).
 *
 * Column order matches the mockup:
 *
 *     [ main nav sidebar ] [ TOPICS RAIL ] [ header + <Outlet/> ]
 *
 * Contents, top to bottom:
 *   • header       — "Topics" + inline `+` create button
 *   • scroll list  — topic pills: semantic icon, title, node-count badge
 *   • ghost button — dashed "New topic"
 *   • footer       — pinned settings + refresh controls
 *
 * The backend list endpoint is tree-scoped (`GET /topics?tree_id=…` returns
 * 400 MISSING_TREE_ID otherwise), so the rail resolves a tree first via
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
  PanelLeftClose,
  PanelLeftOpen,
} from 'lucide-react';
import { apiGet } from '../lib/api';
import type { TopicSummary, TreeSummary } from '../types/topic';
import { topicIcon, orderTopics } from '../lib/topicIcons';
import {
  ACTIVE_TREE_STORAGE_KEY,
  readStoredTreeId,
  storeTreeId,
} from '../lib/activeTree';

// ─── Types ─────────────────────────────────────────────────────────────

interface ListTopicsResponse {
  topics: TopicSummary[];
}

interface ListTreesResponse {
  trees: TreeSummary[];
}

// ─── Topic pill ────────────────────────────────────────────────────────

function TopicPill({
  topic,
  active,
  collapsed,
  onSelect,
}: {
  topic: TopicSummary;
  active: boolean;
  collapsed: boolean;
  onSelect: () => void;
}) {
  const Icon = topicIcon(topic.title);
  const count = topic.node_count ?? 0;

  return (
    <button
      type="button"
      onClick={onSelect}
      aria-current={active ? 'true' : undefined}
      title={collapsed ? `${topic.title} — ${count} nodes` : undefined}
      className={[
        'group w-full flex items-center rounded-lg transition-colors text-left',
        collapsed ? 'justify-center px-0 py-2.5' : 'gap-2.5 px-2.5 py-2',
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

      {!collapsed && (
        <>
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
        </>
      )}
    </button>
  );
}

// ─── Rail ──────────────────────────────────────────────────────────────

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
  const [collapsed, setCollapsed] = useState(false);

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

    const data = await apiGet<ListTreesResponse>('/trees?limit=1');
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

  const railWidth = collapsed ? 'w-16' : 'w-60';

  return (
    <nav
      aria-label="Topics"
      data-testid="topics-rail"
      className={`${railWidth} hidden md:flex shrink-0 flex-col border-r border-line-subtle bg-surface-panel/60 transition-[width] duration-200`}
    >
      {/* Header */}
      <div
        className={`flex h-14 shrink-0 items-center border-b border-line-subtle ${
          collapsed ? 'justify-center px-2' : 'gap-2 px-4'
        }`}
      >
        {!collapsed && (
          <h2 className="flex-1 text-sm font-semibold tracking-tight text-content-primary">
            Topics
          </h2>
        )}
        <button
          type="button"
          onClick={() => setCollapsed((c) => !c)}
          aria-label={collapsed ? 'Expand topics rail' : 'Collapse topics rail'}
          aria-expanded={!collapsed}
          className="grid h-7 w-7 shrink-0 place-items-center rounded-md text-content-muted ring-1 ring-inset ring-line-subtle transition-colors hover:bg-surface-hover hover:text-content-primary"
        >
          {collapsed ? (
            <PanelLeftOpen className="h-3.5 w-3.5" aria-hidden="true" />
          ) : (
            <PanelLeftClose className="h-3.5 w-3.5" aria-hidden="true" />
          )}
        </button>
        {!collapsed && (
          <button
            type="button"
            onClick={openCreate}
            aria-label="New topic"
            className="grid h-7 w-7 shrink-0 place-items-center rounded-md bg-accent-2/15 text-accent-2-300 ring-1 ring-inset ring-accent-2/30 transition-colors hover:bg-accent-2/25"
          >
            <Plus className="h-3.5 w-3.5" aria-hidden="true" />
          </button>
        )}
      </div>

      {/* Topic list */}
      <div className={`flex-1 overflow-y-auto ${collapsed ? 'p-2' : 'p-3'} space-y-1`}>
        {loading && (
          <div className="space-y-1" aria-hidden="true">
            {[0, 1, 2, 3, 4].map((i) => (
              <div
                key={i}
                className={`animate-pulse rounded-lg bg-surface-input/70 ${
                  collapsed ? 'h-11' : 'h-11'
                }`}
              />
            ))}
          </div>
        )}

        {!loading && error && !collapsed && (
          <div
            role="alert"
            className="flex items-start gap-2 rounded-lg border border-rose-500/30 bg-rose-500/10 p-2.5 text-[11px] text-status-danger"
          >
            <AlertCircle className="mt-px h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            <span className="min-w-0 break-words">{error}</span>
          </div>
        )}

        {!loading && !error && topics.length === 0 && !collapsed && (
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
          topics.map((topic) => (
            <TopicPill
              key={topic.id}
              topic={topic}
              active={topic.id === activeTopicId}
              collapsed={collapsed}
              onSelect={() => openTopic(topic)}
            />
          ))}

        {/* Ghost "New topic" button — mockup places it below the list */}
        {!collapsed && !loading && (
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
      <div
        className={`flex shrink-0 items-center border-t border-line-subtle ${
          collapsed ? 'flex-col gap-1 p-2' : 'gap-1 px-3 py-2.5'
        }`}
      >
        <button
          type="button"
          onClick={() =>
            navigate(treeId ? `/topics?tree=${encodeURIComponent(treeId)}` : '/topics')
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
        {!collapsed && (
          <span className="ml-auto text-[11px] tabular-nums text-content-muted">
            {topics.length} {topics.length === 1 ? 'topic' : 'topics'}
          </span>
        )}
      </div>
    </nav>
  );
}
