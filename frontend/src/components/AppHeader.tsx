/**
 * Hermes Canopy — App header (UI-03, Phase 11 Mockup Parity)
 *
 * The context-aware top bar of the main content column, mounted once in
 * `App.tsx` `Layout()` so it persists across routes (mockup-1.png).
 *
 * Three zones, left to right:
 *
 *   ┌────────────────────────────────────────────────────────────────┐
 *   │ Strategy (12)        [ ⌗ Tree | ☰ Detail | ⑂ Merge ]   ● API   │
 *   │ Macro tree view                                                │
 *   └────────────────────────────────────────────────────────────────┘
 *
 *   • LEFT   — context title (active topic → active tree → fallback),
 *              a node-count badge, and the view-mode subtitle.
 *   • CENTER — segmented Tree/Detail/Merge selector. Real links, not
 *              decoration: each segment routes, and the highlight is
 *              derived from the current path so browser back/forward
 *              and the sidebar nav keep it honest.
 *   • RIGHT  — the backend status pill (dev indicator, preserved).
 *
 * All resolution logic is pure and lives in `lib/headerContext.ts`; this
 * file only fetches and paints. The lists it needs are the same two the
 * topics rail already loads, so the header re-fetches on the shared
 * `canopy.activeTreeId` event rather than owning its own cache.
 *
 * The heading is an `<h2>`: each page owns the single `<h1>` on screen
 * (BUG-006 regression guard).
 */

import { useState, useEffect, useCallback, useRef } from 'react';
import { NavLink, useLocation, useSearchParams } from 'react-router-dom';
import { Network, List, GitMerge, type LucideIcon } from 'lucide-react';
import { apiGet } from '../lib/api';
import type { TopicSummary } from '../types/topic';
import { ACTIVE_TREE_STORAGE_KEY, readStoredTreeId } from '../lib/activeTree';
import {
  resolveActiveTreeId,
  resolveHeaderContext,
  activeViewMode,
  viewModeHref,
  viewModeSubtitle,
  type HeaderTree,
  type ViewMode,
} from '../lib/headerContext';

// ─── Types ─────────────────────────────────────────────────────────────

interface ListTreesResponse {
  trees: HeaderTree[];
}

interface ListTopicsResponse {
  topics: TopicSummary[];
}

interface HealthResponse {
  status: string;
  service?: string;
}

type HealthState = { kind: 'healthy'; label: string } | { kind: 'unhealthy' };

interface Segment {
  mode: ViewMode;
  label: string;
  icon: LucideIcon;
  /** Announced to assistive tech, where the icon carries no meaning. */
  hint: string;
}

const SEGMENTS: readonly Segment[] = [
  { mode: 'tree', label: 'Tree', icon: Network, hint: 'Tree view — graph canvas' },
  { mode: 'detail', label: 'Detail', icon: List, hint: 'Detail view — node list' },
  { mode: 'merge', label: 'Merge', icon: GitMerge, hint: 'Merge view — approvals' },
];

const HEALTH_POLL_MS = 15_000;

// ─── Segmented view selector ───────────────────────────────────────────

/**
 * `role="tablist"` describes the semantics best — three mutually
 * exclusive views of the same context — while the segments stay real
 * `<a>` elements so they are linkable, middle-clickable, and keyboard
 * operable without re-implementing focus management.
 */
function ViewSelector({
  active,
  treeId,
}: {
  active: ViewMode | null;
  treeId: string;
}) {
  return (
    <div
      role="tablist"
      aria-label="View mode"
      data-testid="view-selector"
      className="flex items-center gap-0.5 rounded-lg bg-surface-input/80 p-1 ring-1 ring-inset ring-line-subtle"
    >
      {SEGMENTS.map(({ mode, label, icon: Icon, hint }) => {
        const isActive = active === mode;
        return (
          <NavLink
            key={mode}
            to={viewModeHref(mode, treeId)}
            role="tab"
            aria-selected={isActive}
            aria-label={hint}
            data-mode={mode}
            data-active={isActive ? 'true' : 'false'}
            className={[
              'flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-sm font-medium transition-colors sm:px-3',
              isActive
                ? 'bg-accent-2-600 text-white shadow-sm'
                : 'text-content-tertiary hover:bg-surface-hover/70 hover:text-content-primary',
            ].join(' ')}
          >
            <Icon className="h-4 w-4 shrink-0" aria-hidden="true" />
            {/* Labels fold away on narrow widths; icons keep the control usable. */}
            <span className="hidden lg:inline">{label}</span>
          </NavLink>
        );
      })}
    </div>
  );
}

// ─── Header ────────────────────────────────────────────────────────────

