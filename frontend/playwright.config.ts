/**
 * Playwright configuration for Hermes Canopy integration tests.
 *
 * Used by vitest-based integration tests (see vitest.integration.config.ts).
 * The vitest test runner orchestrates Playwright browser automation through
 * the `playwright` / `@playwright/test` packages.
 */
import { defineConfig } from '@playwright/test';

export default defineConfig({
  /** Base URL for the Vite dev server */
  use: {
    baseURL: 'http://localhost:5173',
    /** Default navigation timeout */
    navigationTimeout: 10_000,
    /** Default action timeout */
    actionTimeout: 10_000,
    /** Screenshot on first failure (CI-friendly) */
    screenshot: 'only-on-failure',
    /** Trace on first failure for debugging */
    trace: 'on-first-retry',
  },

  /** Chromium-only for integration tests */
  projects: [
    {
      name: 'chromium',
      use: {
        browserName: 'chromium',
        /** Headless by default; set HEADED=true env to see the browser */
        headless: process.env.HEADED !== 'true',
        viewport: { width: 1440, height: 900 },
      },
    },
  ],

  /** Retry once on CI, zero locally */
  retries: process.env.CI ? 1 : 0,

  /** Reporter: list for terminal, html for CI artifacts */
  reporter: [['list'], ['html', { open: 'never' }]],
});
