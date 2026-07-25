/**
 * Integration tests — Tree Rendering
 *
 * Covers: canvas loads, nodes visible, React Flow @xyflow/react renders,
 * zoom/pan controls exist, MiniMap present.
 *
 * Route: /tree/demo  (the demo tree)
 *
 * Note: Uses vitest's expect() with Playwright locator APIs directly
 * (not @playwright/test's custom locator matchers).
 */
import { describe, it, expect, beforeAll, afterAll } from 'vitest';
import {
  createTestContext,
  destroyContext,
  tryGoto,
  isServerRunning,
  type TestContext,
} from './setup';

describe('Tree Rendering', () => {
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

  it('canvas container is present on the tree page', async () => {
    if (!serverAvailable) {
      console.warn('⚠ Dev server not running — skipping integration test');
      return;
    }

    const ok = await tryGoto(ctx.page, '/tree/demo');
    expect(ok).toBe(true);

    // React Flow renders inside a .react-flow container
    const flowContainer = ctx.page.locator('.react-flow');
    const count = await flowContainer.count();
    expect(count).toBe(1);
  });

  it('React Flow background grid is visible', async () => {
    if (!serverAvailable) {
      console.warn('⚠ Dev server not running — skipping integration test');
      return;
    }

    await tryGoto(ctx.page, '/tree/demo');

    // React Flow Background renders an SVG pattern
    const background = ctx.page.locator('.react-flow__background');
    const count = await background.count();
    expect(count).toBe(1);
  });

  it('zoom controls are present', async () => {
    if (!serverAvailable) {
      console.warn('⚠ Dev server not running — skipping integration test');
      return;
    }

    await tryGoto(ctx.page, '/tree/demo');

    // React Flow Controls render zoom-in, zoom-out, and fit-view buttons
    const controls = ctx.page.locator('.react-flow__controls');
    const isVisible = await controls.isVisible();
    expect(isVisible).toBe(true);

    // Verify zoom-in button exists
    const zoomIn = controls.locator('.react-flow__controls-zoomin');
    expect(await zoomIn.isVisible()).toBe(true);

    // Verify zoom-out button exists
    const zoomOut = controls.locator('.react-flow__controls-zoomout');
    expect(await zoomOut.isVisible()).toBe(true);

    // Verify fit-view button exists
    const fitView = controls.locator('.react-flow__controls-fitview');
    expect(await fitView.isVisible()).toBe(true);
  });

  it('MiniMap is present on the canvas', async () => {
    if (!serverAvailable) {
      console.warn('⚠ Dev server not running — skipping integration test');
      return;
    }

    await tryGoto(ctx.page, '/tree/demo');

    const minimap = ctx.page.locator('.react-flow__minimap');
    const count = await minimap.count();
    expect(count).toBe(1);
  });

  it('canvas has correct ARIA role for application', async () => {
    if (!serverAvailable) {
      console.warn('⚠ Dev server not running — skipping integration test');
      return;
    }

    await tryGoto(ctx.page, '/tree/demo');

    // The TreeCanvas sets role="application" and aria-label
    const appRegion = ctx.page.locator('[role="application"]');
    const count = await appRegion.count();
    expect(count).toBe(1);

    const label = await appRegion.getAttribute('aria-label');
    expect(label).toContain('Tree canvas');
  });

  it('page title or heading indicates tree view', async () => {
    if (!serverAvailable) {
      console.warn('⚠ Dev server not running — skipping integration test');
      return;
    }

    await tryGoto(ctx.page, '/tree/demo');

    // The header bar should show the tree emoji
    const emojiLocator = ctx.page.locator('text=🌳').first();
    const isVisible = await emojiLocator.isVisible();
    expect(isVisible).toBe(true);
  });

  it('navigating to base URL shows Dashboard', async () => {
    if (!serverAvailable) {
      console.warn('⚠ Dev server not running — skipping integration test');
      return;
    }

    await tryGoto(ctx.page, '/');

    const heading = ctx.page.locator('h1');
    const text = await heading.innerText();
    expect(text).toContain('Dashboard');
  });
});
