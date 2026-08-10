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
