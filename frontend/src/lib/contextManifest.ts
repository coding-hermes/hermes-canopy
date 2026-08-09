/**
 * Hermes Canopy — Context manifest wire shapes + derivations (WIRE-002)
 *
 * `GET /api/v1/context/{node_id}` (internal/handler/context_handler.go) is
 * the Context Compiler's user-visible artifact: given a node it walks the
 * ancestry chain, resolves `#references`, applies a token budget and
 * reports EXACTLY what it included. The endpoint has been wired and
 * tested on the backend since IMPL-GAP-001 and had zero UI consumers —
 * this module is the frontend half.
 *
 * Two things here rather than in the component:
 *
 *   1. Wire → view normalisation. Go marshals a nil slice as `null`, not
 *      `[]`, so `manifest.ancestry` is `null` for a root node with no
 *      ancestors, and `warnings`/`truncationMarkers` are `null` on every
 *      healthy response. A component that maps over them straight off the
 *      wire crashes on the happy path. `normaliseManifest` is the only
 *      place that has to know.
 *   2. Pure derivations (usage ratio, severity, phrasing) — unit-testable
 *      without a renderer, and a `.tsx` module that exports non-components
 *      breaks React Fast Refresh.
 */

import { shortNodeId } from './nodeShortId.ts';

// ─── Constants ─────────────────────────────────────────────────────────

/**
 * Budget requested by the UI. The handler clamps anything above 10× its
 * own default, and defaults to that same value when `budget` is absent —
 * sending it explicitly keeps the panel's label honest regardless of how
 * the server is configured.
 */
export const DEFAULT_CONTEXT_BUDGET = 8000;

/** Usage ratio above which the meter warns. */
const WARN_RATIO = 0.8;

// ─── Wire shapes (as JSON, nullable slices included) ───────────────────

/** One component of the compiled context, as it arrives. */
export interface RawManifestItem {
  id?: string | null;
  kind?: string | null;
  title?: string | null;
  tokenCount?: number | null;
  truncated?: boolean | null;
}

/** `internal/context.Manifest`, as it arrives (nil slices → `null`). */
export interface RawManifest {
  requestId?: string | null;
  nodeId?: string | null;
  compiledAt?: string | null;
  tokenBudget?: number | null;
  tokensUsed?: number | null;
  ancestry?: RawManifestItem[] | null;
  references?: RawManifestItem[] | null;
  cards?: RawManifestItem[] | null;
  omittedCount?: number | null;
  omittedReason?: string | null;
  truncationMarkers?: string[] | null;
  warnings?: string[] | null;
}

/** `internal/context.CompiledContext` — the endpoint's 200 body. */
export interface CompiledContext {
  content?: string | null;
  manifest?: RawManifest | null;
}

// ─── View shapes (normalised — arrays are always arrays) ───────────────

export type ManifestItemKind = 'node' | 'topic' | 'card';

export interface ManifestItem {
  id: string;
  kind: ManifestItemKind;
  title: string;
  tokenCount: number;
  truncated: boolean;
}

export interface Manifest {
  requestId: string;
  nodeId: string;
  compiledAt: string;
  tokenBudget: number;
  tokensUsed: number;
  ancestry: ManifestItem[];
  references: ManifestItem[];
  cards: ManifestItem[];
  omittedCount: number;
  /** `"budget"` | `"depth"` | `""` */
  omittedReason: string;
  truncationMarkers: string[];
  warnings: string[];
}

// ─── Request ───────────────────────────────────────────────────────────

/**
 * Node ids the compiler can actually accept.
 *
 * `parseNodeID` 400s on anything that is not a UUID, and the canvas can
 * hold ids that never reached the backend — a locally-seeded demo tree
 * (`__canopySeedDemoTree`) or a synthetic ghost slot. Filtering here
 * keeps those clicks from generating guaranteed-failing requests.
 */
export function isCompilableNodeId(id: string | null | undefined): boolean {
  if (!id) return false;
  return /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(
    id.trim(),
  );
}

/** Path passed to `apiGet` — `/context/{id}?budget=N`. */
export function contextRequestPath(
  nodeId: string,
  budget: number = DEFAULT_CONTEXT_BUDGET,
): string {
  const b = Number.isFinite(budget) && budget >= 1 ? Math.floor(budget) : DEFAULT_CONTEXT_BUDGET;
  return `/context/${encodeURIComponent(nodeId)}?budget=${b}`;
}

// ─── Normalisation ─────────────────────────────────────────────────────

const KINDS: readonly ManifestItemKind[] = ['node', 'topic', 'card'];

function toKind(raw: string | null | undefined): ManifestItemKind {
  const k = (raw ?? '').toLowerCase();
  return KINDS.find((valid) => valid === k) ?? 'node';
}

function toCount(raw: number | null | undefined): number {
  return typeof raw === 'number' && Number.isFinite(raw) ? raw : 0;
}

