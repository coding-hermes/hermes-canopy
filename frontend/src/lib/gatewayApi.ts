/**
 * Hermes Canopy — Live Gateway API (GAP-050)
 *
 * Typed client for canopyd's /api/v1/gateway surface, which proxies the
 * LIVE Hermes gateway api_server (:8642) — the hermes-webui pattern.
 * canopyd holds the gateway API key; the browser only ever talks to
 * canopyd (same-origin, JWT-authed), so the Bearer secret never reaches
 * the frontend.
 */

import { apiGet, apiPost, apiUrl } from './api';
import {
  DEFAULT_CONTEXT_BUDGET,
  formatTokenUsage,
  type RawManifest,
} from './contextManifest.ts';

// ─── Types (mirror internal/gateway) ──────────────────────────────────

export interface GatewayRunEvent {
  event: string;
  run_id: string;
  timestamp?: number;
  delta?: string;
  output?: string;
  error?: string;
  text?: string;
  tool?: string;
  preview?: string;
  duration?: number;
  command?: string;
  choice?: string;
  resolved?: number;
  approval_id?: string;
  choices?: string[];
  usage?: Record<string, unknown>;
}

export interface GatewayRun {
  run_id: string;
  session_id: string;
  message: string;
  model: string;
  status: string;
  created_at: string;
  last_event?: string;
  output?: string;
  error?: string;
  usage?: Record<string, unknown>;
  events: GatewayRunEvent[];
  /*
   * Context provenance (GAP-075 backend carrier / GAP-084 frontend caller).
   * All four are additive and `omitempty` on the Go record, so a
   * context-free run is byte-identical to the pre-GAP-075 shape: every one
   * of these is ABSENT (not null, not 0) for a run started without
   * `node_id`. A consumer must treat absence as "not a context run".
   */
  source_node_id?: string;
  token_budget?: number;
  context_tokens?: number;
  manifest?: RawManifest | null;
}

export interface GatewayStatus {
  connected: boolean;
  base_url: string;
  error?: string;
  run_count: number;
  active_runs: number;
  recent_runs?: GatewayRun[];
}

export interface StartRunResponse {
  run_id: string;
  status: string;
}

// ─── API calls ────────────────────────────────────────────────────────

export function getGatewayStatus(): Promise<GatewayStatus> {
  return apiGet<GatewayStatus>('/gateway/status');
}

export function listGatewayRuns(): Promise<{ runs: GatewayRun[] }> {
  return apiGet<{ runs: GatewayRun[] }>('/gateway/runs');
}

/**
 * Start a gateway run.
 *
 * `nodeId` (GAP-084) switches the run onto the context-aware path: canopyd
 * compiles that node's budgeted context, sends the COMPILED context to the
 * gateway instead of the raw message, and attaches the compiler manifest to
 * the run record. The keys are additive and conditional — a context-free
 * call sends exactly the body it sent before this parameter existed.
 */
export function startGatewayRun(
  message: string,
  sessionId?: string,
  nodeId?: string,
  tokenBudget?: number,
): Promise<StartRunResponse> {
  return apiPost<StartRunResponse>('/gateway/runs', {
    message,
    ...(sessionId ? { session_id: sessionId } : {}),
    ...(typeof nodeId === 'string' && nodeId.length > 0 ? { node_id: nodeId } : {}),
    ...(typeof tokenBudget === 'number' &&
    Number.isFinite(tokenBudget) &&
    tokenBudget > 0
      ? { token_budget: tokenBudget }
      : {}),
  });
}

// ─── Run context provenance (GAP-084) ─────────────────────────────────

/**
 * The one-line provenance label for a run's compiled context, or `null`
 * when the run carried none.
 *
 * A context-free run has NEITHER field (the Go record is `omitempty`), so
 * "no context" and "a context of zero tokens" arrive the same way: absent.
 * Both readings mean the same thing to a user — nothing to show — so the
 * label is suppressed rather than rendered as `0 / 8,000 tokens`, which
 * would imply a compile happened and produced nothing.
 *
 * The two halves are judged INDEPENDENTLY: a run that recorded a budget but
 * no measured usage (a degraded compile) still proves a compile was
 * requested, so it renders, with the absent half filled by the same
 * defaults the manifest panel uses.
 */
export function runContextWindowLine(
  run: Pick<GatewayRun, 'context_tokens' | 'token_budget'>,
): string | null {
  const hasUsage =
    typeof run.context_tokens === 'number' &&
    Number.isFinite(run.context_tokens) &&
    run.context_tokens > 0;
  const hasBudget =
    typeof run.token_budget === 'number' &&
    Number.isFinite(run.token_budget) &&
    run.token_budget > 0;

  if (!hasUsage && !hasBudget) return null;

  return `Context window: ${formatTokenUsage(
    hasUsage ? (run.context_tokens ?? 0) : 0,
    hasBudget ? (run.token_budget ?? DEFAULT_CONTEXT_BUDGET) : DEFAULT_CONTEXT_BUDGET,
  )}`;
}

export function stopGatewayRun(runId: string): Promise<{ run_id: string; status: string }> {
  return apiPost<{ run_id: string; status: string }>(`/gateway/runs/${encodeURIComponent(runId)}/stop`);
}

export function respondGatewayApproval(
  runId: string,
  choice: 'once' | 'session' | 'always' | 'deny',
  approvalId?: string,
): Promise<{ run_id: string; choice: string; resolved: boolean }> {
  return apiPost(`/gateway/runs/${encodeURIComponent(runId)}/approval`, {
    choice,
    ...(approvalId ? { approval_id: approvalId } : {}),
  });
}

// ─── SSE stream URL ───────────────────────────────────────────────────

/**
 * SSE URL for a run's event feed. Built through `apiUrl()` so the base
 * honours `VITE_API_BASE_URL` — a hardcoded base would pin the feed to the
 * default and break a non-default deployment.
 */
export function gatewayRunEventsUrl(runId: string): string {
  return apiUrl(`/gateway/runs/${encodeURIComponent(runId)}/events`);
}
