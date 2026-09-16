/**
 * Hermes Canopy — Reference provenance fetch (SPEC-PL-06 §7.3, §9.3)
 *
 * `GET /api/v1/nodes/{node_id}/reference-context` is the provenance read
 * behind the inspector's "View context" action: the sources that were
 * compiled into the reply, their truncation state, the token allocation and
 * the manifest hash that was recorded at creation.
 *
 * It is fetched ON DEMAND — the panel already knows the canonical order,
 * labels, colours and branch span from the node's own metadata (§8.3), so
 * the round trip only buys the truncation flags and the live hash check.
 * Clicking "View context" is what asks for it.
 *
 * Request lifecycle mirrors `useContextManifest`: a monotonic generation
 * guards against a stale response landing on a new selection, a missing /
 * non-UUID id issues no request at all, and every failure resolves to a
 * message rather than a thrown render.
 */

import { useCallback, useEffect, useRef, useState } from 'react';
import { apiGet } from '../lib/api.ts';
import { isCompilableNodeId } from '../lib/contextManifest.ts';
import {
  normaliseReferenceContext,
  referenceContextRequestPath,
  type RawReferenceContext,
  type ReferenceContext,
} from '../lib/multiReference.ts';

export interface UseReferenceContextResult {
  context: ReferenceContext | null;
  loading: boolean;
  /** Server-supplied message, already unwrapped by `apiGet`. */
  error: string | null;
  /** Ask for the §9.3 read (idempotent while a request is in flight). */
  request: () => void;
  /** Forget the fetched context (selection changed, panel closed). */
  reset: () => void;
}

export function useReferenceContext(nodeId: string | null): UseReferenceContextResult {
  const [context, setContext] = useState<ReferenceContext | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [requested, setRequested] = useState(false);

  /** Monotonic id — compared on arrival, not relied on via abort. */
  const generation = useRef(0);

  // A new node invalidates the previous node's provenance outright: showing
  // R1…RN of the node before it would be a lie the user cannot detect.
  useEffect(() => {
    generation.current += 1;
    setContext(null);
    setError(null);
    setRequested(false);
    setLoading(false);
  }, [nodeId]);

  const request = useCallback(() => {
    setRequested(true);
  }, []);

  const reset = useCallback(() => {
    generation.current += 1;
    setContext(null);
    setError(null);
    setRequested(false);
    setLoading(false);
  }, []);

  useEffect(() => {
    if (!requested || !isCompilableNodeId(nodeId)) return;

    const current = ++generation.current;
    setLoading(true);
    setError(null);

    void (async () => {
      try {
        const body = await apiGet<RawReferenceContext>(
          referenceContextRequestPath(nodeId as string),
        );
        if (generation.current !== current) return; // stale
        setContext(normaliseReferenceContext(body));
        setError(null);
      } catch (err) {
        if (generation.current !== current) return; // stale
        setContext(null);
        setError(err instanceof Error ? err.message : 'Reference context unavailable');
      } finally {
        if (generation.current === current) setLoading(false);
      }
    })();
  }, [requested, nodeId]);

  return { context, loading, error, request, reset };
}

export default useReferenceContext;
