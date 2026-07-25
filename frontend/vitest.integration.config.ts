/**
 * Vitest configuration for Hermes Canopy integration tests.
 *
 * These tests use Playwright for browser automation and run against the
 * Vite dev server (http://localhost:5173).
 *
 * To run:  npm run test:integration
 */
import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    /** Only pick up integration test files under tests/ */
    include: ['tests/**/*.test.ts'],

    /** Exclude unit tests under src/ */
    exclude: ['src/**/*.test.ts', 'src/**/*.test.tsx', 'node_modules/**'],

    /** Browser tests need longer timeouts */
    testTimeout: 30_000,
    hookTimeout: 30_000,

    /** Run tests sequentially to avoid browser conflicts */
    fileParallelism: false,

    /** Retry once to handle flaky network/startup timing */
    retry: 1,
  },
});
