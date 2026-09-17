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
 * Budget requested by the UI when the user does NOT choose one, and the
 * basis of the backend's explicit-request ceiling (`DEFAULT_CONTEXT_BUDGET *
 * 10` = 80 000). The panel sends NO budget parameter unless the user moved
 * the slider — the server derives its own default from the selected model's
 * context window (GAP-080), and pinning this number in the UI made that
 * derivation unreachable from the product.
 */
export const DEFAULT_CONTEXT_BUDGET = 8000;

/** Smallest budget the slider can request. Below this a compile is useless. */
export const MIN_CONTEXT_BUDGET = 256;

/** Slider granularity — the range moves in whole 256-token steps. */
export const CONTEXT_BUDGET_STEP = 256;

/** The backend's ceiling for an explicit budget with no known model window. */
export const MAX_CONTEXT_BUDGET = DEFAULT_CONTEXT_BUDGET * 10;

/**
 * Hex characters of `manifestHash` shown in the compact form (GAP-080
 * phase 5a). 12 chars = 48 bits, spot-the-difference long enough for a human
 * comparing two manifests, short enough to sit inline; the full 64-char value
 * is always in the element's `title`.
 */
export const MANIFEST_HASH_SHORT_LENGTH = 12;

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
  /**
   * Stable digest of the compiled payload (GAP-080 phase 5a). Optional on the
   * wire: a manifest recorded before the field existed carries no key at all.
   */
  manifestHash?: string | null;
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
  /**
   * Stable digest of the compiled payload (GAP-080 phase 5a) — lowercase
   * 64-hex sha256 over the manifest's content-bearing fields. It is what makes
   * the panel's preview compile and a run record's manifest comparable, and it
   * is recomputable from the record's JSON by anyone.
   *
   * `''` means the record carried no digest (a manifest recorded before the
   * field existed). Never invent one — an invented hash is a claim about a
   * payload nobody hashed.
   */
  manifestHash: string;
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

/**
 * Path passed to `apiGet` — `/context/{id}`, optionally `?budget=N`, then
 * `?model=…`.
 *
 * `budget` is OMITTED entirely when it is `undefined`/`null` (or unusable:
 * non-finite, or below 1, which the handler answers 400 for). That absence
 * is what "Auto" means: the server applies its own default, which for a
 * named model is a percentage of that model's context window (GAP-080).
 * An explicit budget is sent verbatim.
 *
 * Parameter order is stable and asserted by tests: `budget` first, then
 * `model`, and no `?` at all when neither is present.
 */
export function contextRequestPath(
  nodeId: string,
  budget?: number | null,
  model?: string | null,
): string {
  const params: string[] = [];
  const b = normaliseBudget(budget);
  if (b !== null) params.push(`budget=${b}`);
  const name = typeof model === 'string' ? model.trim() : '';
  if (name) params.push(`model=${encodeURIComponent(name)}`);
  const query = params.length > 0 ? `?${params.join('&')}` : '';
  return `/context/${encodeURIComponent(nodeId)}${query}`;
}

/**
 * A budget the handler will accept, or `null` for "no budget parameter".
 * `null`/`undefined` are the caller asking for Auto; a zero, negative or
 * non-finite value is unusable and must NOT become `?budget=0` — the
 * handler 400s that, and a request the server is guaranteed to reject is
 * worse than no request.
 */
export function normaliseBudget(
  budget: number | null | undefined,
): number | null {
  if (budget === null || budget === undefined) return null;
  if (!Number.isFinite(budget) || budget < 1) return null;
  return Math.floor(budget);
}

// ─── Model catalog (GET /api/v1/gateway/models — GAP-080 phase 2b) ─────

/** One entry of the models route, as it arrives. */
export interface RawGatewayModel {
  id?: string | null;
  context_window?: number | null;
  desired_budget?: number | null;
}

/** The models route's envelope, as it arrives (nil slices → `null`). */
export interface RawGatewayModelCatalog {
  models?: RawGatewayModel[] | null;
  percent?: number | null;
  default_budget?: number | null;
  source?: string | null;
}

/** One model the user can choose, with the budget it would get. */
export interface ContextModelOption {
  id: string;
  contextWindow: number;
  desiredBudget: number;
}

/**
 * Wire body → the select's options. Never throws and never returns `null`:
 * a failed fetch, a non-JSON body and a `models: null` envelope all degrade
 * to `[]`, which the panel renders as its single "Server default" option.
 * Ids are trimmed and de-duplicated (the gateway may list a model twice);
 * entries are left in the order the server sent them (it sorts).
 */
export function normaliseModelOptions(body: unknown): ContextModelOption[] {
  const raw = (body as RawGatewayModelCatalog | null | undefined)?.models;
  if (!Array.isArray(raw)) return [];

  const seen = new Set<string>();
  const options: ContextModelOption[] = [];
  for (const item of raw) {
    const id = typeof item?.id === 'string' ? item.id.trim() : '';
    if (!id || seen.has(id)) continue;
    seen.add(id);
    options.push({
      id,
      contextWindow: toCount(item?.context_window),
      desiredBudget: toCount(item?.desired_budget),
    });
  }
  return options;
}

/**
 * Slider maximum: the selected model's `desired_budget` from the models
 * route when the catalog knows one, else the backend's explicit-request
 * ceiling. Floored at the slider minimum so a tiny model (a 50-token window
 * derives a budget of 1) cannot produce a range whose min exceeds its max.
 */
export function contextBudgetCeiling(
  selected: ContextModelOption | undefined,
): number {
  const desired = selected?.desiredBudget ?? 0;
  return Math.max(MIN_CONTEXT_BUDGET, desired > 0 ? desired : MAX_CONTEXT_BUDGET);
}

/**
 * The capped-request note, or `null` when nothing was capped.
 *
 * The whole point of the honesty requirement: when the panel ASKED for a
 * budget the server did not grant, the effective number is the manifest's
 * `tokenBudget`, and the difference has to be visible — a control that
 * displays the requested number as if it were in force is lying about what
 * the model was sent.
 */
export function budgetCappedNote(
  requested: number | null,
  effective: number,
): string | null {
  if (requested === null || !Number.isFinite(requested)) return null;
  if (!Number.isFinite(effective) || effective >= requested) return null;
  return `Server capped the request at ${formatTokenCount(effective)} tokens (requested ${formatTokenCount(requested)}).`;
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
    // A record from before GAP-080 phase 5a has no digest at all. `''` is the
    // honest degradation: the components render nothing for it rather than a
    // placeholder hash nothing can be compared against.
    manifestHash: raw.manifestHash ?? '',
  };
}

// ─── Manifest digest (GAP-080 phase 5a) ────────────────────────────────

/**
 * The compact, comparable form of a manifest digest — `null` when the record
 * carries none.
 *
 * Two surfaces render it with the SAME short form on purpose: the panel's
 * preview compile (the BEFORE side) and the run record's manifest (the AFTER
 * side). Equal short forms mean the payload the run was given is the payload
 * that was previewed; the full 64-char value rides in the element's `title`
 * for the copy-paste case.
 *
 * `null` — not `''`, not a placeholder — is the absent case, and callers render
 * NOTHING for it. A manifest recorded before this field existed has no digest;
 * an empty chip would read as "digest = nothing", and a placeholder would be a
 * claim about a payload nobody hashed.
 */
export function manifestHashShort(
  hash: string | null | undefined,
): string | null {
  const value = typeof hash === 'string' ? hash.trim() : '';
  if (!value) return null;
  return value.slice(0, MANIFEST_HASH_SHORT_LENGTH);
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
