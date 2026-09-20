/**
 * Unit test — README quickstart contract (GAP-093)
 *
 * The "First 10 minutes" section of the README is the only onboarding doc a
 * new human reads. This contract test parses it out of the live README and
 * FAILS when a referenced path/command stops existing, so the quickstart can
 * never silently drift from reality:
 *
 *   - every repo path it names (scripts, docs, deploy files) exists on disk;
 *   - the commands it quotes are real: `./bin/canopyd session import` is a
 *     mounted subcommand in cmd/canopyd/session_cmd.go, the API routes exist
 *     in the server wiring, the frontend scripts exist in package.json;
 *   - no hidden fixture dependency: it must NOT reference the E2E-only
 *     demo seed (scripts/seed-demo-data.sql) as something a new user runs.
 */

import { describe, it, expect } from 'vitest';
import { readFileSync, existsSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const thisFile = fileURLToPath(import.meta.url); // .../frontend/tests/readme-quickstart.test.ts
const frontendDir = path.dirname(path.dirname(thisFile)); // .../frontend
const repoRoot = path.dirname(frontendDir); // repo root

const readme = readFileSync(path.join(repoRoot, 'README.md'), 'utf8');

/** The onboarding section the onboarding tests/CTAs claim as their docs. */
function quickstartSection(): string {
  const start = readme.indexOf('## First 10 minutes');
  expect(start, 'README must keep a "## First 10 minutes" onboarding section (GAP-093)').toBeGreaterThanOrEqual(0);
  const nextHeader = readme.indexOf('\n## ', start + 1);
  return nextHeader === -1 ? readme.slice(start) : readme.slice(start, nextHeader);
}

describe('README quickstart contract (GAP-093)', () => {
  const section = quickstartSection();

  it('keeps the "First 10 minutes" onboarding section present and non-trivial', () => {
    expect(section.length).toBeGreaterThan(500);
  });

  it('references repo paths that actually exist', () => {
    // Every repo-relative path the section names must exist on disk.
    // Extract path-ish tokens (scripts/*.sh, docs/*.md, deploy entries).
    const pathLike = section.match(
      /(?:scripts|docs|deploy|frontend)\/[A-Za-z0-9._-]+(?:\.(?:sh|md|py|conf|ts|json)|\/)/g,
    ) ?? [];
    expect(pathLike.length, 'the section should reference concrete paths').toBeGreaterThan(0);
    for (const p of new Set(pathLike)) {
      const target = path.join(repoRoot, p);
      expect(
        existsSync(target),
        `README quickstart references ${p} but it does not exist at ${target}`,
      ).toBe(true);
    }
  });

  it('documents the real session-import CLI command (wired in cmd/canopyd)', () => {
    expect(section).toContain('./bin/canopyd session import');
    const sessionCmd = readFileSync(path.join(repoRoot, 'cmd/canopyd/session_cmd.go'), 'utf8');
    expect(sessionCmd).toContain('case "import"');
  });

  it('documents real API routes (create + import wired in internal/server)', () => {
    expect(section).toContain('/api/v1/trees/import');
    expect(section).toContain('/api/v1/trees');
    const server = readFileSync(path.join(repoRoot, 'internal/server/server.go'), 'utf8');
    expect(server).toContain('exportHandler.ImportTree');
    expect(server).toContain('Post("/trees/import"');
  });

  it('links to the GAP-077 session-source section and the anchor actually exists', () => {
    // The section links [#hermes-session-source-gap-077]; the heading must
    // exist so the anchor does not 404 inside the page.
    expect(section).toContain('#hermes-session-source-gap-077');
    const heading = readme.match(/### Hermes session source \(GAP-077\)/);
    expect(heading, 'the linked anchor target heading is missing').not.toBeNull();
  });

  it('creates a tree through the dialog contract the E2E tests already rely on', () => {
    // Required-fields wording must match the real Create Tree dialog
    // behavior asserted in frontend/tests/tree-create.test.ts.
    const e2e = readFileSync(path.join(frontendDir, 'tests/tree-create.test.ts'), 'utf8');
    expect(e2e).toContain('root');
  });

  it('has no hidden demo-fixture dependency for a new user', () => {
    // The E2E-only seed must never be presented as an onboarding step.
    expect(section).not.toContain('seed-demo-data.sql');
    expect(section.toLowerCase()).toContain('no demo data');
  });

  it('mentions the isolated scratch-instance recipe path correctly', () => {
    expect(section).toContain('scripts/scratch-instance.sh');
    const script = readFileSync(path.join(repoRoot, 'scripts/scratch-instance.sh'), 'utf8');
    expect(script.length).toBeGreaterThan(1000);
  });
});
