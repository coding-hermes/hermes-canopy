/**
 * Integration test — Same-User Two-Tab Sync (BUG-040 regression guard)
 *
 * BUG-040 reported that a SECOND TAB of the same user (same browser profile,
 * shared localStorage/IndexedDB) stayed stale until reload while cross-user/
 * cross-browser sync passed. Root cause proved to be the BUG-042 service-
 * worker defect pair (commit b3b389b): sw.ts cached never-ending SSE /events
 * streams, wedging CacheStorage and freezing realtime paths, compounded by
 * thin node_added echoes creating content-less Yjs nodes that crashed
 * buildSnapshot. With those fixed, same-user two-tab sync works over TWO
 * redundant paths:
 *
 *   1. SSE hub broadcast (server fans out to every subscriber — including
 *      the sender's sibling tabs; no same-user filtering exists).
 *   2. y-indexeddb BroadcastChannel: both tabs share the
 *      `canopy-tree-{treeId}` IndexedDB database, and y-indexeddb relays
 *      local doc updates between tabs directly (verified: two-tab sync
 *      survives a route.abort() on tab B's SSE stream alone).
 *
 * This test pins the USER-VISIBLE contract either way: a message sent in tab
 * A must appear on tab B's canvas WITHOUT a reload. It differs from
 * two-context-sync.test.ts (WIRE-001) in exactly the way BUG-040 reported:
 * BOTH pages live in the SAME browser context, so storage is shared.
 *
 * Run against a live stack: BASE_URL = http://localhost:5173 (Vite dev proxy
 * injects the dev JWT, so plain page loads + fetch() calls are authenticated).
 */
import { describe, it, expect, beforeAll, afterAll } from 'vitest';
import { chromium, type Browser, type BrowserContext, type Page } from '@playwright/test';
import { BASE_URL, isServerRunning } from './setup';
import { TreeCleanup } from './e2e-cleanup';

// How long to wait for cross-tab propagation + canvas re-render. Generous:
// delivery may arrive via SSE or the y-indexeddb BroadcastChannel, but it
// MUST arrive without a reload.
const SYNC_TIMEOUT = 20_000;

// Strongest stable signal that a node reached tab B's canvas: the unique
// message text rendered inside a React Flow node card.
async function countSyncedNodes(page: Page, text: string): Promise<number> {
  return page.locator('.react-flow__node', { hasText: text }).count();
}

describe('Same-User Two-Tab Sync (BUG-040)', () => {
  let browser: Browser;
  let context: BrowserContext;
  let pageA: Page;
  let pageB: Page;
  let serverAvailable = false;
  // BUG-044 teardown: scratch trees this suite creates must not accumulate
  // in the live compose database.
  const cleanup = new TreeCleanup();

  beforeAll(async () => {
    serverAvailable = await isServerRunning();
    if (!serverAvailable) return;

    // ONE browser context, TWO pages — the same-user two-tab case.
    // Shared localStorage, shared IndexedDB, shared service worker.
    browser = await chromium.launch({ headless: true });
    context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    pageA = await context.newPage();
    pageB = await context.newPage();
  }, 30_000);

  afterAll(async () => {
    if (!serverAvailable) return;
    // BUG-044: remove the scratch trees this suite created before exiting
    // (afterAll runs even if a case failed mid-way).
    await cleanup.sweep();
    await pageA?.close();
    await pageB?.close();
    await context?.close();
    await browser?.close();
  });

  it(
    'message sent in tab A appears in tab B (same context) without a reload',
    async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      const suffix = `${Date.now()}_${Math.random().toString(36).slice(2, 8)}`;
      const treeTitle = `BUG-040 ${suffix}`;
      const rootContent = `root-${suffix}`;
      const msg = `TWO_TAB_${suffix}`;

      // ── 1. Fresh uniquely-named tree via the real REST API. ──────────
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
      cleanup.trackFromCreateBody(await createResp.json());

      // ── 2. Open the SAME tree in TWO TABS of the SAME context. ───────
      const url = `${BASE_URL}/tree/${tree.id}`;

      await pageA.goto(url, { waitUntil: 'domcontentloaded', timeout: 15_000 });
      await pageB.goto(url, { waitUntil: 'domcontentloaded', timeout: 15_000 });

      // Both tabs must reach the React Flow canvas.
      await pageA.waitForSelector('.react-flow', { state: 'visible', timeout: 15_000 });
      await pageB.waitForSelector('.react-flow', { state: 'visible', timeout: 15_000 });

      // Composer enabled once the local replica is ready.
      const composerA = pageA.locator('[data-testid="composer-bar"]');
      await composerA.waitFor({ state: 'visible', timeout: 15_000 });
      const textareaA = pageA.locator('textarea[aria-label="Message input"]');
      await textareaA.waitFor({ state: 'visible', timeout: 15_000 });
      expect(await textareaA.isEnabled()).toBe(true);

      // Sanity: B already shows the root node (same tree loaded) but not
      // the message we are about to send.
      expect(await countSyncedNodes(pageB, rootContent)).toBeGreaterThan(0);
      expect(await countSyncedNodes(pageB, msg)).toBe(0);

      // ── 3. Send from tab A via the real composer shortcut ────────────
      //     (Cmd/Ctrl+Enter sends; plain Enter is a newline).
      await textareaA.fill(msg);
      await textareaA.press('Control+Enter');

      // A mirrors its own message locally first.
      await expect
        .poll(async () => countSyncedNodes(pageA, msg), { timeout: SYNC_TIMEOUT })
        .toBeGreaterThan(0);

      // ── 4. THE ASSERTION: tab B sees the node WITHOUT a reload. ──────
      await expect
        .poll(async () => countSyncedNodes(pageB, msg), { timeout: SYNC_TIMEOUT })
        .toBeGreaterThan(0);

      // Capture evidence of the cross-tab sync (no reload was performed).
      try {
        await pageB.screenshot({ path: '/tmp/canopy-t392-two-tab-sync.png', fullPage: false });
      } catch {
        /* evidence screenshot is best-effort */
      }
    },
    90_000,
  );
});
