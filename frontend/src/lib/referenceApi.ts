/**
 * Hermes Canopy — Reference API client (TM-04)
 *
 * Spec: SPEC-TM-04 §6 API Endpoints.
 * Uses the shared apiGet/apiPost helpers from lib/api.ts.
 */

import { apiGet, apiPost } from './api';
import type {
  AutocompleteResponse,
  ResolveReferencesRequest,
  ReferenceResolutionResult,
  InjectWithReferencesRequest,
} from '../types/reference';

export interface InjectWithReferencesResponse {
  context: {
    topics: unknown[];
    merged_text: string;
    total_nodes: number;
    truncated: boolean;
  };
  event_id: string;
  not_found?: import('../types/reference').ParsedReference[];
  too_many: boolean;
  warning?: string;
}

// ── Autocomplete ──────────────────────────────────────────────────────────

export function autocompleteReferences(
  treeId: string,
  prefix: string,
  opts?: {
    limit?: number;
    include?: 'active' | 'archived' | 'all';
  },
): Promise<AutocompleteResponse> {
  const params = new URLSearchParams({ prefix });
  if (opts?.limit) params.set('limit', String(opts.limit));
  if (opts?.include) params.set('include', opts.include);
  return apiGet<AutocompleteResponse>(
    `/trees/${treeId}/references/autocomplete?${params.toString()}`,
  );
}

// ── Resolve ───────────────────────────────────────────────────────────────

export function resolveReferences(
  treeId: string,
  req: ResolveReferencesRequest,
): Promise<ReferenceResolutionResult> {
  return apiPost<ReferenceResolutionResult>(
    `/trees/${treeId}/references/resolve`,
    req,
  );
}

// ── Inject with References ────────────────────────────────────────────────

export function injectWithReferences(
  treeId: string,
  req: InjectWithReferencesRequest,
): Promise<InjectWithReferencesResponse> {
  return apiPost<InjectWithReferencesResponse>(
    `/trees/${treeId}/references/inject`,
    req,
  );
}

// ── Multi-reference synthesis (SPEC-PL-06 §9.1, §9.2) ────────────────────
//
// The two-step write path behind a "synthesize from selected nodes" flow:
//
//   1. preflight  — POST /trees/{treeId}/reference-selections validates the
//      selection, quotes the token budget and returns a SIGNED, OPAQUE
//      `selection_token` (§9.1);
//   2. create     — POST /trees/{treeId}/multi-reference-replies spends that
//      token verbatim to write one multi_reference node + its N reference
//      edges (§9.2).
//
// Wire shapes here mirror the Go boundary structs exactly
// (internal/service/multi_reference_selection.go / multi_reference_reply.go,
// exercised by internal/handler/multi_reference_integration_test.go) —
// snake_case fields, Go's `null` for absent slices. The token is never
// parsed or stored client-side; it only travels preflight → create.

/** Request body for the §9.1 preflight. */
export interface ReferencePreflightRequest {
  source_node_ids: string[];
  /** Defaults server-side when omitted (8000 in the shipped wiring). */
  profile_context_budget?: number;
}

/** Per-source summary in the §9.1 preflight envelope. */
export interface ReferencePreflightSource {
  node_id: string;
  /** Canonical position label — R1, R2, … up to 20 sources. */
  source_label: string;
  /** Server-computed §7.2 palette key (`ref-0`…`ref-7`). */
  color_key: string;
  branch_root_id: string | null;
  content_hash: string;
  sequence_num: number;
  content_preview: string;
}

/** Token allocation the preflight quotes for the synthesis (§9.1). */
export interface ReferenceContextBudget {
  available_tokens: number;
  minimum_required_tokens: number;
  estimated_tokens: number;
  fits: boolean;
}

/** 200 envelope of the §9.1 preflight. */
export interface ReferencePreflightResponse {
  tree_id: string;
  /** Deduplicated selection in canonical (selection) order. */
  canonical_source_ids: string[];
  primary_source_id: string;
  is_synthetic_merge_point: boolean;
  /** Signed, opaque — hand it back to `createMultiReferenceReply` as-is. */
  selection_token: string;
  expires_at: string;
  context_budget: ReferenceContextBudget;
  sources: ReferencePreflightSource[];
}

/** Request body for the §9.2 creation. */
export interface CreateMultiReferenceReplyRequest {
  selection_token: string;
  content: string;
  /** Defaults to `markdown` server-side when omitted. */
  content_format?: string;
}

/** The created node in the §9.2 envelope (snake_case HTTP boundary). */
export interface ReferenceReplyNode {
  id: string;
  tree_id: string;
  /** Display anchor — the primary source, not a reply parent (§7.1). */
  parent_id: string | null;
  parent_mode: string;
  author_id: string;
  node_type: string;
  content: string;
  content_format: string;
  sequence_num: number;
  /** Reserved `multi_reference` metadata (§8.3). */
  metadata: Record<string, unknown> | null;
  created_at: string;
}

/** One created reference edge in the §9.2 envelope. */
export interface ReferenceReplyEdge {
  id: string;
  tree_id: string;
  source_node_id: string;
  target_node_id: string;
  edge_type: string;
  sequence_num: number;
  /** §5.2 labels: `source_label`, `color_key`, `role`, `reference_index`. */
  metadata: Record<string, unknown> | null;
  created_at: string;
}

/** 201 envelope of the §9.2 creation. */
export interface CreateMultiReferenceReplyResponse {
  node: ReferenceReplyNode;
  edges: ReferenceReplyEdge[];
  reference_context: {
    manifest_hash: string;
    source_count: number;
    is_synthetic_merge_point: boolean;
  };
}

/**
 * Step 1 — validate the selection and mint the signed selection token
 * (POST /trees/{treeId}/reference-selections, §9.1). Rejects below 2 and
 * above 20 sources, cross-tree/missing/deleted/duplicate sources, and
 * budgets that cannot fit (§9.4 error codes).
 */
export function referencePreflight(
  treeId: string,
  sourceNodeIds: string[],
  opts?: { profileContextBudget?: number },
): Promise<ReferencePreflightResponse> {
  const body: ReferencePreflightRequest = {
    source_node_ids: sourceNodeIds,
  };
  if (opts?.profileContextBudget !== undefined) {
    body.profile_context_budget = opts.profileContextBudget;
  }
  return apiPost<ReferencePreflightResponse>(
    `/trees/${treeId}/reference-selections`,
    body,
  );
}

/**
 * Step 2 — spend the preflight's token to create the multi-reference reply
 * (POST /trees/{treeId}/multi-reference-replies, §9.2). The token is
 * single-use and expires; a stale one answers REFERENCE_SELECTION_STALE.
 */
export function createMultiReferenceReply(
  treeId: string,
  req: CreateMultiReferenceReplyRequest,
): Promise<CreateMultiReferenceReplyResponse> {
  return apiPost<CreateMultiReferenceReplyResponse>(
    `/trees/${treeId}/multi-reference-replies`,
    req,
  );
}