export default function AppHeader() {
  const location = useLocation();
  const [searchParams] = useSearchParams();

  // Depend on param VALUES, never the `searchParams` object — its
  // identity changes every render, which would re-create the callbacks
  // below and spin a fetch loop (UI-02 pitfall).
  const treeParam = searchParams.get('tree') ?? '';
  const topicParam = searchParams.get('topic') ?? '';
  const { pathname } = location;

  const [trees, setTrees] = useState<HeaderTree[]>([]);
  const [topics, setTopics] = useState<TopicSummary[]>([]);

  // Guards against an out-of-order topics response overwriting a newer
  // one (StrictMode double-invokes effects in dev).
  const requestedTree = useRef<string>('');

  const treeId = resolveActiveTreeId({
    pathname,
    treeParam,
    storedTreeId: readStoredTreeId(),
  });

  const loadTrees = useCallback(async () => {
    try {
      const data = await apiGet<ListTreesResponse>('/trees?limit=100');
      setTrees(data.trees ?? []);
    } catch {
      // A nameless header is a cosmetic failure — the pages surface the
      // real error. Fall back to the static title rather than shouting.
      setTrees([]);
    }
  }, []);

  const loadTopics = useCallback(async (id: string) => {
    requestedTree.current = id;
    if (!id) {
      setTopics([]);
      return;
    }
    try {
      const data = await apiGet<ListTopicsResponse>(
        `/topics?tree_id=${encodeURIComponent(id)}&limit=100`,
      );
      if (requestedTree.current !== id) return; // superseded
      setTopics(data.topics ?? []);
    } catch {
      if (requestedTree.current !== id) return;
      setTopics([]);
    }
  }, []);

  useEffect(() => {
    void loadTrees();
  }, [loadTrees]);

  useEffect(() => {
    void loadTopics(treeId);
  }, [loadTopics, treeId]);

  // Re-sync when another surface (topics rail, tree/topic dialogs)
  // changes the active tree or mutates the topic set.
  useEffect(() => {
    const onChanged = () => {
      void loadTrees();
      void loadTopics(readStoredTreeId() || treeId);
    };
    window.addEventListener(ACTIVE_TREE_STORAGE_KEY, onChanged);
    return () => window.removeEventListener(ACTIVE_TREE_STORAGE_KEY, onChanged);
  }, [loadTrees, loadTopics, treeId]);

  const mode = activeViewMode(pathname);
  const { title, count } = resolveHeaderContext({
    pathname,
    topicParam,
    treeId,
    topics,
    trees,
  });

  const [health, setHealth] = useState<HealthState>({ kind: 'unhealthy' });
  const healthy = health.kind === 'healthy';
  const backendLabel = healthy ? `Backend: ${health.label}` : 'Backend: unreachable';
  const backendTitle = healthy ? 'Backend is healthy' : 'Backend is unreachable';

  useEffect(() => {
    let cancelled = false;

    async function check() {
      try {
        const res = await fetch('/health');
        if (!res.ok || cancelled) return;
        const data = (await res.json()) as HealthResponse;
        if (data.status === 'ok') {
          setHealth({ kind: 'healthy', label: data.service ?? data.status });
        } else {
          setHealth({ kind: 'unhealthy' });
        }
      } catch {
        if (!cancelled) {
          setHealth({ kind: 'unhealthy' });
        }
      }
    }

    void check();
    const id = setInterval(check, HEALTH_POLL_MS);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, []);

  return (
    <header
      className="flex h-16 shrink-0 items-center gap-4 border-b border-line-subtle bg-surface-panel/80 px-4 backdrop-blur-md sm:px-6"
      role="banner"
    >
      {/* Context — title + count badge, subtitle beneath */}
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline gap-2">
          <h2
            data-testid="header-context-title"
            className="min-w-0 truncate text-lg font-semibold tracking-tight text-content-primary sm:text-xl"
          >
            {title}
          </h2>
          {count !== null && (
            <span
              data-testid="header-context-count"
              aria-label={`${count} ${count === 1 ? 'node' : 'nodes'}`}
              className="shrink-0 rounded-sm bg-surface-input px-1.5 py-0.5 text-xs font-medium tabular-nums text-content-secondary ring-1 ring-inset ring-line-subtle"
            >
              {count}
            </span>
          )}
        </div>
        <p className="truncate text-xs text-content-muted sm:text-[13px]">
          {viewModeSubtitle(mode)}
        </p>
      </div>

      {/* View mode — Tree / Detail / Merge */}
      <ViewSelector active={mode} treeId={treeId} />

      {/* Utility zone — backend status (dev indicator) */}
      <div className="flex shrink-0 items-center gap-3">
        <span
          data-testid="backend-status"
          title={backendTitle}
          aria-label={backendTitle}
          className="inline-flex items-center gap-1.5 rounded-sm bg-surface-input px-2 py-1 text-xs text-content-muted ring-1 ring-inset ring-line-subtle"
        >
          <span
            aria-hidden="true"
            className={[
              'h-1.5 w-1.5 rounded-full',
              healthy ? 'bg-status-success' : 'bg-status-danger',
            ].join(' ')}
          />
          {/* Narrow widths keep the dot; the label folds into the a11y tree. */}
          <span className="hidden xl:inline">{backendLabel}</span>
          <span className="sr-only xl:hidden">{backendLabel}</span>
        </span>
      </div>
    </header>
  );
}
