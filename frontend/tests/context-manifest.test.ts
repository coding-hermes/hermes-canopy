/**
 * Integration test — Context Manifest UI (WIRE-002 proof)
 *
 * Anti-phantom wiring test: proves that selecting a canvas node renders
 * the Context Manifest Panel with the token budget AND the ancestry chain
 * in the node detail, WITHOUT a reload, exercising the REAL code path:
 *
 *   Canvas node click → onSelectionChange
 *     → ContextManifestPanel mount with nodeId
 *     → fetch GET /api/v1/context/{node_id}?budget=8000
 *     → token usage + budget meter render (collapsed)
 *     → toggle click → ancestry section renders with real items
 *
 * No mocks. One Playwright browser context. Assert the token-usage text
 * AND ancestry items are present after a node click — no navigation, no
 * refresh, no mocked fetch.
 *
 * Run against a live stack: BASE_URL = http://localhost:5173 (Vite dev
 * proxy injects the dev JWT, so plain page loads + fetch() calls are
 * authenticated).
 */
import { describe, it, expect, beforeAll, afterAll } from 'vitest';
import { chromium, type Browser, type Page } from '@playwright/test';
import { BASE_URL, isServerRunning } from './setup';
import { TreeCleanup } from './e2e-cleanup';

// How long to wait for the async context manifest fetch + render.
const MANIFEST_TIMEOUT = 20_000;

describe('Context Manifest UI (WIRE-002)', () => {
  let browser: Browser;
  let page: Page;
  let serverAvailable = false;
  // BUG-044 teardown: scratch trees this suite creates must not accumulate
  // in the live compose database.
  const cleanup = new TreeCleanup();

  beforeAll(async () => {
    serverAvailable = await isServerRunning();
    if (!serverAvailable) return;

    browser = await chromium.launch({ headless: true });
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    page = await ctx.newPage();
  }, 30_000);

  afterAll(async () => {
    if (!serverAvailable) return;
    // BUG-044: remove the scratch trees this suite created before exiting
    // (afterAll runs even if a case failed mid-way).
    await cleanup.sweep();
    await page?.context()?.close();
    await browser?.close();
  });

  it(
    'selecting a canvas node renders the Context Manifest Panel with token budget and ancestry chain',
    async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      const suffix = `${Date.now()}_${Math.random().toString(36).slice(2, 8)}`;
      const treeTitle = `T268 WIRE-002 ${suffix}`;
      const rootContent = `root-${suffix}`;

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
      cleanup.trackFromCreateBody(await createResp.json());

      // ── 2. Open the tree page. ─────────────────────────────────────────
      const url = `${BASE_URL}/tree/${tree.id}`;
      await page.goto(url, { waitUntil: 'domcontentloaded', timeout: 15_000 });

      // Wait for the React Flow canvas to appear (hydration succeeded).
      await page.waitForSelector('.react-flow', { state: 'visible', timeout: 15_000 });

      // Sanity: the root node is rendered on the canvas.
      const rootNode = page.locator('.react-flow__node', { hasText: rootContent });
      await rootNode.first().waitFor({ state: 'visible', timeout: 15_000 });

      // ── 3. CLICK a canvas node to select it. ───────────────────────────
      // This is the trigger: onSelectionChange(node.id) → ContextManifestPanel
      // mounts and fetches GET /api/v1/context/{node_id}?budget=8000.
      await rootNode.first().click();

      // ── 4. ASSERT: Context Manifest Panel is visible. ──────────────────
      const panel = page.locator('[data-testid="context-manifest-panel"]');
      await panel.waitFor({ state: 'visible', timeout: 15_000 });

      // ── 5. ASSERT TOKEN BUDGET: token-usage text matches format. ───────
      // Format: "N,NNN / M,NNN tokens" (e.g. "1,240 / 8,000 tokens").
      // Use expect.poll — the manifest fetch is async after the click.
      const tokenUsage = page.locator('[data-testid="context-token-usage"]');
      await expect
        .poll(
          async () => {
            const text = await tokenUsage.textContent();
            return text;
          },
          { timeout: MANIFEST_TIMEOUT },
        )
        .toMatch(/\d[\d,]* \/ [\d,]+ tokens/);

      // Also assert the budget meter rendered with accessible bounds.
      const meter = page.locator('[data-testid="context-budget-meter"]');
      await expect.poll(
        async () => {
          const role = await meter.getAttribute('role');
          const valuemax = await meter.getAttribute('aria-valuemax');
          return { role, valuemax };
        },
        { timeout: MANIFEST_TIMEOUT },
      ).toEqual({ role: 'meter', valuemax: '8000' });

      // ── 6. OPEN THE DETAIL: click toggle, wait for detail section. ────
      const toggle = page.locator('[data-testid="context-manifest-toggle"]');
      await toggle.click();

      // The detail section renders ONLY when toggle is open && manifest loaded.
      await page
        .locator('[data-testid="context-manifest-detail"]')
        .waitFor({ state: 'visible', timeout: MANIFEST_TIMEOUT });

      await expect
        .poll(
          async () => toggle.getAttribute('aria-expanded'),
          { timeout: 5_000 },
        )
        .toBe('true');

      // ── 7. ASSERT ANCESTRY: section visible with ≥1 item containing   ──
      //     the unique root message text. This proves REAL backend data
      //     reached the UI (the root node is the selected node's ancestor
      //     — a single-root tree's ancestry contains the root node itself).
      const ancestry = page.locator('[data-testid="context-section-ancestry"]');
      await ancestry.waitFor({ state: 'visible', timeout: MANIFEST_TIMEOUT });

      const ancestryItems = ancestry.locator('[data-testid="context-manifest-item"]');
      await expect
        .poll(
          async () => {
            const count = await ancestryItems.count();
            const texts: string[] = [];
            for (let i = 0; i < count; i++) {
              texts.push((await ancestryItems.nth(i).textContent()) ?? '');
            }
            return { count, texts };
          },
          { timeout: MANIFEST_TIMEOUT },
        )
        .toMatchObject({
          count: expect.any(Number),
        });

      // Verify at least one ancestry item contains the root message content.
      const hasRootItem = await expect
        .poll(
          async () => {
            const count = await ancestryItems.count();
            for (let i = 0; i < count; i++) {
              const text = (await ancestryItems.nth(i).textContent()) ?? '';
              if (text.includes(rootContent)) return true;
            }
            return false;
          },
          { timeout: MANIFEST_TIMEOUT },
        )
        .toBe(true);

      // ── 8. Evidence screenshot (best-effort). ──────────────────────────
      try {
        await page.screenshot({
          path: '/tmp/canopy-t268-context-manifest.png',
          fullPage: false,
        });
      } catch {
        /* evidence screenshot is best-effort */
      }
    },
    90_000,
  );
});
