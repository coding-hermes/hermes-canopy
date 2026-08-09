/**
 * Hermes Canopy — Context manifest fetch (WIRE-002)
 *
 * Owns the request lifecycle for `GET /api/v1/context/{node_id}` so the
 * panel component stays a pure renderer.
 *
 * Three things this has to get right:
 *
 *   stale responses   selection changes faster than the compiler answers
 *                     (it walks ancestry and resolves references against
 *                     Postgres). Every response is checked against the
 *                     request generation that is current WHEN IT LANDS —
 *                     an out-of-order reply is dropped, never rendered
 *                     against the wrong node.
 *   abort on change   the in-flight request is aborted when the selection
 *                     moves or the view unmounts, so a rapid click-through
 *                     of a large tree doesn't queue compiles server-side.
 *   never crash       a 404 (NODE_NOT_FOUND) or 503 (SERVICE_UNAVAILABLE)
 *                     resolves to a note, not a thrown render. The tree
 *                     canvas is the page; the panel is an inspector on it.
 *
 * The dependency is the node ID VALUE, not an object — an effect keyed on
 * a fresh object identity re-runs every render, which on a fetch means an
 * unbounded request loop (the UI-02 crash shape).
 */

import { useEffect, useRef, useState } from 'react';
import { apiUrl } from '../lib/api.ts';
import {
  DEFAULT_CONTEXT_BUDGET,
  contextRequestPath,
  isCompilableNodeId,
  normaliseManifest,
  type CompiledContext,
  type Manifest,
} from '../lib/contextManifest.ts';

// ─── Result ────────────────────────────────────────────────────────────

export interface UseContextManifestResult {
  manifest: Manifest | null;
  loading: boolean;
  /** Server-supplied message, already thrown by the fetch. */
  error: string | null;
}

// ─── Fetch ─────────────────────────────────────────────────────────────

/**
 * Read the compiled-context manifest for a node.
 *
 * Uses `fetch` directly rather than `apiGet` because this is the one call
 * in the app that must be abortable — `apiGet` has no signal parameter and
 * widening its contract for a single consumer is out of scope for WIRE-002.
 * Error extraction matches `apiGet`'s: the backend returns
 * `{"error":{"code","message"}}` and the message is what a human reads
 * (BUG — `[object Object]` when the object is passed to `new Error`).
 */
async function fetchManifest(
  nodeId: string,
  budget: number,
  signal: AbortSignal,
): Promise<Manifest | null> {
  const res = await fetch(apiUrl(contextRequestPath(nodeId, budget)), {
    signal,
  });

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

  const body = (await res.json()) as CompiledContext;
  return normaliseManifest(body);
}

// ─── Hook ──────────────────────────────────────────────────────────────

/**
 * Compiled-context manifest for the selected node.
 *
 * Passing `null` (nothing selected) clears the panel without issuing a
 * request. Ids the compiler cannot parse — a locally-seeded demo node, a
 * ghost slot — are treated the same way rather than spending a guaranteed
 * 400 on them.
 */
export function useContextManifest(
  nodeId: string | null,
  budget: number = DEFAULT_CONTEXT_BUDGET,
): UseContextManifestResult {
  const [manifest, setManifest] = useState<Manifest | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  /*
   * Monotonic request id. Compared on arrival rather than relying on the
   * abort alone: an abort raced by an already-resolved body still runs its
   * `.then`, and that body belongs to the PREVIOUS node.
   */
  const generation = useRef(0);

  useEffect(() => {
    const current = ++generation.current;

    if (!isCompilableNodeId(nodeId)) {
      setManifest(null);
      setLoading(false);
      setError(null);
      return;
    }

    const controller = new AbortController();
    setLoading(true);
    setError(null);

    void (async () => {
      try {
        const next = await fetchManifest(nodeId as string, budget, controller.signal);
        if (generation.current !== current) return; // stale
        setManifest(next);
        setError(null);
      } catch (err) {
        if (generation.current !== current) return; // stale
        if (err instanceof DOMException && err.name === 'AbortError') return;
        setManifest(null);
        setError(err instanceof Error ? err.message : 'Context unavailable');
      } finally {
        if (generation.current === current) setLoading(false);
      }
    })();

    return () => {
      controller.abort();
    };
  }, [nodeId, budget]);

  return { manifest, loading, error };
}

export default useContextManifest;
