/**
 * Hermes Canopy — Topic types (shared)
 *
 * Extracted from `pages/TopicsPage.tsx` (UI-02) so the persistent topics
 * rail and the topics page describe the same API payloads instead of
 * carrying divergent copies of the shape.
 */

/** A topic as returned by `GET /api/v1/topics?tree_id=…`. */
export interface TopicSummary {
  id: string;
  tree_id: string;
  root_node_id: string;
  title: string;
  description: string;
  slug: string;
  status: string;
  /** Nodes contained in the topic subgraph. May be absent on older payloads. */
  node_count: number;
  created_at: string;
}

/** Minimal tree shape used by topic-scoping selectors. */
export interface TreeSummary {
  id: string;
  title: string;
}
