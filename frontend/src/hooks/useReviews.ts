/**
 * Hermes Canopy — useReviews Hook (SPEC-023-UI-004)
 *
 * Fetches the PR review list and, on demand, a single review's detail
 * (including blast radius + Chimera verdict). Also exposes a `trigger`
 * callback to run a simulated Chimera review via POST.
 *
 * Backend contract (internal/handler/review_handler.go):
 *   GET  /api/v1/reviews           → ReviewListItem[]
 *   GET  /api/v1/reviews/{id}      → ReviewDetail
 *   POST /api/v1/reviews/{pr}/trigger → ReviewDetail
 *
 * The hook owns the review list state and exposes `fetchDetail` /
 * `trigger` callbacks. The list is fetched once on mount and can be
 * refreshed via `refresh`.
 */

import { useState, useEffect, useCallback } from 'react';
import { apiGet, apiPost } from '../lib/api.ts';
import type { ReviewListItem, ReviewDetail } from '../types/review.ts';

export interface UseReviewsReturn {
  /** The review list (empty until loaded). */
  reviews: ReviewListItem[];
  /** True while the initial list fetch is in flight. */
  loading: boolean;
  /** Error message if the list fetch failed (null otherwise). */
  error: string | null;
  /** Re-fetch the review list. */
  refresh: () => void;
  /** Fetch a single review's detail (with blast radius + verdict). */
  fetchDetail: (id: string) => Promise<ReviewDetail | null>;
  /** Trigger a simulated Chimera review for a PR. */
  trigger: (pr: string) => Promise<ReviewDetail | null>;
}

export function useReviews(): UseReviewsReturn {
  const [reviews, setReviews] = useState<ReviewListItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const loadReviews = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await apiGet<ReviewListItem[]>('/reviews');
      setReviews(data ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load reviews');
      setReviews([]);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadReviews();
  }, [loadReviews]);

  const refresh = useCallback(() => {
    void loadReviews();
  }, [loadReviews]);

  const fetchDetail = useCallback(
    async (id: string): Promise<ReviewDetail | null> => {
      try {
        return await apiGet<ReviewDetail>(
          `/reviews/${encodeURIComponent(id)}`,
        );
      } catch (err) {
        throw err instanceof Error
          ? err
          : new Error('Failed to load review detail');
      }
    },
    [],
  );

  const trigger = useCallback(
    async (pr: string): Promise<ReviewDetail | null> => {
      try {
        return await apiPost<ReviewDetail>(
          `/reviews/${encodeURIComponent(pr)}/trigger`,
        );
      } catch (err) {
        throw err instanceof Error
          ? err
          : new Error('Failed to trigger review');
      }
    },
    [],
  );

  return { reviews, loading, error, refresh, fetchDetail, trigger };
}
