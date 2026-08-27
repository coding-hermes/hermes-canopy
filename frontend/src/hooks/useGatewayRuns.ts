/**
 * Hermes Canopy — useGatewayRuns hook (GAP-050)
 *
 * Live view of the Hermes gateway through canopyd:
 *   - polls /gateway/runs + /gateway/status for the run registry
 *   - starts/stops runs and responds to approvals
 *   - useRunEventStream() opens the run's SSE feed (history replay + live
 *     fan-out) and dedupes replayed events by (event, timestamp, content).
 */

import { useState, useEffect, useRef, useCallback } from 'react';
import {
  listGatewayRuns,
  getGatewayStatus,
  startGatewayRun,
  stopGatewayRun,
  respondGatewayApproval,
  gatewayRunEventsUrl,
  type GatewayRun,
  type GatewayStatus,
  type GatewayRunEvent,
} from '../lib/gatewayApi';

export type { GatewayRun, GatewayRunEvent } from '../lib/gatewayApi';

export type FeedStatus = 'connecting' | 'open' | 'error' | 'closed';

const POLL_MS = 4000;

export interface UseGatewayRunsReturn {
  status: GatewayStatus | null;
  runs: GatewayRun[];
  loading: boolean;
  error: string | null;
  refresh: () => Promise<void>;
  startRun: (message: string, sessionId?: string) => Promise<string>;
  stopRun: (runId: string) => Promise<void>;
  respondApproval: (
    runId: string,
    choice: 'once' | 'session' | 'always' | 'deny',
    approvalId?: string,
  ) => Promise<void>;
}

export function useGatewayRuns(): UseGatewayRunsReturn {
  const [status, setStatus] = useState<GatewayStatus | null>(null);
  const [runs, setRuns] = useState<GatewayRun[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      const [statusData, runsData] = await Promise.all([
        getGatewayStatus(),
        listGatewayRuns(),
      ]);
      setStatus(statusData);
      setRuns(runsData.runs ?? []);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
    const timer = setInterval(() => void refresh(), POLL_MS);
    return () => clearInterval(timer);
  }, [refresh]);

  const startRun = useCallback(async (message: string, sessionId?: string) => {
    const resp = await startGatewayRun(message, sessionId);
    await refresh();
    return resp.run_id;
  }, [refresh]);

  const stopRun = useCallback(
    async (runId: string) => {
      await stopGatewayRun(runId);
      await refresh();
    },
    [refresh],
  );

  const respondApproval = useCallback(
    async (runId: string, choice: 'once' | 'session' | 'always' | 'deny', approvalId?: string) => {
      await respondGatewayApproval(runId, choice, approvalId);
      await refresh();
    },
    [refresh],
  );

  return { status, runs, loading, error, refresh, startRun, stopRun, respondApproval };
}

// ─── Per-run SSE event stream ─────────────────────────────────────────

export interface UseRunEventStreamReturn {
  events: GatewayRunEvent[];
  status: FeedStatus;
  /** Concatenated message.delta text (grouped transcript of the stream). */
  transcript: string;
}

/** Dedupe key for replayed events (an event can arrive in replay AND live). */
function eventKey(ev: GatewayRunEvent): string {
  const content = ev.delta ?? ev.output ?? ev.error ?? ev.text ?? ev.tool ?? ev.command ?? ev.choice ?? '';
  return `${ev.event}:${ev.timestamp ?? 0}:${content}`;
}

export function useRunEventStream(runId: string | null): UseRunEventStreamReturn {
  const [events, setEvents] = useState<GatewayRunEvent[]>([]);
  const [status, setStatus] = useState<FeedStatus>('connecting');
  const terminalSeen = useRef(false);

  useEffect(() => {
    setEvents([]);
    setStatus('connecting');
    terminalSeen.current = false;
    if (!runId) return;

    const src = new EventSource(gatewayRunEventsUrl(runId));
    src.onopen = () => setStatus('open');
    src.onerror = () => {
      // A terminal run's stream closes by design (the gateway closes the
      // SSE at run end; canopyd mirrors that) — that's not an error.
      if (terminalSeen.current) {
        setStatus('closed');
      } else {
        setStatus('error');
      }
    };

    src.onmessage = (e: MessageEvent) => {
      let ev: GatewayRunEvent;
      try {
        ev = JSON.parse(e.data as string) as GatewayRunEvent;
      } catch {
        return;
      }
      if (!ev?.event) return;
      if (['run.completed', 'run.failed', 'run.cancelled'].includes(ev.event)) {
        terminalSeen.current = true;
      }
      const key = eventKey(ev);
      setEvents((prev) => {
        if (prev.some((p) => eventKey(p) === key)) return prev;
        return [...prev, ev];
      });
    };

    return () => {
      src.close();
    };
  }, [runId]);

  const transcript = events
    .filter((ev) => ev.event === 'message.delta' && ev.delta)
    .map((ev) => ev.delta ?? '')
    .join('');

  return { events, status, transcript };
}
