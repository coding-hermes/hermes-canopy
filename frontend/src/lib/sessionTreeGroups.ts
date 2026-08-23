/**
 * Hermes Canopy — Session-tree grouping (UI-LIVE-001)
 *
 * The Trees page classifies imported Hermes sessions by SOURCE and merges
 * continuation segments into their parent session's tree. All of that is
 * set arithmetic over the flat tree list, so it lives here as pure
 * functions — the page keeps a `Set<SourceGroup>` of collapsed groups in
 * React state and asks this module what the list implies.
 *
 * `normalizeSource` mirrors hermes-webui's
 * `normalize_agent_session_source()` taxonomy (api/agent_sessions.py):
 * raw sources collapse into a small, stable set of groups. Keep the two
 * in lockstep — this is a deliberate frontend copy, not a shared import
 * (the backend serves the RAW source string; classification is a display
 * concern).
 */

// ─── Types ─────────────────────────────────────────────────────────────

/** The additive session fields served on GET /trees summaries (omitempty). */
export interface SessionFields {
  id: string;
  title: string;
  description: string;
  /** Hermes session id when the tree was imported (absent otherwise). */
  session_id?: string;
  /** Parent session id — marks this tree as a continuation segment. */
  parent_session_id?: string;
  /** RAW Hermes source string (e.g. "telegram", "wecom_callback"). */
  source?: string;
}

/** Stable source groups — mirrors hermes-webui's session_source values. */
export const SOURCE_GROUPS = [
  'cli',
  'cron',
  'messaging',
  'api',
  'tool',
  'webhook',
  'kanban',
  'webui',
  'unknown',
] as const;

export type SourceGroup = (typeof SOURCE_GROUPS)[number];

/** Human label per group (header text + chips). */
export const SOURCE_LABELS: Record<SourceGroup, string> = {
  cli: 'CLI',
  cron: 'Cron',
  messaging: 'Messaging',
  api: 'API',
  tool: 'Tool',
  webhook: 'Webhook',
  kanban: 'Kanban',
  webui: 'WebUI',
  unknown: 'Unknown',
};

/** Chip styling per group — dark-theme AA on surface-panel. */
export const SOURCE_CHIP_CLASSES: Record<SourceGroup, string> = {
  cli: 'bg-cyan-500/10 text-cyan-300 border-cyan-500/30',
  cron: 'bg-amber-500/10 text-amber-300 border-amber-500/30',
  messaging: 'bg-sky-500/10 text-sky-300 border-sky-500/30',
  api: 'bg-violet-500/10 text-violet-300 border-violet-500/30',
  tool: 'bg-emerald-500/10 text-emerald-300 border-emerald-500/30',
  webhook: 'bg-rose-500/10 text-rose-300 border-rose-500/30',
  kanban: 'bg-fuchsia-500/10 text-fuchsia-300 border-fuchsia-500/30',
  webui: 'bg-indigo-500/10 text-indigo-300 border-indigo-500/30',
  unknown: 'bg-zinc-500/10 text-zinc-300 border-zinc-500/30',
};

/** Raw Hermes sources classified as messaging (hermes-webui MESSAGING_SOURCES). */
const MESSAGING_SOURCES: ReadonlySet<string> = new Set([
  'discord',
  'email',
  'wecom',
  'wecom_callback',
  'slack',
  'telegram',
  'weixin',
  'matrix',
]);

/** Raw sources that are local interactive clients → cli group. */
const CLI_SOURCES: ReadonlySet<string> = new Set(['acp', 'cli', 'tui']);

// ─── Classification ────────────────────────────────────────────────────

/**
 * Map a raw Hermes session source to its display group. Empty /
 * unrecognized / 'unknown' inputs all land in 'unknown'.
 */
export function normalizeSource(raw: string | null | undefined): SourceGroup {
  const value = (raw ?? '').trim().toLowerCase();
  if (!value || value === 'unknown') return 'unknown';
  if (value === 'webui') return 'webui';
  if (CLI_SOURCES.has(value)) return 'cli';
  if (MESSAGING_SOURCES.has(value)) return 'messaging';
  switch (value) {
    case 'cron':
      return 'cron';
    case 'webhook':
      return 'webhook';
    case 'kanban':
      return 'kanban';
    case 'tool':
      return 'tool';
    case 'api_server':
      return 'api';
    default:
      return 'unknown';
  }
}

