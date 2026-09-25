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
  MANIFEST_HASH_SHORT_LENGTH,
  MIN_CONTEXT_BUDGET,
  budgetCappedNote,
  budgetSeverity,
  budgetUsageRatio,
  contextBudgetCeiling,
  contextErrorNote,
  contextRequestPath,
  formatTokenCount,
  formatTokenUsage,
  isCompilableNodeId,
  manifestHashShort,
  manifestItemTitle,
  normaliseBudget,
  normaliseManifest,
  normaliseModelOptions,
  omissionNote,
  relevanceLabel,
  type CompiledContext,
  type Manifest,
} from '../contextManifest';

const NODE_ID = '019fb0c2-cab0-70c5-a477-fa10f136e000';

/** A realistic 64-hex digest (the API.md example value). */
const MANIFEST_HASH =
  '91a2e5d22c17e5870f61ea6e9d501da80c2ac2735d15d5f3b6efb87c8c92856f';

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
    retrieved: [],
    retrievalBudget: 0,
    summaryText: '',
    summaryTokenCount: 0,
    summarizedCount: 0,
    omittedCount: 0,
    omittedReason: '',
    truncationMarkers: [],
    warnings: [],
    manifestHash: '',
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
  /*
   * GAP-080 phase 2b — REWRITTEN, not deleted. The old contract pinned
   * `?budget=<DEFAULT_CONTEXT_BUDGET>` on every request, which made the
   * server's window-derived default unreachable from the product. The
   * absence of the parameter is now the feature: it is what "Auto" means.
   */
  it('sends NO budget parameter when none is requested (Auto)', () => {
    const path = contextRequestPath(NODE_ID);
    expect(path).toBe(`/context/${NODE_ID}`);
    expect(path).not.toContain('budget=');
  });

  it('honours a caller-supplied budget', () => {
    expect(contextRequestPath(NODE_ID, 2000)).toBe(
      `/context/${NODE_ID}?budget=2000`,
    );
  });

  it('names the model, URI-encoded', () => {
    expect(contextRequestPath(NODE_ID, undefined, 'big-model')).toBe(
      `/context/${NODE_ID}?model=big-model`,
    );
    expect(contextRequestPath(NODE_ID, undefined, 'vendor/model 2')).toBe(
      `/context/${NODE_ID}?model=vendor%2Fmodel%202`,
    );
  });

  it('keeps a stable parameter order: budget then model', () => {
    const path = contextRequestPath(NODE_ID, 4096, 'big-model');
    expect(path).toBe(`/context/${NODE_ID}?budget=4096&model=big-model`);
    expect(path.indexOf('budget=')).toBeLessThan(path.indexOf('model='));
  });

  /*
   * A value the handler would 400 (`?budget=0`, `?budget=NaN`) must not be
   * sent at all: omitting it asks the server to derive its own default,
   * which is strictly better than a request guaranteed to fail.
   */
  it('omits an unusable budget rather than sending one the handler would 400', () => {
    for (const unusable of [0, -1, Number.NaN, Number.POSITIVE_INFINITY]) {
      const path = contextRequestPath(NODE_ID, unusable);
      expect(path).toBe(`/context/${NODE_ID}`);
      expect(path).not.toContain('budget=');
    }
  });

  it('treats an absent, empty or blank model as no model at all', () => {
    for (const none of [undefined, null, '', '   ']) {
      const path = contextRequestPath(NODE_ID, undefined, none);
      expect(path).toBe(`/context/${NODE_ID}`);
      expect(path).not.toContain('model=');
    }
  });
});

describe('normaliseBudget', () => {
  it('keeps a usable budget, floored', () => {
    expect(normaliseBudget(2000)).toBe(2000);
    expect(normaliseBudget(2000.9)).toBe(2000);
    expect(normaliseBudget(1)).toBe(1);
  });

  it('answers null — "no parameter" — for Auto and for unusable values', () => {
    expect(normaliseBudget(undefined)).toBeNull();
    expect(normaliseBudget(null)).toBeNull();
    expect(normaliseBudget(0)).toBeNull();
    expect(normaliseBudget(-5)).toBeNull();
    expect(normaliseBudget(Number.NaN)).toBeNull();
  });
});

