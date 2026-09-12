/**
 * Playwright toolchain smoke spec — the ONLY thing `npx playwright test`
 * collects in this repo (see playwright.config.ts: testDir e2e/, testMatch
 * matching every .pw.ts).
 *
 * Server-independent: no canopyd, no PostgreSQL, no vite dev server.
 * What it pins down:
 *   1. Chromium launches and renders via this repo's Playwright config.
 *   2. The documented E2E entry point (`npm run test:integration`) is intact.
 *   3. Config scoping holds — a regression to Playwright's default
 *      whole-tree discovery (which collects the vitest suites and dies on
 *      import.meta.env) fails here instead of silently breaking the run.
 */
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { expect, test } from '@playwright/test';

// Playwright transpiles specs as ESM: resolve from import.meta.url.
const thisFile = fileURLToPath(import.meta.url); // .../frontend/e2e/toolchain.pw.ts
const frontendDir = path.dirname(path.dirname(thisFile)); // .../frontend

test('chromium launches and renders a document', async ({ page }) => {
  await page.goto(
    'data:text/html,<html><head><title>canopy toolchain smoke</title></head><body>ready</body></html>',
  );
  await expect(page).toHaveTitle('canopy toolchain smoke');
});

test('the documented integration entry point is unchanged', async () => {
  const pkgJson = JSON.parse(
    readFileSync(path.join(frontendDir, 'package.json'), 'utf8'),
  ) as { scripts?: Record<string, string> };
  expect(pkgJson.scripts?.['test:integration']).toBe(
    'vitest run --config vitest.integration.config.ts',
  );
});

test('playwright only collects e2e/*.pw.ts specs', async () => {
  const configSource = readFileSync(
    path.join(frontendDir, 'playwright.config.ts'),
    'utf8',
  );
  const testDir = configSource.match(/testDir:\s*'([^']+)'/)?.[1];
  const testMatch = configSource.match(/testMatch:\s*'([^']+)'/)?.[1];

  // Either binding missing => default discovery sweeps the whole frontend/
  // tree and collects the vitest suites, which crash at import time.
  expect(testDir, 'playwright.config.ts must pin testDir').toBeTruthy();
  expect(testMatch, 'playwright.config.ts must pin testMatch').toBeTruthy();
  expect(path.resolve(frontendDir, testDir!)).toBe(
    path.join(frontendDir, 'e2e'),
  );
  expect(testMatch).toBe('**/*.pw.ts');

  // This spec must itself be inside the collected set.
  expect(thisFile.endsWith('.pw.ts')).toBe(true);
  expect(path.dirname(thisFile)).toBe(path.join(frontendDir, 'e2e'));
});
