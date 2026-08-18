/**
 * Integration test — Mobile sidebar drawer (Bane 08-18)
 *
 * Anti-phantom UI test: the sidebar must be usable on mobile portrait —
 * hidden by default, opened via the hamburger, closed via the X button,
 * the backdrop, or navigation — and the close affordance must exist in
 * EVERY UI mode (the drawer lives in the app Layout, so this covers
 * Dashboard / Trees / Tree View / Nodes / Topics / Cards / Workspace /
 * Agents / Reviews / Approvals at once).
 *
 * Desktop must stay pixel-identical: static sidebar, no hamburger.
 *
 * Run against a live stack: BASE_URL = http://localhost:5173 (Vite dev
 * proxy injects the dev JWT).
 *
 * NOTE: this harness has no Playwright expect matchers (no toBeVisible),
 * so visibility is asserted via boundingBox().x (the drawer slides off
 * the left edge at translateX(-100%) when closed) and locator counts.
 */
import { describe, it, expect, beforeAll, afterAll } from 'vitest';
import { chromium, type Browser, type Page } from '@playwright/test';
import { BASE_URL, isServerRunning } from './setup';

const MOBILE = { width: 390, height: 844 };
const DESKTOP = { width: 1440, height: 900 };

/** X position of the drawer's left edge — negative = slid off-screen. */
async function sidebarX(page: Page): Promise<number> {
  const box = await page.getByTestId('sidebar').boundingBox();
  return box ? box.x : -9999;
}

describe('Mobile sidebar drawer (Bane 08-18)', () => {
  let browser: Browser;
  let page: Page;
  let serverAvailable = false;

  beforeAll(async () => {
    serverAvailable = await isServerRunning();
    if (!serverAvailable) return;
    browser = await chromium.launch({ headless: true });
    page = await browser.newPage({ viewport: MOBILE });
  }, 30_000);

  afterAll(async () => {
    if (!serverAvailable) return;
    await page?.context()?.close();
    await browser?.close();
  });

  it('mobile: sidebar hidden by default, hamburger visible', async () => {
    if (!serverAvailable) return;
    await page.goto(`${BASE_URL}/trees`);
    await expect.poll(() => sidebarX(page), { timeout: 5000 }).toBeLessThan(0);
    expect(await page.getByTestId('sidebar-open').isVisible()).toBe(true);
  });

  it('mobile: hamburger opens drawer, X button closes it', async () => {
    if (!serverAvailable) return;
    await page.goto(`${BASE_URL}/trees`);
    await page.getByTestId('sidebar-open').click();
    await expect.poll(() => sidebarX(page), { timeout: 5000 }).toBeGreaterThanOrEqual(0);
    expect(await page.getByTestId('sidebar-close').isVisible()).toBe(true);
    await page.getByTestId('sidebar-close').click();
    await expect.poll(() => sidebarX(page), { timeout: 5000 }).toBeLessThan(0);
  });

  it('mobile: backdrop click closes the drawer', async () => {
    if (!serverAvailable) return;
    await page.goto(`${BASE_URL}/trees`);
    await page.getByTestId('sidebar-open').click();
    await expect.poll(() => page.getByTestId('sidebar-backdrop').count(), { timeout: 5000 }).toBe(1);
    await page.getByTestId('sidebar-backdrop').click({ position: { x: 380, y: 400 } });
    await expect.poll(() => sidebarX(page), { timeout: 5000 }).toBeLessThan(0);
    expect(await page.getByTestId('sidebar-backdrop').count()).toBe(0);
  });

  it('mobile: navigating closes the drawer (all UI modes)', async () => {
    if (!serverAvailable) return;
    await page.goto(`${BASE_URL}/trees`);
    await page.getByTestId('sidebar-open').click();
    await expect.poll(() => sidebarX(page), { timeout: 5000 }).toBeGreaterThanOrEqual(0);
    await page.getByTestId('sidebar').getByRole('link', { name: 'Dashboard' }).click();
    await expect.poll(() => sidebarX(page), { timeout: 5000 }).toBeLessThan(0);
    expect(page.url()).toBe(`${BASE_URL}/`);
  });

  it('desktop: sidebar static and visible, no hamburger', async () => {
    if (!serverAvailable) return;
    const desktopPage = await browser.newPage({ viewport: DESKTOP });
    try {
      await desktopPage.goto(`${BASE_URL}/trees`);
      const x = await sidebarX(desktopPage);
      expect(x).toBeGreaterThanOrEqual(0);
      expect(await desktopPage.getByTestId('sidebar-open').isVisible()).toBe(false);
      expect(await desktopPage.getByTestId('sidebar-close').isVisible()).toBe(false);
    } finally {
      await desktopPage.close();
    }
  });
});
