/**
 * Hermes Canopy — Tree related-associations fetch (UI-REL-001)
 *
 * Owns the request lifecycle for `GET /api/v1/trees/{id}` so the Related
 * panel component stays a pure renderer. The endpoint returns the full
 * TreeDetail; this hook extracts the additive `related` object (WIRE-006)
 * and reports whether the tree carries any associations at all.
 *
 * Same three guarantees as `useContextManifest`:
 *
 *   stale responses   the tree selector changes faster than the backend
 *                     answers. Every response is checked against the
 *                     request generation that is current WHEN IT LANDS —
 *                     an out-of-order reply is dropped, never rendered
 *                     against the wrong tree.
 *   abort on change   the in-flight request is aborted when the selection
 *                     moves or the view unmounts, so a rapid click-through
 *                     of the tree list doesn't queue detail fetches.
 *   never crash       a 404 (TREE_NOT_FOUND) or 503 (SERVICE_UNAVAILABLE)
 *                     resolves to a note, not a thrown render. The node
 *                     list is the page; the panel is an inspector on it.
 *
 * The dependency is the tree id VALUE, not an object — an effect keyed on
 * a fresh object identity re-runs every render, which on a fetch means an
 * unbounded request loop (the UI-02 crash shape).
 */

import { useEffect, useRef, useState } from 'react';
import { apiUrl } from '../lib/api.ts';
import type { TreeDetail, TreeRelated } from '../types/tree.ts';

// ─── Result ────────────────────────────────────────────────────────────

export interface UseTreeRelatedResult {
  /** The `related` object, or `null` when the tree has no associations. */
  related: TreeRelated | null;
  loading: boolean;
  /** Server-supplied message, already thrown by the fetch. */
  error: string | null;
}

// ─── Fetch ─────────────────────────────────────────────────────────────

/**
 * Read the related-associations object for a tree.
 *
 * Uses `fetch` directly rather than `apiGet` because this is an abortable
 * call — `apiGet` has no signal parameter and widening its contract for a
 * single consumer is out of scope for UI-REL-001. Error extraction
 * matches `apiGet`'s: the backend returns `{"error":{"code","message"}}`
 * and the message is what a human reads (BUG — `[object Object]` when the
 * object is passed to `new Error`).
 */
async function fetchTreeDetail(
  treeId: string,
  signal: AbortSignal,
): Promise<TreeDetail> {
  const res = await fetch(apiUrl(`/trees/${treeId}`), { signal });

  if (!res.ok) {
    const body = await res.text();
    let msg: string;
    try {
      const parsed = JSON.parse(body) as { error?: unknown };
      const e = parsed.error;
      msg =
        (typeof e === 'object' && e !== null
          ? (e as { message?: string }).message
          : (e as string | undefined)) ?? body;
    } catch {
      msg = body || `HTTP ${res.status}`;
    }
    throw new Error(msg);
  }

  return (await res.json()) as TreeDetail;
}

// ─── Hook ──────────────────────────────────────────────────────────────

/**
 * Related-associations for the selected tree.
 *
 * Passing `null` (nothing selected) clears the panel without issuing a
 * request. A tree whose detail carries no `related` key (an ordinary tree,
 * not imported from a Hermes session) resolves to `related: null` — the
 * panel renders its compact empty state rather than an error.
 */
export function useTreeRelated(treeId: string | null): UseTreeRelatedResult {
  const [related, setRelated] = useState<TreeRelated | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  /*
   * Monotonic request id. Compared on arrival rather than relying on the
   * abort alone: an abort raced by an already-resolved body still runs its
   * `.then`, and that body belongs to the PREVIOUS tree.
   */
  const generation = useRef(0);

  useEffect(() => {
    const current = ++generation.current;

    if (!treeId) {
      setRelated(null);
      setLoading(false);
      setError(null);
      return;
    }

    const controller = new AbortController();
    setLoading(true);
    setError(null);

    void (async () => {
      try {
        const detail = await fetchTreeDetail(treeId, controller.signal);
        if (generation.current !== current) return; // stale
        setRelated(detail.related ?? null);
        setError(null);
      } catch (err) {
        if (generation.current !== current) return; // stale
        if (err instanceof DOMException && err.name === 'AbortError') return;
        setRelated(null);
        setError(err instanceof Error ? err.message : 'Related unavailable');
      } finally {
        if (generation.current === current) setLoading(false);
      }
    })();

    return () => {
      controller.abort();
    };
  }, [treeId]);

  return { related, loading, error };
}

export default useTreeRelated;
