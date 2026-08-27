/**
 * Unit tests — gatewayApi lib (GAP-050)
 *
 * Verifies the typed client for canopyd's /api/v1/gateway surface: URL
 * construction, request bodies, and error surfacing.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import {
  getGatewayStatus,
  listGatewayRuns,
  startGatewayRun,
  stopGatewayRun,
  respondGatewayApproval,
  gatewayRunEventsUrl,
} from '../gatewayApi';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('gatewayApi', () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('getGatewayStatus GETs /api/v1/gateway/status', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ connected: true, base_url: 'http://127.0.0.1:8642', run_count: 2, active_runs: 1 }),
    );
    const status = await getGatewayStatus();
    expect(status.connected).toBe(true);
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/gateway/status');
  });

  it('listGatewayRuns GETs /gateway/runs', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ runs: [{ run_id: 'run_1', status: 'running' }] }));
    const { runs } = await listGatewayRuns();
    expect(runs).toHaveLength(1);
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/gateway/runs');
  });

  it('startGatewayRun POSTs message + session_id', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ run_id: 'run_9', status: 'started' }));
    const resp = await startGatewayRun('hello hermes', 'sess-42');
    expect(resp.run_id).toBe('run_9');
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/v1/gateway/runs');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body)).toEqual({ message: 'hello hermes', session_id: 'sess-42' });
  });

  it('startGatewayRun omits session_id when absent', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ run_id: 'run_9', status: 'started' }));
    await startGatewayRun('hi');
    const [, init] = fetchMock.mock.calls[0];
    expect(JSON.parse(init.body)).toEqual({ message: 'hi' });
  });

  it('stopGatewayRun POSTs to the run stop endpoint', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ run_id: 'run_1', status: 'stopping' }));
    const resp = await stopGatewayRun('run_1');
    expect(resp.status).toBe('stopping');
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/gateway/runs/run_1/stop');
  });

  it('respondGatewayApproval POSTs choice + approval_id', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ run_id: 'run_1', choice: 'once', resolved: true }));
    const resp = await respondGatewayApproval('run_1', 'once', 'appr-7');
    expect(resp.resolved).toBe(true);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/v1/gateway/runs/run_1/approval');
    expect(JSON.parse(init.body)).toEqual({ choice: 'once', approval_id: 'appr-7' });
  });

  it('surfaces HTTP errors with the API error message', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ error: { message: 'gateway down' } }, 502),
    );
    await expect(getGatewayStatus()).rejects.toThrow('gateway down');
  });

  it('gatewayRunEventsUrl builds the SSE URL', () => {
    expect(gatewayRunEventsUrl('run_1')).toBe('/api/v1/gateway/runs/run_1/events');
    expect(gatewayRunEventsUrl('run/1')).toBe('/api/v1/gateway/runs/run%2F1/events');
  });
});
