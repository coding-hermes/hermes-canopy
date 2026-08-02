/**
 * Hermes Canopy — Header context resolution (UI-03, Phase 11 Mockup Parity)
 *
 * The app header (docs/mockups/mockup-1.png) is context-aware: it names
 * whatever the user is currently looking at, badges it with that
 * context's node count, and highlights the matching view mode in the
 * Tree / Detail / Merge selector.
 *
 * All of that is a pure function of the URL plus two already-fetched
 * lists, so the resolution rules live here — outside the component —
 * where they can be unit-tested without a router or a network.
 *
 * Resolution chain (widest signal wins, most specific first):
 *
 *   tree id   /tree/<id> path param → ?tree= → persisted active tree
 *   title     active topic → active tree → "Knowledge Canopy"
 *   count     the resolved context's node_count, or null (badge hidden)
 *
 * `null` — never `0` — is used for "unknown count" so the badge can be
 * omitted entirely rather than asserting a hardcoded number.
 */

import type { TopicSummary } from '../types/topic';

// ─── Types ─────────────────────────────────────────────────────────────

/** The three view modes offered by the header's segmented control. */
export type ViewMode = 'tree' | 'detail' | 'merge';

/** Minimal tree shape the header needs. `GET /trees` returns a superset. */
export interface HeaderTree {
  id: string;
  title: string;
  /** Absent on older payloads — treated as "unknown", not zero. */
  node_count?: number;
}

/** What the header's left zone renders. */
export interface HeaderContext {
  title: string;
  /** Node count for the badge, or null when unknown (badge hidden). */
  count: number | null;
  /** Which link of the chain produced the title — useful for tests/aria. */
  source: 'topic' | 'tree' | 'fallback';
}

// ─── Constants ─────────────────────────────────────────────────────────

/** Title shown when no tree or topic context can be resolved. */
export const FALLBACK_TITLE = 'Knowledge Canopy';

/**
 * The only route that carries a `?topic=` deep link (TopicsRail writes
 * it, TopicsPage reads it). Scoping topic resolution to this path keeps
 * the header's title in lockstep with the rail's highlighted pill.
 */
export const TOPIC_CONTEXT_PATH = '/topics';

const TREE_ROUTE = /^\/tree\/([^/?#]+)/;

const SUBTITLES: Record<ViewMode, string> = {
  tree: 'Macro tree view',
  detail: 'Node detail view',
  merge: 'Merge review view',
};

// ─── Route helpers ─────────────────────────────────────────────────────

/** Extract the tree id from a `/tree/:treeId` canvas route. '' if absent. */
export function treeIdFromPath(pathname: string): string {
  const match = TREE_ROUTE.exec(pathname);
  if (!match) return '';
  try {
    return decodeURIComponent(match[1]);
  } catch {
    // Malformed percent-encoding — use the raw segment rather than throw.
    return match[1];
  }
}

/**
 * Resolve the tree the header describes.
 *
 * The canvas route wins because the user is literally looking at that
 * tree; `?tree=` is the deep-link the topics rail emits; the persisted
 * id is the last explicit choice (see `lib/activeTree.ts`).
 */
export function resolveActiveTreeId(input: {
  pathname: string;
  treeParam: string;
  storedTreeId: string;
}): string {
  return (
    treeIdFromPath(input.pathname) ||
    input.treeParam ||
    input.storedTreeId ||
    ''
  );
}

/**
 * Which segment of the view selector is current, or null on routes that
 * are outside the Tree/Detail/Merge triad (dashboard, topics, cards) —
 * those highlight nothing rather than lying about the active view.
 */
export function activeViewMode(pathname: string): ViewMode | null {
  if (pathname === '/nodes' || pathname.startsWith('/nodes/')) return 'detail';
  if (pathname === '/approvals' || pathname.startsWith('/approvals/')) {
    return 'merge';
  }
  if (pathname === '/trees' || pathname === '/tree' || pathname.startsWith('/tree/')) {
    return 'tree';
  }
  return null;
}

/** Destination for a segment. Tree targets the canvas when one is known. */
export function viewModeHref(mode: ViewMode, treeId: string): string {
  switch (mode) {
    case 'tree':
      // No tree resolved yet → the picker, which is the canvas's entry point.
      return treeId ? `/tree/${encodeURIComponent(treeId)}` : '/trees';
    case 'detail':
      return '/nodes';
    case 'merge':
      return '/approvals';
  }
}

/** Subtitle under the context title. Defaults to the macro (tree) view. */
export function viewModeSubtitle(mode: ViewMode | null): string {
  return mode ? SUBTITLES[mode] : SUBTITLES.tree;
}

// ─── Context resolution ────────────────────────────────────────────────

/**
 * Resolve the header's title + count badge from the current URL and the
 * lists already loaded by the header.
 *
 * A context only wins if it RESOLVES — an unknown topic id or a tree id
 * that isn't in the list falls through to the next link in the chain,
 * so a stale deep link degrades to a sensible title instead of a blank
 * header.
 */
export function resolveHeaderContext(input: {
  pathname: string;
  topicParam: string;
  treeId: string;
  topics: readonly TopicSummary[];
  trees: readonly HeaderTree[];
}): HeaderContext {
  const { pathname, topicParam, treeId, topics, trees } = input;

  if (topicParam && pathname === TOPIC_CONTEXT_PATH) {
    const topic = topics.find((t) => t.id === topicParam);
    if (topic?.title) {
      return {
        title: topic.title,
        count: typeof topic.node_count === 'number' ? topic.node_count : null,
        source: 'topic',
      };
    }
  }

  if (treeId) {
    const tree = trees.find((t) => t.id === treeId);
    if (tree?.title) {
      return {
        title: tree.title,
        count: typeof tree.node_count === 'number' ? tree.node_count : null,
        source: 'tree',
      };
    }
  }

  return { title: FALLBACK_TITLE, count: null, source: 'fallback' };
}