describe('normaliseModelOptions', () => {
  it('reads the models route envelope', () => {
    const options = normaliseModelOptions({
      models: [
        { id: 'big-model', context_window: 200000, desired_budget: 120000 },
        { id: 'small-model', context_window: 4096, desired_budget: 2457 },
      ],
      percent: 60,
      default_budget: 8000,
      source: 'window',
    });

    expect(options).toEqual([
      { id: 'big-model', contextWindow: 200000, desiredBudget: 120000 },
      { id: 'small-model', contextWindow: 4096, desiredBudget: 2457 },
    ]);
  });

  /*
   * The degraded paths. Each of these is a real wire state — the panel
   * mounts with whatever `/gateway/models` answers, including a 500, an
   * empty-but-non-null list, and (in the panel's own tests) a completely
   * unrelated body from a mocked fetch.
   */
  it('degrades to no options instead of throwing', () => {
    expect(normaliseModelOptions(null)).toEqual([]);
    expect(normaliseModelOptions(undefined)).toEqual([]);
    expect(normaliseModelOptions({})).toEqual([]);
    expect(normaliseModelOptions({ models: null })).toEqual([]);
    expect(normaliseModelOptions({ models: [] })).toEqual([]);
    expect(normaliseModelOptions({ content: 'not a catalog' })).toEqual([]);
    expect(normaliseModelOptions('nonsense')).toEqual([]);
  });

  it('drops entries with no usable id and de-duplicates the rest', () => {
    const options = normaliseModelOptions({
      models: [
        { id: 'big-model', context_window: 200000, desired_budget: 120000 },
        { id: '', context_window: 1, desired_budget: 1 },
        { id: '   ', context_window: 1, desired_budget: 1 },
        { id: null, context_window: 1, desired_budget: 1 },
        { id: ' big-model ', context_window: 200000, desired_budget: 120000 },
      ],
    });

    expect(options).toEqual([
      { id: 'big-model', contextWindow: 200000, desiredBudget: 120000 },
    ]);
  });
});

describe('contextBudgetCeiling', () => {
  it("is the selected model's desired budget", () => {
    expect(
      contextBudgetCeiling({ id: 'm', contextWindow: 200000, desiredBudget: 120000 }),
    ).toBe(120000);
  });

  it('is the backend explicit ceiling with no model selected', () => {
    expect(contextBudgetCeiling(undefined)).toBe(DEFAULT_CONTEXT_BUDGET * 10);
  });

  it('never drops below the slider minimum, whatever the model derives', () => {
    // A 50-token window at 1% derives a budget of 1 — a range whose max is
    // below its min is not a usable control.
    expect(
      contextBudgetCeiling({ id: 'tiny', contextWindow: 50, desiredBudget: 1 }),
    ).toBe(MIN_CONTEXT_BUDGET);
    expect(
      contextBudgetCeiling({ id: 'zero', contextWindow: 0, desiredBudget: 0 }),
    ).toBe(DEFAULT_CONTEXT_BUDGET * 10);
  });
});

