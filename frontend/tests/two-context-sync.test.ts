/**
 * Integration test — Two-Context Realtime Sync (WIRE-001 proof)
 *
 * Anti-phantom wiring test: proves that a message sent in browser context A
 * propagates to an already-open browser context B on the SAME tree WITHOUT a
 * manual reload, exercising the REAL code path:
 *
 *   Composer (A) → POST /trees/{id}/nodes
 *     → backend OnNodeMutation → SSE broadcast (event: node_added)
 *     → EventSource in B (/trees/{id}/events) → SSESyncProvider.applyNodeUpdate
 *     → Yjs doc mutation → useYjsTree re-render → React Flow canvas node
 *
 * No mocks. Two independent Playwright browser contexts share one Chromium
 * instance, each with its own storage/EventSource connection.
 *
 * Run against a live stack: BASE_URL = http://localhost:5173 (Vite dev proxy
 * injects the dev JWT, so plain page loads + fetch() calls are authenticated).
 */
import { describe, it, expect, beforeAll, afterAll } from 'vitest';
import { chromium, type Browser, type BrowserContext, type Page } from '@playwright/test';
import { BASE_URL, isServerRunning } from './setup';

// How long to wait for the cross-context SSE propagation + canvas re-render.
const SYNC_TIMEOUT = 20_000;

// Strongest stable signal that a node reached context B's canvas: the unique
// message text rendered inside a React Flow node card.
async function countSyncedNodes(page: Page, text: string): Promise<number> {
  return page.locator('.react-flow__node', { hasText: text }).count();
}

describe('Two-Context Realtime Sync (WIRE-001)', () => {
  let browser: Browser;
  let ctxA: BrowserContext;
  let ctxB: BrowserContext;
  let pageA: Page;
  let pageB: Page;
  let serverAvailable = false;

  beforeAll(async () => {
    serverAvailable = await isServerRunning();
    if (!serverAvailable) return;

    // Two INDEPENDENT browser contexts (not two pages in one context).
    browser = await chromium.launch({ headless: true });
    ctxA = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    ctxB = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    pageA = await ctxA.newPage();
    pageB = await ctxB.newPage();
  }, 30_000);

  afterAll(async () => {
    if (!serverAvailable) return;
    await pageA?.close();
    await pageB?.close();
    await ctxA?.close();
    await ctxB?.close();
    await browser?.close();
  });

  it(
    'message sent in context A appears in context B without a reload',
    async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      const suffix = `${Date.now()}_${Math.random().toString(36).slice(2, 8)}`;
      const treeTitle = `T265 Sync ${suffix}`;
      const rootContent = `root-${suffix}`;
      const msg = `SYNC_MESSAGE_${suffix}`;

      // ── 1. Create a fresh, uniquely-named tree via the real REST API. ──
      // Authenticated through the Vite dev proxy, which injects the dev JWT.
      // Use Playwright's APIRequestContext (same origin as BASE_URL) so no
      // page navigation is required and cross-origin fetch is avoided.
      const api = pageA.request;
      const createResp = await api.post(`${BASE_URL}/api/v1/trees`, {
        headers: { 'Content-Type': 'application/json' },
        data: { title: treeTitle, rootMessage: { content: rootContent, contentFormat: 'markdown', nodeType: 'message' } },
      });
      if (!createResp.ok()) {
        throw new Error(`create tree failed: ${createResp.status()} ${await createResp.text()}`);
      }
      const tree = (await createResp.json()) as { id: string; title: string };
      expect(tree.id).toMatch(/^[0-9a-fA-F-]{36}$/);

      // ── 2. Open the SAME tree in both independent contexts. ──────────
      const url = `${BASE_URL}/tree/${tree.id}`;

      await pageA.goto(url, { waitUntil: 'domcontentloaded', timeout: 15_000 });
      await pageB.goto(url, { waitUntil: 'domcontentloaded', timeout: 15_000 });

      // Both contexts must reach the React Flow canvas (hydration from
      // GET /trees/{id}/nodes + SSE provider connect succeeded).
      await pageA.waitForSelector('.react-flow', { state: 'visible', timeout: 15_000 });
      await pageB.waitForSelector('.react-flow', { state: 'visible', timeout: 15_000 });

      // The composer is enabled only once the local replica is ready.
      const composerA = pageA.locator('[data-testid="composer-bar"]');
      await composerA.waitFor({ state: 'visible', timeout: 15_000 });
      const textareaA = pageA.locator('textarea[aria-label="Message input"]');
      await textareaA.waitFor({ state: 'visible', timeout: 15_000 });
      await expect(await textareaA.isEnabled()).toBe(true);

      // Sanity: B already shows the root node (proves both contexts loaded
      // the same tree), but NOT the message we are about to send.
      expect(await countSyncedNodes(pageB, rootContent)).toBeGreaterThan(0);
      expect(await countSyncedNodes(pageB, msg)).toBe(0);

      // ── 3. Send the message in context A via the real composer. ───────
      await textareaA.fill(msg);
      await pageA.locator('button[aria-label="Send message"]').click();

      // A's own canvas reflects the sent message (mirrored locally).
      await expect
        .poll(async () => countSyncedNodes(pageA, msg), { timeout: SYNC_TIMEOUT })
        .toBeGreaterThan(0);

      // ── 4. THE ASSERTION: message propagates to B via SSE/Yjs, ────────
      //     with no manual reload. This only succeeds if the realtime path
      //     (EventSource → pushUpdate broadcast → Yjs apply) is wired.
      await expect
        .poll(async () => countSyncedNodes(pageB, msg), { timeout: SYNC_TIMEOUT })
        .toBeGreaterThan(0);

      // Capture evidence of the cross-context sync (no reload was performed).
      try {
        await pageB.screenshot({ path: '/tmp/canopy-t265-two-context-sync.png', fullPage: false });
      } catch {
        /* evidence screenshot is best-effort */
      }
    },
    60_000,
  );
});
