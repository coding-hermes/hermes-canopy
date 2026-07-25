/**
 * Integration tests — CRUD Pages
 *
 * Covers: TreesPage, NodesPage, TopicsPage, CardsPage render correctly,
 * headings visible, filter/search inputs present, error/empty states.
 */
import { describe, it, expect, beforeAll, afterAll } from 'vitest';
import {
  createTestContext,
  destroyContext,
  tryGoto,
  isServerRunning,
  type TestContext,
} from './setup';

describe('CRUD Pages', () => {
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

  // ── TreesPage ───────────────────────────────────────────────────────

  describe('TreesPage', () => {
    it('renders the page heading', async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      await tryGoto(ctx.page, '/trees');

      const heading = ctx.page.locator('h1');
      const text = await heading.innerText();
      expect(text).toContain('Trees');
    });

    it('has a New Tree button', async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      await tryGoto(ctx.page, '/trees');

      const newBtn = ctx.page.locator('button', { hasText: 'New Tree' });
      expect(await newBtn.isVisible()).toBe(true);
    });

    it('has a Refresh button', async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      await tryGoto(ctx.page, '/trees');

      const refreshBtn = ctx.page.locator('button', { hasText: 'Refresh' });
      expect(await refreshBtn.isVisible()).toBe(true);
    });

    it('page renders without crashing (has body content)', async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      await tryGoto(ctx.page, '/trees');

      // Wait for any meaningful content
      await ctx.page.waitForTimeout(2_000);

      const body = ctx.page.locator('body');
      const text = await body.innerText();
      // The page should at minimum have rendered (not blank)
      expect(text.length).toBeGreaterThan(20);
    });
  });

  // ── NodesPage ───────────────────────────────────────────────────────

  describe('NodesPage', () => {
    it('renders the page heading', async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      await tryGoto(ctx.page, '/nodes');

      const heading = ctx.page.locator('h1');
      const text = await heading.innerText();
      expect(text).toContain('Nodes');
    });

    it('has a tree selector dropdown', async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      await tryGoto(ctx.page, '/nodes');

      const select = ctx.page.locator('#nodes-tree-select');
      expect(await select.isVisible()).toBe(true);
    });

    it('shows "No tree selected" placeholder', async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      await tryGoto(ctx.page, '/nodes');

      const placeholder = ctx.page.locator('text=No tree selected');
      expect(await placeholder.isVisible()).toBe(true);
    });
  });

  // ── TopicsPage ──────────────────────────────────────────────────────

  describe('TopicsPage', () => {
    it('renders the page heading', async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      await tryGoto(ctx.page, '/topics');

      const heading = ctx.page.locator('h1');
      const text = await heading.innerText();
      expect(text).toContain('Topics');
    });

    it('has a tree selector dropdown', async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      await tryGoto(ctx.page, '/topics');

      const select = ctx.page.locator('#topics-tree-select');
      expect(await select.isVisible()).toBe(true);
    });

    it('shows "Select a tree" placeholder', async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      await tryGoto(ctx.page, '/topics');

      const placeholder = ctx.page.locator('text=Select a tree');
      expect(await placeholder.isVisible()).toBe(true);
    });
  });

  // ── CardsPage ───────────────────────────────────────────────────────

  describe('CardsPage', () => {
    it('renders the page heading', async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      await tryGoto(ctx.page, '/cards');

      const heading = ctx.page.locator('h1');
      const text = await heading.innerText();
      expect(text).toContain('Cards');
    });

    it('has a tree selector dropdown', async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      await tryGoto(ctx.page, '/cards');

      const select = ctx.page.locator('#cards-tree-select');
      expect(await select.isVisible()).toBe(true);
    });

    it('page renders without error', async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      await tryGoto(ctx.page, '/cards');

      // The heading should be visible
      const heading = ctx.page.locator('h1');
      expect(await heading.isVisible()).toBe(true);
    });
  });
});