describe('budgetCappedNote', () => {
  it('names both numbers when the server granted less than was asked', () => {
    expect(budgetCappedNote(8000, 4096)).toBe(
      'Server capped the request at 4,096 tokens (requested 8,000).',
    );
  });

  it('is silent when nothing was requested (Auto)', () => {
    expect(budgetCappedNote(null, 4096)).toBeNull();
  });

  it('is silent when the request was granted exactly', () => {
    expect(budgetCappedNote(8000, 8000)).toBeNull();
  });

  it('is silent when the server granted more than was asked', () => {
    expect(budgetCappedNote(2000, 8000)).toBeNull();
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

// ─── Manifest digest (GAP-080 phase 5a) ────────────────────────────────

describe('manifest hash', () => {
  it('uses a 64-character fixture (the digest the backend sends)', () => {
    expect(MANIFEST_HASH).toHaveLength(64);
    expect(MANIFEST_HASH).toMatch(/^[0-9a-f]{64}$/);
  });

  /*
   * The type round trip: a full wire body normalises to exactly the view
   * shape, so a field added to the interface without a line in
   * `normaliseManifest` fails here rather than surfacing as `undefined` in a
   * component.
   */
  it('normalises the whole manifest, digest included', () => {
    const m = normaliseManifest({
      content: '--- node … ---',
      manifest: {
        requestId: 'req-1',
        nodeId: NODE_ID,
        compiledAt: '2026-08-08T10:00:00Z',
        tokenBudget: 8000,
        tokensUsed: 1240,
        ancestry: null,
        references: null,
        cards: null,
        omittedCount: 0,
        omittedReason: '',
        truncationMarkers: null,
        warnings: null,
        manifestHash: MANIFEST_HASH,
      },
    });

    expect(m).toEqual(manifest({ manifestHash: MANIFEST_HASH }));
    expect(m?.manifestHash).toBe(MANIFEST_HASH);
  });

  /*
   * A manifest recorded before the field existed carries no digest, and the
   * normaliser must not invent one: an invented hash is a claim about a
   * payload nobody hashed.
   */
  it('degrades an absent digest to empty, never an invented one', () => {
    expect(normaliseManifest({ manifest: {} })?.manifestHash).toBe('');
    expect(
      normaliseManifest({ manifest: { manifestHash: null } })?.manifestHash,
    ).toBe('');
  });
});

describe('manifestHashShort', () => {
  it('shows the first 12 hex characters', () => {
    const short = manifestHashShort(MANIFEST_HASH);
    expect(short).toBe(MANIFEST_HASH.slice(0, 12));
    expect(short).toHaveLength(MANIFEST_HASH_SHORT_LENGTH);
  });

  /*
   * The components branch on `null` to render NOTHING. An empty chip or a
   * placeholder would read as a digest the reader can compare — and there is
   * nothing to compare.
   */
  it('returns null for an absent or blank digest', () => {
    expect(manifestHashShort('')).toBeNull();
    expect(manifestHashShort('   ')).toBeNull();
    expect(manifestHashShort(null)).toBeNull();
    expect(manifestHashShort(undefined)).toBeNull();
  });

  it('trims a stray-whitespace digest before shortening', () => {
    expect(manifestHashShort(`  ${MANIFEST_HASH}  `)).toBe(
      MANIFEST_HASH.slice(0, 12),
    );
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
        relevance: 0,
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
      relevance: 0,
    });
    expect(label).toBe('019fb0c2…e000');
  });

  it('never renders an empty label', () => {
    expect(
      manifestItemTitle({ id: '', kind: 'node', title: '', tokenCount: 0, truncated: false, relevance: 0 }),
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

// ─── Retrieved tier (GAP-080 phase 4b) ─────────────────────────────────

describe('relevanceLabel', () => {
  it('renders a higher-is-better 0..1 score as a whole-number percent', () => {
    expect(relevanceLabel(0.83)).toBe('83% relevance');
    expect(relevanceLabel(1)).toBe('100% relevance');
    expect(relevanceLabel(0)).toBe('0% relevance');
  });

  it('is null when no score was present — never a misleading 0%', () => {
    expect(relevanceLabel(null)).toBeNull();
    expect(relevanceLabel(undefined)).toBeNull();
    expect(relevanceLabel(Number.NaN)).toBeNull();
    expect(relevanceLabel(-0.5)).toBeNull();
  });
});

describe('normaliseManifest — retrieved tier', () => {
  it('carries retrieved_topic items and their relevance, preserving the kind', () => {
    const m = normaliseManifest({
      content: '--- retrieved topic … ---',
      manifest: {
        requestId: 'req-1',
        nodeId: NODE_ID,
        compiledAt: '2026-08-08T10:00:00Z',
        tokenBudget: 8000,
        tokensUsed: 1240,
        ancestry: null,
        references: null,
        cards: null,
        retrieved: [
          {
            id: 'topic-a',
            kind: 'retrieved_topic',
            title: 'architecture',
            tokenCount: 96,
            truncated: false,
            relevance: 0.91,
          },
        ],
        retrievalBudget: 960,
      },
    });

    expect(m?.retrieved).toHaveLength(1);
    expect(m?.retrieved[0]?.kind).toBe('retrieved_topic');
    expect(m?.retrieved[0]?.title).toBe('architecture');
    expect(m?.retrieved[0]?.tokenCount).toBe(96);
    // The exact backend field survives normalisation.
    expect(m?.retrieved[0]?.relevance).toBe(0.91);
    expect(m?.retrievalBudget).toBe(960);
  });

  it('normalises an absent retrieved tier to empty arrays / zero budget', () => {
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

    expect(m?.retrieved).toEqual([]);
    expect(m?.retrievalBudget).toBe(0);
  });

  it('treats a Go-null retrieved slice as an empty list (healthy disabled tier)', () => {
    const m = normaliseManifest({
      manifest: {
        ancestry: null,
        references: null,
        cards: null,
        retrieved: null,
      },
    });
    expect(m?.retrieved).toEqual([]);
    expect(m?.retrievalBudget).toBe(0);
  });

  it('carries the phase-3 summary fields when present', () => {
    const m = normaliseManifest({
      manifest: {
        summaryText: '--- summarized older messages (2) ---\n[node a] did the thing.',
        summaryTokenCount: 42,
        summarizedCount: 2,
      },
    });
    expect(m?.summaryText).toContain('summarized older messages (2)');
    expect(m?.summaryTokenCount).toBe(42);
    expect(m?.summarizedCount).toBe(2);
  });

  it('normalises an absent summary to empty string / zero counts', () => {
    const m = normaliseManifest({
      manifest: {
        ancestry: null,
        references: null,
        cards: null,
      },
    });
    expect(m?.summaryText).toBe('');
    expect(m?.summaryTokenCount).toBe(0);
    expect(m?.summarizedCount).toBe(0);
  });

  it('preserves a relevance value on any item it arrived on (display is gated to the retrieved tier)', () => {
    const m = normaliseManifest({
      manifest: {
        ancestry: [
          { id: 'a', kind: 'node', title: 't', tokenCount: 1, relevance: 0.5 },
        ],
      },
    });
    // The normaliser does not drop a field that was present; the panel only
    // renders a relevance badge for retrieved_topic items.
    expect(m?.ancestry[0]?.relevance).toBe(0.5);
  });

  it('tolerates an unknown kind without losing the rest of the retrieved tier (early-return default)', () => {
    const m = normaliseManifest({
      manifest: {
        retrieved: [
          { id: 'a', kind: 'retrieved_topic', title: 'real', tokenCount: 7 },
          { id: 'b', kind: 'wat-is-this', title: 'odd', tokenCount: 8 },
        ],
      },
    });
    expect(m?.retrieved).toHaveLength(2);
    expect(m?.retrieved[0]?.kind).toBe('retrieved_topic');
    expect(m?.retrieved[1]?.kind).toBe('node'); // unknown → default, item kept
    expect(m?.retrieved[1]?.tokenCount).toBe(8);
  });

  it('does not leak an unknown relevance on a malformed numeric into NaN', () => {
    const m = normaliseManifest({
      manifest: {
        retrieved: [
          // A non-number relevance on the wire — `toScore` floors it to 0.
          { id: 'a', kind: 'retrieved_topic', title: 't', tokenCount: 1, relevance: 'high' as unknown as number },
        ],
      },
    });
    expect(m?.retrieved[0]?.relevance).toBe(0);
  });
});
