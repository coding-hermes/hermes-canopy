/**
 * Unit tests — context manifest derivations (WIRE-002)
 *
 * These pin the two classes of bug that reach the screen through this
 * module: a Go `null` slice crashing the panel's `.map`, and a token
 * label that misreports what the compiler actually spent.
 */

import { describe, it, expect } from 'vitest';
import {
  DEFAULT_CONTEXT_BUDGET,
  budgetSeverity,
  budgetUsageRatio,
  contextErrorNote,
  contextRequestPath,
  formatTokenCount,
  formatTokenUsage,
  isCompilableNodeId,
  manifestItemTitle,
  normaliseManifest,
  omissionNote,
  type CompiledContext,
  type Manifest,
} from '../contextManifest';

const NODE_ID = '019fb0c2-cab0-70c5-a477-fa10f136e000';

function manifest(overrides: Partial<Manifest> = {}): Manifest {
  return {
    requestId: 'req-1',
    nodeId: NODE_ID,
    compiledAt: '2026-08-08T10:00:00Z',
    tokenBudget: 8000,
    tokensUsed: 1240,
    ancestry: [],
    references: [],
    cards: [],
    omittedCount: 0,
    omittedReason: '',
    truncationMarkers: [],
    warnings: [],
    ...overrides,
  };
}

// ─── Request shaping ───────────────────────────────────────────────────

describe('isCompilableNodeId', () => {
  it('accepts a UUID the backend can parse', () => {
    expect(isCompilableNodeId(NODE_ID)).toBe(true);
  });

  it('rejects nothing selected', () => {
    expect(isCompilableNodeId(null)).toBe(false);
    expect(isCompilableNodeId(undefined)).toBe(false);
    expect(isCompilableNodeId('')).toBe(false);
  });

  it('rejects a synthetic canvas id — parseNodeID would 400 on it', () => {
    // TreeCanvas ghost slots are prefixed affordances, never graph ids.
    expect(isCompilableNodeId('ghost:019fb0c2-cab0-70c5-a477-fa10f136e000')).toBe(
      false,
    );
    expect(isCompilableNodeId('not-a-uuid')).toBe(false);
  });
});

describe('contextRequestPath', () => {
  it('requests the budget explicitly', () => {
    expect(contextRequestPath(NODE_ID)).toBe(
      `/context/${NODE_ID}?budget=${DEFAULT_CONTEXT_BUDGET}`,
    );
  });

  it('honours a caller-supplied budget', () => {
    expect(contextRequestPath(NODE_ID, 2000)).toBe(
      `/context/${NODE_ID}?budget=2000`,
    );
  });

  it('falls back rather than sending a budget the handler would 400', () => {
    expect(contextRequestPath(NODE_ID, 0)).toContain(
      `budget=${DEFAULT_CONTEXT_BUDGET}`,
    );
    expect(contextRequestPath(NODE_ID, Number.NaN)).toContain(
      `budget=${DEFAULT_CONTEXT_BUDGET}`,
    );
  });
});

// ─── Wire normalisation ────────────────────────────────────────────────

describe('normaliseManifest', () => {
  it('reads a full payload', () => {
    const body: CompiledContext = {
      content: '--- node … ---',
      manifest: {
        requestId: 'req-9',
        nodeId: NODE_ID,
        compiledAt: '2026-08-08T10:00:00Z',
        tokenBudget: 8000,
        tokensUsed: 1240,
        ancestry: [
          {
            id: NODE_ID,
            kind: 'node',
            title: 'Welcome to Hermes Canopy',
            tokenCount: 412,
            truncated: false,
          },
        ],
        references: [
          { id: 'topic-1', kind: 'topic', title: 'architecture', tokenCount: 60, truncated: true },
        ],
        cards: [],
        omittedCount: 3,
        omittedReason: 'budget',
        truncationMarkers: ['3 messages omitted'],
        warnings: ['5 references: context becoming unfocused'],
      },
    };

    const m = normaliseManifest(body);
    expect(m).not.toBeNull();
    expect(m?.tokensUsed).toBe(1240);
    expect(m?.tokenBudget).toBe(8000);
    expect(m?.ancestry).toHaveLength(1);
    expect(m?.ancestry[0]?.title).toBe('Welcome to Hermes Canopy');
    expect(m?.references[0]?.truncated).toBe(true);
    expect(m?.omittedReason).toBe('budget');
    expect(m?.truncationMarkers).toEqual(['3 messages omitted']);
  });

  /*
   * The crash shape. Go marshals a nil slice as `null`, so a root node
   * with no ancestors and no warnings — the HEALTHY case — arrives with
   * four nulls. A component that maps over them straight off the wire
   * dies on the happy path.
   */
  it('turns Go nil slices into empty arrays', () => {
    const m = normaliseManifest({
      content: '',
      manifest: {
        requestId: 'req-1',
        nodeId: NODE_ID,
        compiledAt: '2026-08-08T10:00:00Z',
        tokenBudget: 8000,
        tokensUsed: 12,
        ancestry: null,
        references: null,
        cards: null,
        truncationMarkers: null,
        warnings: null,
      },
    });

    expect(m?.ancestry).toEqual([]);
    expect(m?.references).toEqual([]);
    expect(m?.cards).toEqual([]);
    expect(m?.truncationMarkers).toEqual([]);
    expect(m?.warnings).toEqual([]);
    expect(m?.omittedCount).toBe(0);
    expect(m?.omittedReason).toBe('');
  });

  it('returns null when there is no manifest to render', () => {
    expect(normaliseManifest(null)).toBeNull();
    expect(normaliseManifest(undefined)).toBeNull();
    expect(normaliseManifest({ content: 'x' })).toBeNull();
    expect(normaliseManifest({ content: 'x', manifest: null })).toBeNull();
  });

  it('defaults an unknown item kind rather than leaking it into a data attribute', () => {
    const m = normaliseManifest({
      manifest: {
        ancestry: [{ id: 'a', kind: 'wat', title: 't', tokenCount: 1 }],
      },
    });
    expect(m?.ancestry[0]?.kind).toBe('node');
  });
});

