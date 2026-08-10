/**
 * Hermes Canopy — Reference Resolution types (TM-04)
 *
 * Spec: SPEC-TM-04 §5 TypeScript Types & Zod Validation.
 * Backend endpoints: /trees/{tree_id}/references/autocomplete|resolve|inject.
 */

// ── Parsed / Resolved References (spec §5.1) ──────────────────────────────

export interface ParsedReference {
  raw: string; // Full match: "#topic-slug"
  slug: string; // Extracted slug: "topic-slug"
  offset: number; // Character offset in message
  length: number; // Length of matched text
}

export interface TopicSummary {
  id: string;
  treeId: string;
  title: string;
  slug: string;
  description: string;
  status: 'active' | 'archived' | 'deleted';
  nodeCount: number;
  topicTags: string[];
  createdAt: string;
  archivedAt?: string;
}

export interface ResolvedReference {
  reference: ParsedReference;
  topic: TopicSummary;
}

export interface ReferenceResolutionResult {
  node_id: string;
  tree_id: string;
  references: ResolvedReference[];
  not_found?: ParsedReference[];
  too_many: boolean;
  warning?: string;
  total_nodes_in_scope: number;
}

// ── Autocomplete (spec §5.1) ──────────────────────────────────────────────

export type ReferenceMatchType = 'prefix' | 'contains';

export interface ReferenceAutocompleteResult {
  slug: string;
  title: string;
  match_type: ReferenceMatchType;
  status: 'active' | 'archived' | 'deleted';
  node_count: number;
}

export interface AutocompleteResponse {
  results: ReferenceAutocompleteResult[];
}

// ── API Requests (spec §6) ────────────────────────────────────────────────

export interface ResolveReferencesRequest {
  content: string;
  max_nodes?: number;
  with_context?: boolean;
}

export interface InjectWithReferencesRequest {
  topic_ids?: string[];
  references?: string[];
  max_nodes?: number;
}

// ── Composer State ────────────────────────────────────────────────────────

export interface ReferenceAutocompleteState {
  open: boolean;
  query: string;
  results: ReferenceAutocompleteResult[];
  selectedIndex: number;
  triggerOffset: number;
}
