/**
 * Hermes Canopy — Context Preview API (GAP-096)
 *
 * Typed client for `GET /api/v1/context/{node_id}` — the Context Compiler's
 * preview endpoint (internal/handler/context_handler.go). Until now the
 * endpoint's only frontend consumer read it through the manifest panel's
 * hook; the audit-before-send gate needs a first-class caller so a run can
 * be PREVIEWED before POST /gateway/runs is allowed to fire.
 *
 * Types mirror the handler's 200 body (`internal/context.CompiledContext`)
 * and reuse the wire shapes already declared for the manifest panel
 * (lib/contextManifest.ts) — one source of truth for what the compiler
 * sends, not two.
 */

import { apiGet } from './api.ts';
import {
  contextRequestPath,
  type RawManifest,
} from './contextManifest.ts';

// ─── Wire shapes (mirror internal/context.CompiledContext) ────────────

/**
 * The endpoint's 200 body. Both fields `omitempty` on the Go side, so a
 * compile that produced no manifest carries absence, not an empty object.
 */
export interface CompiledContextResponse {
  content?: string | null;
  manifest?: RawManifest | null;
}

// ─── API calls ────────────────────────────────────────────────────────

/**
 * Compile (preview) a node's budgeted context.
 *
 * `budget` OMITTED (or unusable — non-finite, below 1) sends NO `budget`
 * parameter: the server applies its own default, which for a named model
 * is a percentage of that model's context window (GAP-080). An explicit
 * positive budget is sent verbatim; the server may still cap it against
 * the model window / flat ceiling, and the honest effective number is
 * always `manifest.tokenBudget` in the response.
 *
 * Throws with the server's own error message on failure (the apiGet
 * contract) — callers render it, never auto-send through it.
 */
export function getContextPreview(
  nodeId: string,
  budget?: number | null,
): Promise<CompiledContextResponse> {
  return apiGet<CompiledContextResponse>(
    contextRequestPath(nodeId, budget),
  );
}

// ─── Manifest derivations for the audit gate ──────────────────────────

/** One flattened "included source" row for the audit dialog's short list. */
export interface ContextAuditSource {
  id: string;
  kind: string;
  title: string;
  tokenCount: number;
  truncated: boolean;
}

function sourcesOf(items: RawManifest['ancestry']): ContextAuditSource[] {
  if (!Array.isArray(items)) return [];
  return items.map((item) => ({
    id: item?.id ?? '',
    kind: item?.kind ?? '',
    title: item?.title ?? '',
    tokenCount:
      typeof item?.tokenCount === 'number' && Number.isFinite(item.tokenCount)
        ? item.tokenCount
        : 0,
    truncated: item?.truncated === true,
  }));
}

/**
 * The manifest's included sources in compiler order — ancestry, then
 * references, then cards, then the retrieved tier — capped at `limit`.
 *
 * Read straight off the RAW manifest (nil slices are absent, not `[]`),
 * so it never throws and never depends on the view-side normalisation.
 */
export function topAuditSources(
  manifest: RawManifest | null | undefined,
  limit: number,
): ContextAuditSource[] {
  if (!manifest || typeof manifest !== 'object' || limit <= 0) return [];
  const all = [
    ...sourcesOf(manifest.ancestry),
    ...sourcesOf(manifest.references),
    ...sourcesOf(manifest.cards),
    ...sourcesOf(manifest.retrieved),
  ];
  return all.slice(0, limit);
}

/**
 * How many sources the manifest included across all four tiers — the
 * count shown beside the omission note in the audit dialog.
 */
export function auditIncludedCount(
  manifest: RawManifest | null | undefined,
): number {
  if (!manifest || typeof manifest !== 'object') return 0;
  const count = (items: RawManifest['ancestry']): number =>
    Array.isArray(items) ? items.length : 0;
  return (
    count(manifest.ancestry) +
    count(manifest.references) +
    count(manifest.cards) +
    count(manifest.retrieved)
  );
}
