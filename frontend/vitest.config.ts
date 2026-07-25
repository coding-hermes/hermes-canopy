/**
 * Vitest configuration for Hermes Canopy unit tests.
 *
 * Unit tests live under src/ (e.g., src/__tests__/) and run with jsdom.
 * Integration tests use a separate config (vitest.integration.config.ts).
 *
 * To run:  npm test        (unit tests)
 *          npm run test:watch     (watch mode)
 */
import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    include: ['src/**/*.test.ts', 'src/**/*.test.tsx'],
    exclude: ['node_modules/**', 'tests/**'],
    environment: 'jsdom',
    globals: true,
  },
});
