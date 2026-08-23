/**
 * Integration test — Create-Tree dialog E2E (GAP-040 proof)
 *
 * The P0 regression: EVERY Create-Tree dialog submit 400'd — title-only
 * payloads were rejected by the backend ('root message content is
 * required') and title+root-message payloads omitted nodeType ('invalid
 * node type'). A brand-new user could NOT create a tree through the UI.
 *
 * This exercises the REAL dialog: click New Tree → fill title + root
 * message → Create → the new tree appears in the list → opens in Tree View
 * (React Flow canvas). Plus a regression guard: a title-only submit shows
 * the validation error and never hits the API.
 *
 * No mocks. One Playwright browser context. BASE_URL = http://localhost:5173
 * (Vite dev proxy injects the dev JWT, so plain page loads + fetch() calls
 * are authenticated).
 */
import { describe, it, expect, beforeAll, afterAll } from 'vitest';
import { chromium, type Browser, type Page } from '@playwright/test';
import { BASE_URL, isServerRunning } from './setup';
import { TreeCleanup } from './e2e-cleanup';

describe('Create Tree Dialog (GAP-040)', () => {
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
    'creates a tree via the dialog: title + root message → appears in list → opens in Tree View',
    async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      const suffix = `${Date.now()}_${Math.random().toString(36).slice(2, 8)}`;
      const treeTitle = `GAP040 E2E ${suffix}`;
      const rootContent = `GAP040 root ${suffix}`;

      // BUG-044 teardown: capture the tree id from the dialog's create call
      // so afterAll can delete it. The guard (dev-user owned, no session
      // metadata, non-protected title) runs before anything is tracked.
      page.on('response', async (resp) => {
        try {
          if (
            resp.request().method() === 'POST' &&
            resp.url().endsWith('/api/v1/trees') &&
            resp.ok()
          ) {
            cleanup.trackFromCreateBody(await resp.json());
          }
        } catch {
          /* tracking is best-effort */
        }
      });

      // ── 1. Open the Trees page and open the Create-Tree dialog. ──────
      await page.goto(`${BASE_URL}/trees`, { waitUntil: 'domcontentloaded', timeout: 15_000 });
      await page.locator('button', { hasText: 'New Tree' }).waitFor({ state: 'visible', timeout: 15_000 });
      await page.locator('button', { hasText: 'New Tree' }).click();

      const dialog = page.locator('#create-tree-title');
      await dialog.waitFor({ state: 'visible', timeout: 5_000 });

      // The Root Message field is REQUIRED — the label must say so.
      const rootLabel = page.locator('label[for="create-tree-root-msg"]');
      expect(await rootLabel.innerText()).toContain('*');

      // ── 2. Fill title + root message and submit. ────────────────────
      await page.locator('#create-tree-title').fill(treeTitle);
      await page.locator('#create-tree-root-msg').fill(rootContent);
      await page.locator('button', { hasText: 'Create' }).click();

      // ── 3. The dialog closes and the new tree appears in the list. ──
      await page.locator(`h3:has-text("${treeTitle}")`).waitFor({ state: 'visible', timeout: 20_000 });

      // ── 4. Click the tree card → Tree View opens (React Flow canvas). ──
      await page.locator(`h3:has-text("${treeTitle}")`).click();
      await page.waitForSelector('.react-flow', { state: 'visible', timeout: 30_000 });

      // The root message content must be rendered on the canvas.
      await page
        .locator('.react-flow__node', { hasText: rootContent })
        .waitFor({ state: 'visible', timeout: 30_000 });
    },
    120_000,
  );

  it(
    'rejects a title-only submit with a visible validation error and NO API call',
    async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      // Count POST /trees attempts while the dialog is open.
      let createCalls = 0;
      const onRequest = (req: { url: () => string; method: () => string }) => {
        if (req.method() === 'POST' && req.url().endsWith('/api/v1/trees')) createCalls += 1;
      };
      page.on('request', onRequest);
      try {
        await page.goto(`${BASE_URL}/trees`, { waitUntil: 'domcontentloaded', timeout: 15_000 });
        await page.locator('button', { hasText: 'New Tree' }).waitFor({ state: 'visible', timeout: 15_000 });
        await page.locator('button', { hasText: 'New Tree' }).click();

        await page.locator('#create-tree-title').waitFor({ state: 'visible', timeout: 5_000 });
        await page.locator('#create-tree-title').fill(`GAP040 invalid ${Date.now()}`);
        await page.locator('button', { hasText: 'Create' }).click();

        // Visible validation error, and the dialog stays open.
        const errorBox = page.locator('#create-tree-error');
        await errorBox.waitFor({ state: 'visible', timeout: 5_000 });
        expect(await errorBox.innerText()).toContain('Root message is required');

        // Give any stray request a moment to appear before asserting.
        await page.waitForTimeout(1_000);
        expect(createCalls).toBe(0);
      } finally {
        page.off('request', onRequest);
      }
    },
    60_000,
  );
});