// ─── Grouping + continuation merge ─────────────────────────────────────

/** One top-level session tree plus its merged continuation segments. */
export interface SessionTreeNode<T extends SessionFields = SessionFields> {
  tree: T;
  /** Continuation segments whose parent_session_id === tree.session_id. */
  continuations: T[];
}

/** A collapsible source group of top-level session trees. */
export interface SourceGroupView<T extends SessionFields = SessionFields> {
  source: SourceGroup;
  label: string;
  trees: SessionTreeNode<T>[];
}

export interface GroupedSessionLists<T extends SessionFields = SessionFields> {
  /** Session trees grouped by normalized source, in SOURCE_GROUPS order. */
  groups: SourceGroupView<T>[];
  /** Trees without session metadata — render as today ("Workspace"). */
  ungrouped: T[];
}

/**
 * Partition a flat tree list into source groups + workspace trees,
 * merging continuation segments under their parent session's node.
 *
 * Rules:
 *  - Only trees WITH `session_id` are session trees (backend contract).
 *  - A tree with `parent_session_id` whose parent is also in the list
 *    becomes a continuation of that parent — never a top-level entry.
 *  - An orphan continuation (parent not loaded — e.g. pagination window)
 *    stays visible at top level in its own source group rather than
 *    disappearing.
 *  - Incoming order is preserved everywhere (the API returns
 *    created_at DESC; re-sorting here would fight pagination).
 */
export function groupSessionTrees<T extends SessionFields>(
  trees: readonly T[],
): GroupedSessionLists<T> {
  const bySessionId = new Map<string, T>();
  for (const t of trees) {
    if (t.session_id && !bySessionId.has(t.session_id)) bySessionId.set(t.session_id, t);
  }

  const isContinuation = new Set<string>();
  const continuationsByParent = new Map<string, T[]>();
  for (const t of trees) {
    if (!t.parent_session_id) continue;
    const parent = bySessionId.get(t.parent_session_id);
    // Same tree can't be its own parent; skip self-references.
    if (!parent || parent.id === t.id) continue;
    isContinuation.add(t.id);
    const parentSessionId = parent.session_id as string;
    const list = continuationsByParent.get(parentSessionId);
    if (list) {
      list.push(t);
    } else {
      continuationsByParent.set(parentSessionId, [t]);
    }
  }

  const topLevelByGroup = new Map<SourceGroup, SessionTreeNode<T>[]>([]);
  const ungrouped: T[] = [];
  for (const t of trees) {
    if (isContinuation.has(t.id)) continue;
    if (!t.session_id) {
      ungrouped.push(t);
      continue;
    }
    const node: SessionTreeNode<T> = {
      tree: t,
      continuations: continuationsByParent.get(t.session_id) ?? [],
    };
    const group = normalizeSource(t.source);
    const list = topLevelByGroup.get(group);
    if (list) {
      list.push(node);
    } else {
      topLevelByGroup.set(group, [node]);
    }
  }

  const groups: SourceGroupView<T>[] = [];
  for (const source of SOURCE_GROUPS) {
    const list = topLevelByGroup.get(source);
    if (list && list.length > 0) {
      groups.push({ source, label: SOURCE_LABELS[source], trees: list });
    }
  }
  return { groups, ungrouped };
}

// ─── Search ────────────────────────────────────────────────────────────

/**
 * Case-insensitive substring match over title + description. Empty query
 * matches everything (returns true).
 */
export function matchesSearch(
  tree: Pick<SessionFields, 'title' | 'description'>,
  query: string,
): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return (
    tree.title.toLowerCase().includes(q) || (tree.description ?? '').toLowerCase().includes(q)
  );
}

/** Convenience: filter then group in one pass. */
export function buildTreeSections<T extends SessionFields>(
  trees: readonly T[],
  query: string,
): GroupedSessionLists<T> {
  const filtered = trees.filter((t) => matchesSearch(t, query));
  return groupSessionTrees(filtered);
}

/** Total number of trees across all groups (for header counts). */
export function countGroupedTrees<T extends SessionFields>(
  sections: GroupedSessionLists<T>,
): number {
  let n = sections.ungrouped.length;
  for (const g of sections.groups) n += g.trees.length;
  return n;
}
