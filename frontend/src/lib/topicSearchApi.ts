/**
 * Hermes Canopy — Topic Search API client (TM-03)
 *
 * Spec: SPEC-TM-03 §6 API Endpoints.
 * Uses the shared apiGet/apiPost helpers from lib/api.ts.
 */

import { apiGet, apiPost } from './api';
import type {
  SearchResponse,
  RecentTopicsResponse,
  TopicPreview,
  InjectContextRequest,
  ContextInjectResponse,
} from '../types/topic-search';

// ── Search ────────────────────────────────────────────────────────────────

export function searchTopics(
  treeId: string,
  query: string,
  opts?: {
    status?: string;
    limit?: number;
    offset?: number;
    sort?: string;
  },
): Promise<SearchResponse> {
  const params = new URLSearchParams({ q: query });
  if (opts?.status) params.set('status', opts.status);
  if (opts?.limit) params.set('limit', String(opts.limit));
  if (opts?.offset) params.set('offset', String(opts.offset));
  if (opts?.sort) params.set('sort', opts.sort);
  return apiGet<SearchResponse>(
    `/trees/${treeId}/topics/search?${params.toString()}`,
  );
}

// ── Recent Topics ────────────────────────────────────────────────────────

export function getRecentTopics(
  treeId: string,
  limit?: number,
): Promise<RecentTopicsResponse> {
  const params = limit ? `?limit=${limit}` : '';
  return apiGet<RecentTopicsResponse>(
    `/trees/${treeId}/topics/recent${params}`,
  );
}

// ── Topic Preview ─────────────────────────────────────────────────────────

export function getTopicPreview(
  treeId: string,
  topicId: string,
): Promise<TopicPreview> {
  return apiGet<TopicPreview>(
    `/trees/${treeId}/topics/${topicId}/preview`,
  );
}

// ── Context Injection ────────────────────────────────────────────────────

export function injectContext(
  treeId: string,
  req: InjectContextRequest,
): Promise<ContextInjectResponse> {
  return apiPost<ContextInjectResponse>(
    `/trees/${treeId}/context/inject`,
    req,
  );
}
