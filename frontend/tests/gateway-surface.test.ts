/**
 * Integration tests — Gateway surface probe (GAP-052)
 *
 * The board once declared GAP-050 complete and E2E passed 61/61 while the
 * DEPLOYED canopyd binary 404'd the entire /api/v1/gateway surface for ~7h —
 * the E2E battery had zero gateway coverage, so a stale binary looked green.
 *
 * These probes go RED in exactly that failure class: a 404 (or non-JSON body)
 * on the gateway surface fails, regardless of upstream gateway health.
 *
 * Contract (internal/handler/gateway_handler.go + gateway_handler_test.go):
 *   GET /api/v1/gateway/status → ALWAYS 200 with
 *     {connected: bool, base_url: string, error?: string,
 *      run_count: number, active_runs: number, recent_runs?: [...]}
 *     — 200 with connected=false when the upstream Hermes gateway
 *       (:8642) is unreachable or unconfigured.
 *   GET /api/v1/gateway/runs   → ALWAYS 200 with {runs: [...]}
 *
 * So a healthy response is 200 + JSON + those keys in BOTH configurations:
 *   (a) live systemd canopyd with gateway env set (current stack), and
 *   (b) a battery-started canopyd with default config and no gateway env.
 * A 404 means the route is absent (stale/pre-GAP-050 binary) → FAIL.
 *
 * All probes are READ-ONLY (GET only — never POST /runs; the Hermes gateway
 * is a live production system).
 *
 * Auth: requests go through the Vite dev proxy (:5173), which injects the dev
 * JWT automatically on ALL /api traffic (there is no unauthenticated path
 * through the proxy). The first test hits the backend (:8091) directly to
 * prove the surface is auth-protected (401 without a token), then asserts the
 * proxy-authenticated requests are 200 — if the proxy ever stops injecting
 * auth, the suite fails loudly instead of passing vacuously.
 */
import { describe, it, expect, beforeAll, afterAll } from 'vitest';
import {
  BASE_URL,
  createTestContext,
  destroyContext,
  isServerRunning,
  type TestContext,
} from './setup';

const GATEWAY_API = `${BASE_URL}/api/v1/gateway`;
// Direct backend URL — bypasses the vite proxy (and its injected JWT).
const BACKEND_GATEWAY_STATUS = 'http://localhost:8091/api/v1/gateway/status';

describe('Gateway surface probe (GAP-052)', () => {
  let ctx: TestContext;
  let serverAvailable: boolean;

  beforeAll(async () => {
    serverAvailable = await isServerRunning();
    if (serverAvailable) {
      ctx = await createTestContext();
    }
  }, 20_000);

  afterAll(async () => {
    if (serverAvailable) {
      await destroyContext(ctx);
    }
  });

  it('gateway/status is PRESENT via the authenticated vite proxy', async () => {
    if (!serverAvailable) {
      console.warn('⚠ Dev server not running — skipping integration test');
      return;
    }

    // Prove the surface is auth-protected: a direct request to the backend
    // (no vite proxy → no injected JWT) must be rejected as 401.
    const anon = await ctx.page.request.get(BACKEND_GATEWAY_STATUS);
    expect(anon.status()).toBe(401);

    const resp = await ctx.page.request.get(`${GATEWAY_API}/status`);

    // THE assertion this probe exists for: a 404 here is the 7h-outage class
    // (surface absent from the deployed binary).
    expect(resp.status()).not.toBe(404);
    expect(resp.status()).toBe(200);

    const body = await resp.json();
    expect(typeof body).toBe('object');
    expect(body).toHaveProperty('connected');
    expect(typeof body.connected).toBe('boolean');
    expect(typeof body.base_url).toBe('string');
    expect(typeof body.run_count).toBe('number');
    expect(typeof body.active_runs).toBe('number');
    // connected=false must come with an error string (handler contract when
    // the upstream gateway is down/unconfigured).
    if (body.connected === false) {
      expect(typeof body.error).toBe('string');
      expect(body.error.length).toBeGreaterThan(0);
    }
  });

  it('gateway/runs is PRESENT via the authenticated vite proxy', async () => {
    if (!serverAvailable) {
      console.warn('⚠ Dev server not running — skipping integration test');
      return;
    }

    const resp = await ctx.page.request.get(`${GATEWAY_API}/runs`);

    expect(resp.status()).not.toBe(404);
    expect(resp.status()).toBe(200);

    const body = await resp.json();
    expect(body).toHaveProperty('runs');
    expect(Array.isArray(body.runs)).toBe(true);
  });
});
