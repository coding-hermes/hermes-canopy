/**
 * Component tests — ReferenceInspectorPanel (SPEC-PL-06 §7.3, §8.2, §9.3)
 *
 * The derivations are pinned in `components/__tests__/multiReference.test.ts`.
 * This file pins the WIRING against a real DOM and a mocked `fetch`:
 *
 *   - a non-reference selection renders nothing and issues no request;
 *   - a synthetic-merge selection renders the §8.2 marker, the nearest common
 *     ancestor and the source list GROUPED BY BRANCH ROOT;
 *   - every source is an ordinary focusable link (§7.1: the list must work
 *     without the graph canvas);
 *   - row hover reports the source for §7.2 highlight sync;
 *   - "View context" is what hits §9.3 — exactly once — and the truncation
 *     indicators / manifest hash arrive from that response, not from wishful
 *     local state.
 *
 * Driven with React 19's `act` + `react-dom/client` rather than a testing
 * library, matching `ContextManifestPanel.test.tsx`: the project has no
 * @testing-library dependency and adding one is out of scope.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import ReferenceInspectorPanel from '../ReferenceInspectorPanel.tsx';
import { normaliseMultiReferenceMetadata } from '../../lib/multiReference.ts';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// ─── Fixtures ──────────────────────────────────────────────────────────

const REPLY = '0191a8b2-7fff-7000-9000-000000000301';
const SRC_A = '0191a8b2-7fff-7000-9000-000000000101';
const SRC_B = '0191a8b2-7fff-7000-9000-000000000202';
const ANCESTOR = '0191a8b2-7fff-7000-9000-000000000001';
const BRANCH_LEFT = '0191a8b2-7fff-7000-9000-000000000010';
const BRANCH_RIGHT = '0191a8b2-7fff-7000-9000-000000000020';
const HASH = 'a'.repeat(64);

const MERGE_METADATA = {
  version: 1,
  primarySourceId: SRC_A,
  canonicalSourceIds: [SRC_A, SRC_B],
  isSyntheticMergePoint: true,
  branchSpan: {
    commonAncestorId: ANCESTOR,
    sourceBranches: [
      { sourceId: SRC_A, branchRootId: BRANCH_LEFT, distanceFromRoot: 4 },
      { sourceId: SRC_B, branchRootId: BRANCH_RIGHT, distanceFromRoot: 3 },
    ],
  },
  contextManifestHash: HASH,
  contextTokenBudget: 8192,
};

/** A realistic §9.3 200 body, shaped exactly as the service marshals it. */
function contextBody(overrides: Record<string, unknown> = {}) {
  return {
    node_id: REPLY,
    tree_id: '0191a8b2-7fff-7000-9000-000000000001',
    parent_mode: 'multi_reference',
    primary_source_id: SRC_A,
    context: {
      sources: [
        { source_label: 'R1', node_id: SRC_A, content: 'Left branch claim', truncated: false },
        { source_label: 'R2', node_id: SRC_B, content: 'Right branch evidence', truncated: true },
      ],
      is_synthetic_merge_point: true,
      branch_span: {
        common_ancestor_id: ANCESTOR,
        source_branches: [
          { source_id: SRC_A, branch_root_id: BRANCH_LEFT, distance_from_root: 4 },
          { source_id: SRC_B, branch_root_id: BRANCH_RIGHT, distance_from_root: 3 },
        ],
      },
      token_budget: 8192,
      tokens_used: 1460,
      manifest_hash: HASH,
      ...overrides,
    },
  };
}

function okResponse(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    text: () => Promise.resolve(JSON.stringify(body)),
    json: () => Promise.resolve(body),
  } as Response;
}

function resolveNode(id: string) {
  if (id === SRC_A) {
    return {
      authorId: 'u1',
      authorLabel: 'Lexi',
      createdAt: '2026-07-22T11:50:00Z',
      content: 'Left branch claim',
    };
  }
  if (id === SRC_B) {
    return {
      authorId: 'u2',
      authorLabel: 'Lexi',
      createdAt: '2026-07-22T11:55:00Z',
      content: 'Right branch evidence',
    };
  }
  return null;
}

