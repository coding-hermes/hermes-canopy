/**
 * Integration test — Composer→Canvas E2E regression (BUG-032 proof)
 *
 * Anti-phantom wiring test: proves that typing a message in the real composer
 * and clicking Send causes a React Flow canvas node to appear on the SAME page
 * WITHOUT a reload, exercising the REAL code path:
 *
 *   Composer UI → POST /trees/{id}/nodes
 *     → backend OnNodeMutation → response mirror into Yjs doc
 *     → useYjsTree re-render → React Flow canvas node
 *
 * No mocks. One Playwright browser context. Assert the node text is present
 * on the canvas after clicking Send — no navigation, no refresh.
 *
 * Run against a live stack: BASE_URL = http://localhost:5173 (Vite dev proxy
 * injects the dev JWT, so plain page loads + fetch() calls are authenticated).
 */
import { describe, it, expect, beforeAll, afterAll } from 'vitest';
import { chromium, type Browser, type Page } from '@playwright/test';
import { BASE_URL, isServerRunning } from './setup';

// How long to wait for the composer POST → Yjs mirror → canvas re-render.
const NODE_TIMEOUT = 20_000;

// Strongest stable signal that a node reached the canvas: the unique
// message text rendered inside a React Flow node card.
async function countCanvasNodes(page: Page, text: string): Promise<number> {
  return page.locator('.react-flow__node', { hasText: text }).count();
}

describe('Composer→Canvas E2E Regression (BUG-032)', () => {
  let browser: Browser;
  let page: Page;
  let serverAvailable = false;

  beforeAll(async () => {
    serverAvailable = await isServerRunning();
    if (!serverAvailable) return;

    browser = await chromium.launch({ headless: true });
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    page = await ctx.newPage();
  }, 30_000);

  afterAll(async () => {
    if (!serverAvailable) return;
    await page?.context()?.close();
    await browser?.close();
  });

  it(
    'composed message appears as a canvas node without a reload',
    async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      const suffix = `${Date.now()}_${Math.random().toString(36).slice(2, 8)}`;
      const treeTitle = `T267 BUG-032 ${suffix}`;
      const rootContent = `root-${suffix}`;
      const msg = `BUG032_REGRESSION_${suffix}`;

      // ── 1. Create a fresh, uniquely-named tree via the real REST API. ──
      // Authenticated through the Vite dev proxy, which injects the dev JWT.
      const api = page.request;
      const createResp = await api.post(`${BASE_URL}/api/v1/trees`, {
        headers: { 'Content-Type': 'application/json' },
        data: {
          title: treeTitle,
          rootMessage: { content: rootContent, contentFormat: 'markdown', nodeType: 'message' },
        },
      });
      if (!createResp.ok()) {
        throw new Error(`create tree failed: ${createResp.status()} ${await createResp.text()}`);
      }
      const tree = (await createResp.json()) as { id: string; title: string };
      expect(tree.id).toMatch(/^[0-9a-fA-F-]{36}$/);

      // ── 2. Open the tree page. ─────────────────────────────────────────
      const url = `${BASE_URL}/tree/${tree.id}`;
      await page.goto(url, { waitUntil: 'domcontentloaded', timeout: 15_000 });

      // Wait for the React Flow canvas to appear (hydration from
      // GET /trees/{id}/nodes succeeded).
      await page.waitForSelector('.react-flow', { state: 'visible', timeout: 15_000 });

      // The composer is enabled only once the local replica is ready.
      const composer = page.locator('[data-testid="composer-bar"]');
      await composer.waitFor({ state: 'visible', timeout: 15_000 });
      const textarea = page.locator('textarea[aria-label="Message input"]');
      await textarea.waitFor({ state: 'visible', timeout: 15_000 });
      await expect(await textarea.isEnabled()).toBe(true);

      // ── 3. Assert pre-condition: root node is visible, test message ──
      //     is NOT yet on the canvas.
      expect(await countCanvasNodes(page, rootContent)).toBeGreaterThan(0);
      expect(await countCanvasNodes(page, msg)).toBe(0);

      // ── 4. Type the message into the composer and click Send. ─────────
      await textarea.fill(msg);
      await page.locator('button[aria-label="Send message"]').click();

      // ── 5. THE ASSERTION: the sent message appears as a canvas node  ──
      //     on the SAME page, WITHOUT any reload or navigation. This only
      //     succeeds if composer POST → Yjs mirror → canvas path is wired.
      await expect
        .poll(async () => countCanvasNodes(page, msg), { timeout: NODE_TIMEOUT })
        .toBeGreaterThan(0);

      // ── 6. Evidence screenshot (best-effort). ──────────────────────────
      try {
        await page.screenshot({
          path: '/tmp/canopy-t267-composer-to-canvas.png',
          fullPage: false,
        });
      } catch {
        /* evidence screenshot is best-effort */
      }
    },
    60_000,
  );
});