// ─── Budget phrasing ───────────────────────────────────────────────────

describe('token formatting', () => {
  it('groups thousands', () => {
    expect(formatTokenCount(1240)).toBe('1,240');
    expect(formatTokenCount(8000)).toBe('8,000');
    expect(formatTokenCount(0)).toBe('0');
  });

  it('never renders NaN', () => {
    expect(formatTokenCount(Number.NaN)).toBe('0');
  });

  it('renders the headline', () => {
    expect(formatTokenUsage(1240, 8000)).toBe('1,240 / 8,000 tokens');
  });
});

describe('budgetUsageRatio', () => {
  it('is the used fraction', () => {
    expect(budgetUsageRatio(2000, 8000)).toBe(0.25);
  });

  it('clamps an over-budget compile to a full meter', () => {
    expect(budgetUsageRatio(9000, 8000)).toBe(1);
  });

  it('does not divide by a zero budget', () => {
    expect(budgetUsageRatio(10, 0)).toBe(1);
    expect(Number.isFinite(budgetUsageRatio(10, 0))).toBe(true);
  });

  it('floors a negative/absent usage at empty', () => {
    expect(budgetUsageRatio(-5, 8000)).toBe(0);
  });
});

describe('budgetSeverity', () => {
  it('is ok well under budget', () => {
    expect(budgetSeverity(1240, 8000)).toBe('ok');
  });

  it('warns from 80% of the budget', () => {
    expect(budgetSeverity(6400, 8000)).toBe('warn');
    expect(budgetSeverity(6399, 8000)).toBe('ok');
  });

  // The compiler warns rather than failing when it overshoots, so the
  // UI has to be able to show an over-budget state.
  it('flags an over-budget compile', () => {
    expect(budgetSeverity(8001, 8000)).toBe('over');
  });
});

// ─── Item + omission phrasing ──────────────────────────────────────────

describe('manifestItemTitle', () => {
  it('uses the compiler content preview', () => {
    expect(
      manifestItemTitle({
        id: NODE_ID,
        kind: 'node',
        title: 'Child #3: DAG node',
        tokenCount: 10,
        truncated: false,
      }),
    ).toBe('Child #3: DAG node');
  });

  it('falls back to a distinguishing short id, not a blank row', () => {
    const label = manifestItemTitle({
      id: NODE_ID,
      kind: 'node',
      title: '   ',
      tokenCount: 10,
      truncated: false,
    });
    expect(label).toBe('019fb0c2…e000');
  });

  it('never renders an empty label', () => {
    expect(
      manifestItemTitle({ id: '', kind: 'node', title: '', tokenCount: 0, truncated: false }),
    ).toBe('Untitled');
  });
});

describe('omissionNote', () => {
  it('is silent when nothing was dropped', () => {
    expect(omissionNote(manifest())).toBeNull();
  });

  it('reports the count and the reason', () => {
    expect(
      omissionNote(manifest({ omittedCount: 3, omittedReason: 'budget' })),
    ).toBe('3 items omitted (budget)');
  });

  it('inflects a single omission', () => {
    expect(
      omissionNote(manifest({ omittedCount: 1, omittedReason: 'depth' })),
    ).toBe('1 item omitted (depth)');
  });

  it('drops the parenthetical when the compiler gave no reason', () => {
    expect(omissionNote(manifest({ omittedCount: 2 }))).toBe('2 items omitted');
  });
});

// ─── Failure phrasing ──────────────────────────────────────────────────

describe('contextErrorNote', () => {
  it('phrases NODE_NOT_FOUND', () => {
    expect(contextErrorNote('node not found')).toBe(
      'No compiled context for this node.',
    );
  });

  it('phrases SERVICE_UNAVAILABLE', () => {
    expect(contextErrorNote('database unavailable')).toBe(
      'Context service unavailable.',
    );
  });

  it('phrases INVALID_BUDGET', () => {
    expect(contextErrorNote('budget must be >= 1')).toBe(
      'Context budget rejected by the server.',
    );
  });

  it('has a generic fallback and never surfaces [object Object]', () => {
    expect(contextErrorNote('boom')).toBe('Context unavailable.');
    expect(contextErrorNote('')).toBe('Context unavailable.');
  });
});
