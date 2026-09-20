/**
 * Unit tests — parseTreeImportFile (GAP-093)
 *
 * Pure file-acceptance logic for the first-run onboarding importer. It is
 * the gate between a user's file and the live POST /api/v1/trees/import
 * route, so each rejection names the real problem and NOTHING malformed
 * reaches the API.
 */

import { describe, it, expect } from 'vitest';
import { parseTreeImportFile } from '../../components/FirstRunOnboarding';

describe('parseTreeImportFile', () => {
  it('accepts a well-formed export envelope', () => {
    const text = JSON.stringify({
      tree: { title: 'My Tree', description: '', rootNodeId: '019-.1' },
      nodes: [{ id: '019-.1', content: 'hi', nodeType: 'message' }],
      edges: [],
      version: 1,
    });
    const parsed = parseTreeImportFile(text);
    expect(parsed.ok).toBe(true);
    if (parsed.ok) {
      expect((parsed.data as { tree: { title: string } }).tree.title).toBe('My Tree');
      expect((parsed.data as { nodes: unknown[] }).nodes).toHaveLength(1);
    }
  });

  it('rejects non-JSON files with an invalid-JSON error', () => {
    const parsed = parseTreeImportFile('not json {{{');
    expect(parsed.ok).toBe(false);
    if (!parsed.ok) expect(parsed.error.toLowerCase()).toContain('json');
  });

  it('rejects JSON arrays', () => {
    const parsed = parseTreeImportFile('[]');
    expect(parsed.ok).toBe(false);
    if (!parsed.ok) expect(parsed.error).toContain('JSON object');
  });

  it('rejects a JSON object with no tree section', () => {
    const parsed = parseTreeImportFile(JSON.stringify({ nodes: [] }));
    expect(parsed.ok).toBe(false);
    if (!parsed.ok) expect(parsed.error).toContain("'tree' section");
  });

  it('rejects an export whose tree has no title', () => {
    const parsed = parseTreeImportFile(
      JSON.stringify({ tree: { rootNodeId: 'x' }, nodes: [{ id: 'x' }] }),
    );
    expect(parsed.ok).toBe(false);
    if (!parsed.ok) expect(parsed.error).toContain('title');
  });

  it('rejects an export with an empty title', () => {
    const parsed = parseTreeImportFile(
      JSON.stringify({ tree: { title: '   ' }, nodes: [{ id: 'x' }] }),
    );
    expect(parsed.ok).toBe(false);
    if (!parsed.ok) expect(parsed.error).toContain('title');
  });

  it('rejects an export with no nodes', () => {
    const parsed = parseTreeImportFile(
      JSON.stringify({ tree: { title: 'T' }, nodes: [] }),
    );
    expect(parsed.ok).toBe(false);
    if (!parsed.ok) expect(parsed.error).toContain('no messages');
  });

  it('rejects an export with a missing nodes array', () => {
    const parsed = parseTreeImportFile(JSON.stringify({ tree: { title: 'T' } }));
    expect(parsed.ok).toBe(false);
    if (!parsed.ok) expect(parsed.error).toContain('no messages');
  });
});