// ─── Harness ───────────────────────────────────────────────────────────

let container: HTMLDivElement;
let root: Root;
let fetchMock: ReturnType<typeof vi.fn>;

async function settle(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

function q(selector: string): HTMLElement | null {
  return container.querySelector(selector);
}

function qa(selector: string): HTMLElement[] {
  return Array.from(container.querySelectorAll(selector)) as HTMLElement[];
}

function mount(props: Partial<Parameters<typeof ReferenceInspectorPanel>[0]> = {}) {
  const merged = {
    nodeId: REPLY,
    metadata: normaliseMultiReferenceMetadata(MERGE_METADATA),
    open: true,
    onOpenChange: vi.fn(),
    resolveNode,
    ...props,
  };
  act(() => {
    root.render(createElement(ReferenceInspectorPanel, merged as never));
  });
  return merged;
}

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  fetchMock = vi.fn(() => Promise.resolve(okResponse(contextBody())));
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

// ─── Rendering ─────────────────────────────────────────────────────────

describe('ReferenceInspectorPanel — rendering', () => {
  it('renders nothing for a node that is not a multi-reference reply', async () => {
    mount({ metadata: null });
    await settle();

    expect(container.textContent).toBe('');
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('shows the §7.1 badge and the §8.2 synthetic-merge marker', async () => {
    mount();
    await settle();

    expect(q('[data-testid="reference-inspector-badge"]')?.textContent).toBe('2 references');
    const marker = q('[data-testid="reference-merge-marker"]');
    expect(marker?.textContent).toContain('Context from 2 branches');
    expect(q('[data-testid="reference-common-ancestor"]')?.textContent).toContain(ANCESTOR.slice(-3));
  });

  it('never labels the node merged/resolved/synthesis (§8.2)', async () => {
    mount();
    await settle();

    expect(container.textContent ?? '').not.toMatch(/resolved|merged|synthesis/i);
  });

  it('groups the source list by branch root', async () => {
    mount();
    await settle();

    const groups = qa('[data-testid="reference-branch-group"]');
    expect(groups).toHaveLength(2);
    expect(groups.map((g) => g.dataset.branchRoot)).toEqual([BRANCH_LEFT, BRANCH_RIGHT]);
    expect(qa('[data-testid="reference-source-row"]').map((r) => r.dataset.referenceLabel)).toEqual([
      'R1',
      'R2',
    ]);
  });

  it('exposes every source as an ordinary focusable link', async () => {
    mount();
    await settle();

    const links = qa('[data-testid="reference-source-link"]') as HTMLAnchorElement[];
    expect(links).toHaveLength(2);
    expect(links[0].getAttribute('href')).toBe(`?node=${SRC_A}`);
    expect(links[1].getAttribute('href')).toBe(`?node=${SRC_B}`);
    // Real <a> elements: keyboard reachable, no JS-only affordance.
    expect(links.every((l) => l.tagName === 'A')).toBe(true);
  });

  it('prints author, timestamp, branch root and preview per source', async () => {
    mount();
    await settle();

    const first = qa('[data-testid="reference-source-row"]')[0];
    expect(first.querySelector('[data-testid="reference-source-author"]')?.textContent).toBe('Lexi');
    expect(first.querySelector('time')?.getAttribute('datetime')).toBe('2026-07-22T11:50:00Z');
    expect(first.querySelector('[data-testid="reference-branch-root"]')?.textContent).toContain(
      '…0010',
    );
    expect(first.querySelector('[data-testid="reference-source-link"]')?.textContent).toBe(
      'Left branch claim',
    );
  });

  it('carries the §7.2 accessibility sentence', async () => {
    mount();
    await settle();

    expect(container.textContent).toContain(
      'Multi-reference reply to 2 messages: R1 Left branch claim, R2 Right branch evidence.',
    );
  });

  it('uses the persisted §5.2 color_key from the reply reference edges', async () => {
    // §5.2 lives on the EDGE — the panel takes the server-computed key from
    // the persisted edges the replica hydrated (§7.1), not from a guess.
    mount({
      edgeMeta: (id: string) =>
        id === SRC_A ? { colorKey: 'ref-6', sourceLabel: 'R1' } : { colorKey: 'ref-1', sourceLabel: 'R2' },
    });
    await settle();

    const rows = qa('[data-testid="reference-source-row"]');
    expect(rows.map((r) => r.dataset.colorKey)).toEqual(['ref-6', 'ref-1']);
    expect(rows.map((r) => r.dataset.referenceLabel)).toEqual(['R1', 'R2']);
  });

  it('falls back to a distinct per-source palette entry when no key is in hand yet', async () => {
    mount();
    await settle();

    const rows = qa('[data-testid="reference-source-row"]');
    // No wire payload and no edge metadata: index-derived, still distinct.
    expect(rows.map((r) => r.dataset.colorKey)).toEqual(['ref-0', 'ref-1']);
  });
});

// ─── Hover sync (§7.2) ─────────────────────────────────────────────────

describe('ReferenceInspectorPanel — hover sync', () => {
  it('reports the source under the pointer and clears on leave', async () => {
    const onHighlightChange = vi.fn();
    mount({ onHighlightChange });
    await settle();

    const row = qa('[data-testid="reference-source-row"]')[1];
    act(() => {
      row.dispatchEvent(new MouseEvent('mouseover', { bubbles: true }));
    });
    expect(onHighlightChange).toHaveBeenCalledWith(SRC_B);

    act(() => {
      row.dispatchEvent(new MouseEvent('mouseout', { bubbles: true, relatedTarget: container }));
    });
    expect(onHighlightChange).toHaveBeenLastCalledWith(null);
  });

  it('marks the row the canvas reported as highlighted', async () => {
    mount({ highlightedNodeId: SRC_A });
    await settle();

    const rows = qa('[data-testid="reference-source-row"]');
    expect(rows[0].dataset.highlighted).toBe('true');
    expect(rows[1].dataset.highlighted).toBe('false');
  });
});

// ─── §9.3 "View context" ───────────────────────────────────────────────

describe('ReferenceInspectorPanel — View context', () => {
  it('does not call §9.3 until the action is used', async () => {
    mount();
    await settle();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(q('[data-testid="reference-manifest-hash"]')?.textContent).toContain(
      HASH.slice(0, 6),
    );
  });

  it('fetches the provenance endpoint exactly once and renders its answer', async () => {
    mount();
    await settle();

    act(() => {
      q('[data-testid="reference-view-context"]')?.click();
    });
    await settle();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(String(fetchMock.mock.calls[0][0])).toBe(
      `/api/v1/nodes/${REPLY}/reference-context`,
    );

    // Token allocation + truncation come from the response.
    expect(q('[data-testid="reference-token-allocation"]')?.textContent).toBe(
      '1,460 / 8,192 tokens',
    );
    expect(q('[data-testid="reference-truncation-note"]')?.textContent).toContain(
      '1 source truncated',
    );
    expect(qa('[data-testid="reference-truncated"]')).toHaveLength(1);
  });

  it('survives a failed read without taking the panel down', async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve({
        ok: false,
        status: 404,
        text: () =>
          Promise.resolve(
            JSON.stringify({ error: { code: 'REFERENCE_CONTEXT_NOT_FOUND', message: 'not found' } }),
          ),
        json: () => Promise.resolve({}),
      } as Response),
    );

    mount();
    await settle();
    act(() => {
      q('[data-testid="reference-view-context"]')?.click();
    });
    await settle();

    expect(q('[data-testid="reference-context-error"]')).not.toBeNull();
    // The source list is still there — the panel describes the reply from
    // its own metadata.
    expect(qa('[data-testid="reference-source-row"]')).toHaveLength(2);
  });

  it('reports a source that changed since creation', async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        okResponse({ ...contextBody(), source_changed_since_creation: true }),
      ),
    );

    mount();
    await settle();
    act(() => {
      q('[data-testid="reference-view-context"]')?.click();
    });
    await settle();

    expect(q('[data-testid="reference-source-changed"]')).not.toBeNull();
  });
});
