/**
 * Hermes Canopy — useAgents Hook (SPEC-023-UI-003)
 *
 * Fetches the agent roster list and, on demand, a single agent's detail
 * (including the trust timeline). Plain REST (no SSE — presence/trust
 * push is a later enhancement, out of scope per the spec).
 *
 * Backend contract (internal/handler/agent_handler.go):
 *   GET /api/v1/agents           → AgentListItem[]
 *   GET /api/v1/agents/{id}      → AgentDetail
 *
 * The hook is deliberately simple: it owns the roster list state and
 * exposes a `fetchDetail` callback for the detail view. The list is
 * fetched once on mount and can be refreshed via `refresh`.
 */

import { useState, useEffect, useCallback } from 'react';
import { apiGet } from '../lib/api.ts';
import type { AgentListItem, AgentDetail } from '../types/agents.ts';

export interface UseAgentsReturn {
  /** The roster list (empty until loaded). */
  agents: AgentListItem[];
  /** True while the initial roster fetch is in flight. */
  loading: boolean;
  /** Error message if the roster fetch failed (null otherwise). */
  error: string | null;
  /** Re-fetch the roster list. */
  refresh: () => void;
  /** Fetch a single agent's detail (with trust timeline). */
  fetchDetail: (id: string) => Promise<AgentDetail | null>;
}

export function useAgents(): UseAgentsReturn {
  const [agents, setAgents] = useState<AgentListItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const loadAgents = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await apiGet<AgentListItem[]>('/agents');
      setAgents(data ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load agents');
      setAgents([]);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadAgents();
  }, [loadAgents]);

  const refresh = useCallback(() => {
    void loadAgents();
  }, [loadAgents]);

  const fetchDetail = useCallback(
    async (id: string): Promise<AgentDetail | null> => {
      try {
        return await apiGet<AgentDetail>(`/agents/${encodeURIComponent(id)}`);
      } catch (err) {
        // Surface a thrown error so the caller can show an inline message,
        // but return null to keep the return type honest.
        throw err instanceof Error
          ? err
          : new Error('Failed to load agent detail');
      }
    },
    [],
  );

  return { agents, loading, error, refresh, fetchDetail };
}