function toStrings(raw: unknown): string[] {
  if (!Array.isArray(raw)) return [];
  return raw.filter((s): s is string => typeof s === 'string' && s.length > 0);
}

function toItems(raw: RawManifestItem[] | null | undefined): ManifestItem[] {
  if (!Array.isArray(raw)) return [];
  return raw.map((item) => ({
    id: item?.id ?? '',
    kind: toKind(item?.kind),
    title: item?.title ?? '',
    tokenCount: toCount(item?.tokenCount),
    truncated: item?.truncated === true,
  }));
}

/**
 * Wire body → renderable manifest, or `null` when the payload carried no
 * manifest at all. Never throws: a degraded compile is still a result,
 * and the panel must not be able to take the tree view down with it.
 */
export function normaliseManifest(
  body: CompiledContext | null | undefined,
): Manifest | null {
  const raw = body?.manifest;
  if (!raw || typeof raw !== 'object') return null;

  return {
    requestId: raw.requestId ?? '',
    nodeId: raw.nodeId ?? '',
    compiledAt: raw.compiledAt ?? '',
    tokenBudget: toCount(raw.tokenBudget),
    tokensUsed: toCount(raw.tokensUsed),
    ancestry: toItems(raw.ancestry),
    references: toItems(raw.references),
    cards: toItems(raw.cards),
    omittedCount: toCount(raw.omittedCount),
    omittedReason: raw.omittedReason ?? '',
    truncationMarkers: toStrings(raw.truncationMarkers),
    warnings: toStrings(raw.warnings),
  };
}

// ─── Budget phrasing ───────────────────────────────────────────────────

/** `1240` → `"1,240"`. Grouped explicitly so the label is locale-stable. */
export function formatTokenCount(n: number): string {
  const safe = Number.isFinite(n) ? Math.round(n) : 0;
  return safe.toLocaleString('en-US');
}

/** `"1,240 / 8,000 tokens"` — the panel's headline. */
export function formatTokenUsage(used: number, budget: number): string {
  return `${formatTokenCount(used)} / ${formatTokenCount(budget)} tokens`;
}

/**
 * Fraction of the budget consumed, clamped to 0…1 for the meter width.
 * A zero/absent budget reads as full rather than dividing by zero.
 */
export function budgetUsageRatio(used: number, budget: number): number {
  if (!Number.isFinite(budget) || budget <= 0) return 1;
  const u = Number.isFinite(used) && used > 0 ? used : 0;
  return Math.min(1, u / budget);
}

/**
 * Meter colour band. `over` is reachable — the compiler emits a
 * "tokens used (N) exceeds budget (M)" warning rather than failing, so
 * the UI has to be able to show it.
 */
export function budgetSeverity(
  used: number,
  budget: number,
): 'ok' | 'warn' | 'over' {
  if (Number.isFinite(budget) && budget > 0 && used > budget) return 'over';
  return budgetUsageRatio(used, budget) >= WARN_RATIO ? 'warn' : 'ok';
}

// ─── Item phrasing ─────────────────────────────────────────────────────

/**
 * Label for one manifest row. The compiler sends a 120-char content
 * preview as `title`, which is empty for an empty node — fall back to the
 * distinguishing short id (UI-08) rather than rendering a blank row.
 */
export function manifestItemTitle(item: ManifestItem): string {
  const title = item.title.trim();
  if (title) return title;
  const short = shortNodeId(item.id);
  return short || 'Untitled';
}

/**
 * `"3 omitted (budget)"` — what the compiler dropped and why, or `null`
 * when it dropped nothing. This is the whole point of the manifest: an
 * omission the user cannot see is an omission they cannot trust.
 */
export function omissionNote(manifest: Manifest): string | null {
  if (manifest.omittedCount <= 0) return null;
  const noun = manifest.omittedCount === 1 ? 'item' : 'items';
  const reason = manifest.omittedReason.trim();
  return reason
    ? `${manifest.omittedCount} ${noun} omitted (${reason})`
    : `${manifest.omittedCount} ${noun} omitted`;
}

// ─── Failure phrasing ──────────────────────────────────────────────────

/**
 * A failed compile is a subtle note, never a banner: the tree canvas is
 * the page, and a node whose context cannot be compiled (a local-only
 * replica node, a database blip) must not look like the tree broke.
 *
 * `apiGet` throws with the server's `error.message`, so the classification
 * is on that text — the wrapper does not surface a status code.
 */
export function contextErrorNote(message: string): string {
  const m = (message ?? '').toLowerCase();
  if (m.includes('not found')) return 'No compiled context for this node.';
  if (m.includes('unavailable')) return 'Context service unavailable.';
  if (m.includes('budget')) return 'Context budget rejected by the server.';
  return 'Context unavailable.';
}
