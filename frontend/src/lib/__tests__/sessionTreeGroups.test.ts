/**
 * Unit tests — session-tree grouping helpers (UI-LIVE-001)
 *
 * Covers normalizeSource (hermes-webui taxonomy mirror), the
 * continuation merge, group ordering/counts, and search filtering.
 */

import { describe, it, expect } from 'vitest';
import {
  normalizeSource,
  groupSessionTrees,
  matchesSearch,
  buildTreeSections,
  countGroupedTrees,
  type SessionFields,
} from '../sessionTreeGroups';

// ─── Fixtures ──────────────────────────────────────────────────────────

function sessionTree(overrides: Partial<SessionFields> & { id: string }): SessionFields {
  return {
    title: `Session ${overrides.id}`,
    description: 'Imported Hermes session',
    ...overrides,
  };
}

function plainTree(id: string): SessionFields {
  return {
    id,
    title: `Workspace ${id}`,
    description: '',
  };
}

const CLI_TREE = sessionTree({ id: 't-cli', session_id: 's-cli', source: 'cli' });
const TELEGRAM_TREE = sessionTree({ id: 't-tg', session_id: 's-tg', source: 'telegram' });
const CRON_TREE = sessionTree({ id: 't-cron', session_id: 's-cron', source: 'cron' });

// ─── normalizeSource ───────────────────────────────────────────────────

describe('normalizeSource', () => {
  it('classifies local interactive clients as cli', () => {
    expect(normalizeSource('cli')).toBe('cli');
    expect(normalizeSource('tui')).toBe('cli');
    expect(normalizeSource('acp')).toBe('cli');
  });

  it('classifies messaging sources', () => {
    for (const raw of ['telegram', 'discord', 'slack', 'matrix', 'wecom_callback']) {
      expect(normalizeSource(raw)).toBe('messaging');
    }
  });

  it('maps singletons', () => {
    expect(normalizeSource('webui')).toBe('webui');
    expect(normalizeSource('cron')).toBe('cron');
    expect(normalizeSource('webhook')).toBe('webhook');
    expect(normalizeSource('kanban')).toBe('kanban');
    expect(normalizeSource('tool')).toBe('tool');
    expect(normalizeSource('api_server')).toBe('api');
  });

  it('normalizes case and whitespace before matching', () => {
    expect(normalizeSource('  Telegram ')).toBe('messaging');
    expect(normalizeSource('CRON')).toBe('cron');
  });

  it('lands empty / unknown / unrecognized in unknown', () => {
    expect(normalizeSource(null)).toBe('unknown');
    expect(normalizeSource(undefined)).toBe('unknown');
    expect(normalizeSource('')).toBe('unknown');
    expect(normalizeSource('unknown')).toBe('unknown');
    expect(normalizeSource('carrier-pigeon')).toBe('unknown');
  });
});

// ─── groupSessionTrees ────────────────────────────────────────────────

describe('groupSessionTrees', () => {
  it('groups session trees by normalized source, drops empty groups', () => {
    const { groups, ungrouped } = groupSessionTrees([CLI_TREE, TELEGRAM_TREE, CRON_TREE]);
    expect(groups.map((g) => [g.source, g.trees.length])).toEqual([
      ['cli', 1],
      ['cron', 1],
      ['messaging', 1],
    ]);
    expect(ungrouped).toEqual([]);
  });

  it('keeps SOURCE_GROUPS order regardless of input order', () => {
    const { groups } = groupSessionTrees([TELEGRAM_TREE, CRON_TREE, CLI_TREE]);
    expect(groups.map((g) => g.source)).toEqual(['cli', 'cron', 'messaging']);
  });

  it('merges continuations under their parent and hides them top-level', () => {
    const parent = CLI_TREE;
    const cont = sessionTree({
      id: 't-cont',
      session_id: 's-cont',
      parent_session_id: 's-cli',
      source: 'cli',
    });
    const { groups, ungrouped } = groupSessionTrees([cont, parent, plainTree('w1')]);

    expect(groups).toHaveLength(1);
    const node = groups[0].trees[0];
    expect(node.tree.id).toBe('t-cli');
    expect(node.continuations.map((c) => c.id)).toEqual(['t-cont']);

    // Continuation is not a second top-level entry; workspace tree intact.
    expect(groups[0].trees).toHaveLength(1);
    expect(ungrouped.map((t) => t.id)).toEqual(['w1']);
  });

  it('keeps orphan continuations visible at top level', () => {
    // Parent not in the list (e.g. pagination window).
    const orphanCont = sessionTree({
      id: 't-orphan',
      session_id: 's-orphan',
      parent_session_id: 's-not-loaded',
      source: 'cron',
    });
    const { groups, ungrouped } = groupSessionTrees([orphanCont]);
    expect(ungrouped).toEqual([]);
    expect(groups.map((g) => g.source)).toEqual(['cron']);
    expect(groups[0].trees[0].tree.id).toBe('t-orphan');
    expect(groups[0].trees[0].continuations).toEqual([]);
  });

  it('never lets a tree be its own continuation', () => {
    const selfRef = sessionTree({
      id: 't-self',
      session_id: 's-self',
      parent_session_id: 's-self',
    });
    const { groups } = groupSessionTrees([selfRef]);
    expect(groups[0].trees[0].tree.id).toBe('t-self');
    expect(groups[0].trees[0].continuations).toEqual([]);
  });

  it('routes trees without session metadata to ungrouped', () => {
    const w1 = plainTree('w1');
    const w2 = plainTree('w2');
    const { groups, ungrouped } = groupSessionTrees([CLI_TREE, w1, w2, TELEGRAM_TREE]);
    expect(ungrouped.map((t) => t.id)).toEqual(['w1', 'w2']); // order preserved
    expect(groups.map((g) => g.source)).toEqual(['cli', 'messaging']);
  });
});

// ─── search + sections ────────────────────────────────────────────────

describe('matchesSearch', () => {
  it('matches title and description case-insensitively', () => {
    expect(matchesSearch(CLI_TREE, 'SESSION')).toBe(true);
    expect(matchesSearch(HERMES_DESC_TREE, 'imported hermes')).toBe(true);
    expect(matchesSearch(CLI_TREE, 'zzz-not-there')).toBe(false);
  });

  it('empty query matches everything', () => {
    expect(matchesSearch(CLI_TREE, '')).toBe(true);
    expect(matchesSearch(CLI_TREE, '   ')).toBe(true);
  });
});

const HERMES_DESC_TREE = sessionTree({
  id: 't-desc',
  session_id: 's-desc',
  source: 'cli',
  title: 'Unrelated title',
  description: 'Imported Hermes session abc123 · model=x · started=z',
});

describe('buildTreeSections', () => {
  const ALL = [CLI_TREE, TELEGRAM_TREE, HERMES_DESC_TREE, CRON_TREE, plainTree('w1')];

  it('filters within groups and keeps groups intact', () => {
    const sections = buildTreeSections(ALL, 'hermes');
    expect(countGroupedTrees(sections)).toBe(4);
    const cliGroup = sections.groups.find((g) => g.source === 'cli');
    expect(cliGroup?.trees.map((n) => n.tree.id).sort()).toEqual(['t-cli', 't-desc']);
  });

  it('search that only hits ungrouped trees empties groups', () => {
    const sections = buildTreeSections(ALL, 'workspace');
    expect(sections.groups).toEqual([]);
    expect(sections.ungrouped.map((t) => t.id)).toEqual(['w1']);
  });

  it('no query returns everything grouped', () => {
    const sections = buildTreeSections(ALL, '');
    expect(countGroupedTrees(sections)).toBe(ALL.length);
  });
});
