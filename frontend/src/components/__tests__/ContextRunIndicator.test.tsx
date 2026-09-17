/**
 * Component tests — ContextRunIndicator (GAP-084)
 *
 * The AFTER side of the context-compiler promise: what a run that actually
 * happened was given. Two behaviours carry the feature and are pinned here:
 *
 *   1. A context-bearing run renders its window line + the compiler
 *      manifest it was run with.
 *   2. A CONTEXT-FREE run renders NOTHING — the four provenance fields are
 *      `omitempty` on the Go record, so their absence means "no compile
 *      happened", and an indicator here would fabricate an auditable call
 *      that never took place.
 *
 * Driven with React 19's `act` + `react-dom/client`, matching
 * `ContextManifestPanel.test.tsx` — the project has no
 * @testing-library dependency and adding one is out of scope.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import ContextRunIndicator from '../ContextRunIndicator.tsx';
import type { GatewayRun } from '../../lib/gatewayApi.ts';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

// ─── Fixtures ──────────────────────────────────────────────────────────

const NODE_A = '019fb0c2-cab0-70c5-a477-fa10f136e000';
const NODE_B = '019fb0c2-cad5-75b5-a291-2dde84047400';

/** A run that went out through the compiler, manifest and all. */
function contextRun(overrides: Partial<GatewayRun> = {}): GatewayRun {
  return {
    run_id: 'run_ctx',
    session_id: '',
    message: 'summarise this thread',
    model: 'hermes',
    status: 'completed',
    created_at: '2026-09-17T10:00:00Z',
    events: [],
    source_node_id: NODE_A,
    token_budget: 8000,
    context_tokens: 1240,
    manifest: {
      requestId: 'req-1',
      nodeId: NODE_A,
      compiledAt: '2026-09-17T10:00:00Z',
      tokenBudget: 8000,
      tokensUsed: 1240,
      ancestry: [
        {
          id: NODE_A,
          kind: 'node',
          title: 'Welcome to Hermes Canopy',
          tokenCount: 412,
          truncated: false,
        },
        {
          id: NODE_B,
          kind: 'node',
          title: 'Child 1: Architecture',
          tokenCount: 828,
          truncated: true,
        },
      ],
      references: null,
      cards: null,
      omittedCount: 3,
      omittedReason: 'budget',
      truncationMarkers: ['3 messages omitted'],
      warnings: ['5 references: context becoming unfocused'],
    },
    ...overrides,
  };
}

/** A run started on the raw-text path — no provenance fields at all. */
function rawRun(): GatewayRun {
  return {
    run_id: 'run_raw',
    session_id: '',
    message: 'hello',
    model: 'hermes',
    status: 'completed',
    created_at: '2026-09-17T10:00:00Z',
    events: [],
  };
}

// ─── Harness ───────────────────────────────────────────────────────────

let container: HTMLDivElement;
let root: Root;

function mount(run: GatewayRun | null) {
  act(() => {
    root.render(createElement(ContextRunIndicator, { run }));
  });
}

function q(selector: string): HTMLElement | null {
  return container.querySelector(selector);
}

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

// ─── Tests ─────────────────────────────────────────────────────────────

describe('ContextRunIndicator — context-bearing run', () => {
  it('renders the run’s context window line and its source node', () => {
    mount(contextRun());

    expect(q('[data-testid="context-run-window"]')?.textContent).toBe(
      'Context window: 1,240 / 8,000 tokens',
    );
    // Shortened via the shared shortNodeId helper (head + random tail).
    expect(q('[data-testid="context-run-source"]')?.textContent).toBe(
      '019fb0c2…e000',
    );
    expect(q('[data-testid="context-run-indicator"]')).not.toBeNull();
  });

  it('is collapsed by default and expands the manifest on click', () => {
    mount(contextRun());

    expect(q('[data-testid="context-run-detail"]')).toBeNull();
    expect(
      q('[data-testid="context-run-toggle"]')?.getAttribute('aria-expanded'),
    ).toBe('false');

    act(() => q('[data-testid="context-run-toggle"]')?.click());

    expect(
      q('[data-testid="context-run-toggle"]')?.getAttribute('aria-expanded'),
    ).toBe('true');
    expect(q('[data-testid="context-run-usage"]')?.textContent).toBe(
      '1,240 / 8,000 tokens',
    );

    const items = container.querySelectorAll('[data-testid="context-run-item"]');
    expect(items).toHaveLength(2);
    expect(items[0]?.textContent).toContain('Welcome to Hermes Canopy');
    expect(items[0]?.textContent).toContain('412');
    expect(items[1]?.textContent).toContain('Child 1: Architecture');

    // The omission is the whole point of the manifest — it must be visible.
    expect(q('[data-testid="context-run-omission"]')?.textContent).toBe(
      '3 items omitted (budget)',
    );
    expect(q('[data-testid="context-run-detail"]')?.textContent).toContain(
      '3 messages omitted',
    );
    expect(q('[data-testid="context-run-warnings"]')?.textContent).toContain(
      'context becoming unfocused',
    );
  });

  it('renders Go-null slices without crashing (a healthy fresh compile)', () => {
    mount(
      contextRun({
        context_tokens: 42,
        manifest: {
          requestId: 'req-2',
          nodeId: NODE_A,
          compiledAt: '2026-09-17T10:00:00Z',
          tokenBudget: 8000,
          tokensUsed: 42,
          ancestry: null,
          references: null,
          cards: null,
          truncationMarkers: null,
          warnings: null,
        },
      }),
    );

    act(() => q('[data-testid="context-run-toggle"]')?.click());

    expect(q('[data-testid="context-run-window"]')?.textContent).toBe(
      'Context window: 42 / 8,000 tokens',
    );
    expect(
      container.querySelectorAll('[data-testid="context-run-item"]'),
    ).toHaveLength(0);
    expect(q('[data-testid="context-run-detail"]')?.textContent).toContain(
      'Nothing compiled into this context',
    );
  });

  it('reports a degraded compile honestly instead of showing an empty list', () => {
    mount(
      contextRun({
        manifest: null,
        context_tokens: 0,
        token_budget: 8000,
      }),
    );

    // The window line still proves a compile was requested…
    expect(q('[data-testid="context-run-window"]')?.textContent).toBe(
      'Context window: 0 / 8,000 tokens',
    );

    act(() => q('[data-testid="context-run-toggle"]')?.click());

    // …and the missing manifest is stated, not silently omitted.
    expect(q('[data-testid="context-run-manifest-missing"]')).not.toBeNull();
    expect(
      container.querySelectorAll('[data-testid="context-run-item"]'),
    ).toHaveLength(0);
  });
});

describe('ContextRunIndicator — context-free run', () => {
  it('renders nothing for a run with no provenance fields', () => {
    mount(rawRun());

    expect(q('[data-testid="context-run-indicator"]')).toBeNull();
    expect(container.textContent).toBe('');
  });

  it('renders nothing when the fields are present but zero', () => {
    mount(rawRun());
    mount(contextRun({ context_tokens: 0, token_budget: 0 }));

    expect(q('[data-testid="context-run-indicator"]')).toBeNull();
  });

  it('renders nothing before a run is known', () => {
    mount(null);

    expect(q('[data-testid="context-run-indicator"]')).toBeNull();
  });
});
