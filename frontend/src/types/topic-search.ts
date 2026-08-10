/**
 * Hermes Canopy — Topic Search types (TM-03)
 *
 * Spec: SPEC-TM-03 §5 TypeScript Types & Zod Validation.
 * Backend endpoints: /trees/{tree_id}/topics/search|recent|preview,
 *                    /trees/{tree_id}/context/inject.
 */

// ── Search Types (spec §5.1) ─────────────────────────────────────────────

export interface TopicSearchResult {
  topic_id: string;
  tree_id: string;
  title: string;
  slug: string;
  snippet: string; // ts_headline with <mark>highlighted</mark> matches
  status: 'active' | 'archived' | 'deleted';
  node_count: number;
  last_active_at: string; // ISO 8601
  relevance: number;
}

export interface SearchResponse {
  results: TopicSearchResult[];
  total: number;
  query_time_ms: number;
}

export interface RecentTopicsResponse {
  topics: TopicSearchResult[];
}

// ── Topic Preview (Hover) ────────────────────────────────────────────────

export interface TopicPreview {
  topic_id: string;
  title: string;
  snippets: string[];
  participant_count: number;
  node_count: number;
  last_active_at: string;
  last_active_rel: string;
}

// ── Context Injection ────────────────────────────────────────────────────

export interface InjectContextRequest {
  topic_ids: string[];
  max_nodes?: number;
}

export interface TopicContextNode {
  id: string;
  tree_id: string;
  author_id: string;
  content: string;
  created_at: string;
}

export interface TopicContext {
  topic_id: string;
  title: string;
  slug: string;
  root_node_id: string;
  nodes: TopicContextNode[];
  total_nodes: number;
  has_more: boolean;
  context_hash: string;
}

export interface MultiTopicContext {
  topics: TopicContext[];
  merged_text: string;
  total_nodes: number;
  truncated: boolean;
}

export interface ContextInjectResponse {
  context: MultiTopicContext;
  event_id: string;
}
