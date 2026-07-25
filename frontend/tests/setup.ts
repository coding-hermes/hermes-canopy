/**
 * Integration test helpers for Hermes Canopy.
 *
 * Provides shared browser/page setup, dev-server readiness checks,
 * and navigation utilities used by all integration test suites.
 */
import { chromium, type Browser, type Page } from '@playwright/test';

// ─── Constants ──────────────────────────────────────────────────────────

export const BASE_URL = 'http://localhost:5173';

/** How long to wait for page.goto before assuming the server is down. */
const GOTO_TIMEOUT = 8_000;

/** How long to wait for page load state after a successful navigation. */
const LOAD_TIMEOUT = 15_000;

// ─── Dev-server readiness ───────────────────────────────────────────────

/**
 * Check whether the Vite dev server is reachable.
 * Returns `true` if the server responds, `false` otherwise.
 */
export async function isServerRunning(): Promise<boolean> {
  let browser: Browser | null = null;
  try {
    browser = await chromium.launch({ headless: true });
    const page = await browser.newPage();
    await page.goto(BASE_URL, { timeout: GOTO_TIMEOUT, waitUntil: 'domcontentloaded' });
    return true;
  } catch {
    return false;
  } finally {
    await browser?.close();
  }
}

// ─── Browser fixture ────────────────────────────────────────────────────

export interface TestContext {
  browser: Browser;
  page: Page;
}

/**
 * Launch a Chromium browser + page.  Call `destroyContext` to tear down.
 */
export async function createTestContext(): Promise<TestContext> {
  const browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({
    viewport: { width: 1440, height: 900 },
  });
  return { browser, page };
}

/**
 * Close the page and browser.
 */
export async function destroyContext(ctx: TestContext): Promise<void> {
  await ctx.page.close();
  await ctx.browser.close();
}

// ─── Navigation helpers ─────────────────────────────────────────────────

/**
 * Navigate to a path relative to BASE_URL.
 * Throws if the server is not reachable.
 */
export async function goto(
  page: Page,
  path: string,
  opts?: { timeout?: number; waitUntil?: 'load' | 'domcontentloaded' | 'networkidle' },
): Promise<void> {
  const url = `${BASE_URL}${path}`;
  await page.goto(url, {
    timeout: opts?.timeout ?? GOTO_TIMEOUT,
    waitUntil: opts?.waitUntil ?? 'domcontentloaded',
  });

  // Give React a moment to hydrate
  await page.waitForLoadState('domcontentloaded');
  // Small buffer for React rendering
  await page.waitForTimeout(500);
}

/**
 * Attempt to navigate to a path.  Returns `true` on success, `false` if
 * the server is not reachable (allows graceful skip in describe blocks).
 */
export async function tryGoto(page: Page, path: string): Promise<boolean> {
  try {
    await goto(page, path);
    return true;
  } catch {
    return false;
  }
}

// ─── Assertion helpers ──────────────────────────────────────────────────

/**
 * Assert that the page contains visible text matching the given selector.
 */
export async function assertVisible(page: Page, selector: string, timeout = 5_000): Promise<void> {
  await page.waitForSelector(selector, { state: 'visible', timeout });
}

/**
 * Assert that an element with the given test-id is visible.
 */
export async function assertTestId(page: Page, testId: string, timeout = 5_000): Promise<void> {
  await assertVisible(page, `[data-testid="${testId}"]`, timeout);
}
