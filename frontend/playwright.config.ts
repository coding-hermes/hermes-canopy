/**
 * Playwright configuration for the frontend toolchain smoke specs.
 *
 * Scope is pinned to frontend/e2e/*.pw.ts (testDir/testMatch) so
 * `npx playwright test` never falls back to default whole-tree discovery,
 * which collects the vitest suites (src/__tests__/**, tests/**) and crashes
 * at collection time (import.meta.env is undefined outside vitest).
 *
 * The real E2E / integration suite is vitest-based (see
 * vitest.integration.config.ts): `npm run test:integration` orchestrates
 * Playwright browser automation through the `playwright` /
 * `@playwright/test` packages.
 */
import { defineConfig } from '@playwright/test';

export default defineConfig({
  /** Only ever collect the Playwright smoke specs under e2e/ */
  testDir: './e2e',
  testMatch: '**/*.pw.ts',

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

  /**
   * Artifacts (traces, screenshots, .last-run.json) must NEVER land in
   * frontend/test-results/ — that directory holds TRACKED audit files
   * (accessibility-audit*.json/.md, run-a11y-audit.*) that a default run
   * deletes or overwrites. Keep generated artifacts in a git-ignored
   * sibling of the HTML report dir (nesting inside playwright-report/
   * makes the HTML reporter wipe them, since it clears its folder first).
   */
  outputDir: './playwright-artifacts',
});
