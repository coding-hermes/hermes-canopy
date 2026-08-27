/**
 * Hermes Canopy — Live Gateway API (GAP-050)
 *
 * Typed client for canopyd's /api/v1/gateway surface, which proxies the
 * LIVE Hermes gateway api_server (:8642) — the hermes-webui pattern.
 * canopyd holds the gateway API key; the browser only ever talks to
 * canopyd (same-origin, JWT-authed), so the Bearer secret never reaches
 * the frontend.
 */

import { apiGet, apiPost } from './api';

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

export function startGatewayRun(
  message: string,
  sessionId?: string,
): Promise<StartRunResponse> {
  return apiPost<StartRunResponse>('/gateway/runs', {
    message,
    ...(sessionId ? { session_id: sessionId } : {}),
  });
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

export function gatewayRunEventsUrl(runId: string): string {
  return `/api/v1/gateway/runs/${encodeURIComponent(runId)}/events`;
}
