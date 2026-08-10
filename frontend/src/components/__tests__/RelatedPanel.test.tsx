/**
 * Component tests — RelatedPanel (UI-REL-001)
 *
 * Pins the WIRING against a real DOM and a mocked `fetch`: that the panel
 * calls the endpoint the backend actually serves (`GET /api/v1/trees/{id}`),
 * renders parent/children/chips/delegation goals from a related payload,
 * shows the compact empty state for ordinary trees (no `related` key),
 * survives the 404/503 paths without taking the tree view down, and —
 * the failure mode that produced the UI-02 renderer crash — issues exactly
 * ONE request per tree and never renders a stale response against the
 * wrong tree.
 *
 * Driven with React 19's `act` + `react-dom/client` rather than a testing
 * library, matching `components/__tests__/ContextManifestPanel.test.tsx`:
 * the project has no @testing-library dependency and adding one is out of
 * scope.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import RelatedPanel from '../RelatedPanel.tsx';

// `act` needs this flag set in the test environment to flush renders
// correctly and stay quiet — the project has no global vitest setup file.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// ─── Fixtures ──────────────────────────────────────────────────────────

const TREE_A = '019fb0c2-cab0-70c5-a477-fa10f136e000';
const TREE_B = '019fb0c2-cad5-75b5-a291-2dde84047400';
const TREE_C = '019fb0c2-cae0-75b5-a291-2dde84047401';

/** A realistic 200 body, shaped exactly as internal/service marshals it. */
function treeDetailBody(overrides: Record<string, unknown> = {}) {
  return {
    id: TREE_A,
    title: 'Import session 20260606_155331_5054b7f3',
    description: 'Imported Hermes session 20260606_155331_5054b7f3',
    owner_id: '00000000-0000-0000-0000-000000000001',
    owner_display_name: '',
    node_count: 1,
    member_count: 1,
    root_node_id: '019fe85d-a01f-7eac-92a7-d031a7d0ac00',
    created_at: '2026-08-09T21:11:17.790752Z',
    updated_at: '2026-08-09T21:11:17.790427Z',
    role: 'owner',
    ...overrides,
  };
}

/** A tree with the full association set (WIRE-006 shape). */
function relatedBody() {
  return treeDetailBody({
    related: {
      parent: { id: TREE_B, title: 'Parent session: planning' },
      children: [
        { id: TREE_C, title: 'Child 1: Architecture' },
        { id: '019fb0c2-cb00-75b5-a291-2dde84047402', title: 'Child 2: Wiring' },
      ],
      board_task: 'WIRE-006',
      project: 'hermes-canopy',
      commit_hash: 'a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0',
      delegation_goals: [
        { delegation_id: 'dlg-1', goal: 'Build the related panel' },
        { delegation_id: 'dlg-2', goal: 'Wire drill-down navigation' },
      ],
    },
  });
}

function okResponse(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    text: () => Promise.resolve(JSON.stringify(body)),
    json: () => Promise.resolve(body),
  } as Response;
}

function errorResponse(status: number, code: string, message: string): Response {
  const body = JSON.stringify({ error: { code, message } });
  return {
    ok: false,
    status,
    text: () => Promise.resolve(body),
    json: () => Promise.resolve(JSON.parse(body)),
  } as Response;
}

// ─── Harness ───────────────────────────────────────────────────────────

let container: HTMLDivElement;
let root: Root;
let fetchMock: ReturnType<typeof vi.fn>;
let navigateMock: ReturnType<
  typeof vi.fn<(treeId: string, title?: string) => void>
>;

function mount(props: {
  treeId: string | null;
  onNavigateToTree?: (treeId: string, title?: string) => void;
}) {
  act(() => {
    root.render(
      createElement(RelatedPanel, {
        treeId: props.treeId,
        onNavigateToTree: props.onNavigateToTree ?? navigateMock,
      }),
    );
  });
}

/** Flush the fetch microtask chain plus React's resulting commit. */
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

function requestedUrls(): string[] {
  return fetchMock.mock.calls.map((c) => String(c[0]));
}

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  fetchMock = vi.fn(() => Promise.resolve(okResponse(relatedBody())));
  navigateMock = vi.fn();
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

// ─── The request ───────────────────────────────────────────────────────

describe('RelatedPanel — request', () => {
  it('calls the endpoint the backend actually serves', async () => {
    mount({ treeId: TREE_A });
    await settle();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(requestedUrls()[0]).toBe(`/api/v1/trees/${TREE_A}`);
  });

  it('renders nothing and fetches nothing without a selection', async () => {
    mount({ treeId: null });
    await settle();

    expect(fetchMock).not.toHaveBeenCalled();
    expect(q('[data-testid="related-panel"]')).toBeNull();
  });
});

// ─── The render ────────────────────────────────────────────────────────

describe('RelatedPanel — render', () => {
  it('renders parent, children, chips and delegation goals', async () => {
    mount({ treeId: TREE_A });
    await settle();

    // Parent session
    expect(q('[data-testid="related-parent-section"]')?.textContent).toContain(
      'Parent session: planning',
    );
    // Children
    const children = container.querySelectorAll(
      '[data-testid="related-child-session"]',
    );
    expect(children).toHaveLength(2);
    expect(children[0]?.textContent).toContain('Child 1: Architecture');
    expect(children[1]?.textContent).toContain('Child 2: Wiring');
    // Chips
    expect(q('[data-testid="related-chip-board-task"]')?.textContent).toContain(
      'WIRE-006',
    );
    expect(q('[data-testid="related-chip-project"]')?.textContent).toContain(
      'hermes-canopy',
    );
    expect(q('[data-testid="related-chip-commit"]')?.textContent).toContain(
      'a1b2c3d4',
    );
    // Delegation goals
    const goals = container.querySelectorAll('[data-testid="related-goal"]');
    expect(goals).toHaveLength(2);
    expect(goals[0]?.textContent).toContain('dlg-1');
    expect(goals[0]?.textContent).toContain('Build the related panel');
    expect(goals[1]?.textContent).toContain('dlg-2');
    expect(goals[1]?.textContent).toContain('Wire drill-down navigation');
  });

  it('shows the association count in the header', async () => {
    mount({ treeId: TREE_A });
    await settle();

    // 1 parent + 2 children + 3 chips + 2 goals = 8
    expect(q('[data-testid="related-association-count"]')?.textContent).toBe(
      '8',
    );
  });

  it('is open by default and collapses on click', async () => {
    mount({ treeId: TREE_A });
    await settle();

    expect(q('[data-testid="related-panel-detail"]')).not.toBeNull();
    expect(
      q('[data-testid="related-panel-toggle"]')?.getAttribute('aria-expanded'),
    ).toBe('true');

    act(() => {
      q('[data-testid="related-panel-toggle"]')?.click();
    });

    expect(q('[data-testid="related-panel-detail"]')).toBeNull();
    expect(
      q('[data-testid="related-panel-toggle"]')?.getAttribute('aria-expanded'),
    ).toBe('false');
  });

  it('renders the compact empty state when the tree has no related key', async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(okResponse(treeDetailBody())),
    );

    mount({ treeId: TREE_A });
    await settle();

    expect(q('[data-testid="related-empty"]')?.textContent).toContain(
      'No associations',
    );
    expect(q('[data-testid="related-parent-section"]')).toBeNull();
    expect(q('[data-testid="related-chips-section"]')).toBeNull();
    expect(q('[data-testid="related-goals-section"]')).toBeNull();
  });

  it('renders the empty state for a related object with no resolvable refs', async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        okResponse(
          treeDetailBody({
            related: { parent: null, children: [], board_task: null },
          }),
        ),
      ),
    );

    mount({ treeId: TREE_A });
    await settle();

    expect(q('[data-testid="related-empty"]')?.textContent).toContain(
      'No associations',
    );
  });

  it('renders a partial payload (parent only) without crashing', async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        okResponse(
          treeDetailBody({
            related: { parent: { id: TREE_B, title: 'Parent session: planning' } },
          }),
        ),
      ),
    );

    mount({ treeId: TREE_A });
    await settle();

    expect(q('[data-testid="related-parent-section"]')).not.toBeNull();
    expect(q('[data-testid="related-children-section"]')).toBeNull();
    expect(q('[data-testid="related-chips-section"]')).toBeNull();
    expect(q('[data-testid="related-goals-section"]')).toBeNull();
    expect(q('[data-testid="related-empty"]')).toBeNull();
  });
});

// ─── Drill-down ────────────────────────────────────────────────────────

describe('RelatedPanel — drill-down', () => {
  it('navigates to the parent session on click', async () => {
    mount({ treeId: TREE_A });
    await settle();

    act(() => {
      q('[data-testid="related-parent-session"]')?.click();
    });

    expect(navigateMock).toHaveBeenCalledTimes(1);
    expect(navigateMock).toHaveBeenCalledWith(
      TREE_B,
      'Parent session: planning',
    );
  });

  it('navigates to a child session on click', async () => {
    mount({ treeId: TREE_A });
    await settle();

    const children = container.querySelectorAll(
      '[data-testid="related-child-session"]',
    );
    act(() => {
      (children[1] as HTMLButtonElement | null)?.click();
    });

    expect(navigateMock).toHaveBeenCalledTimes(1);
    expect(navigateMock).toHaveBeenCalledWith(
      '019fb0c2-cb00-75b5-a291-2dde84047402',
      'Child 2: Wiring',
    );
  });

  it('copies the board task chip to the clipboard', async () => {
    const writeText = vi.fn(() => Promise.resolve());
    Object.assign(navigator, { clipboard: { writeText } });

    mount({ treeId: TREE_A });
    await settle();

    act(() => {
      q('[data-testid="related-chip-board-task"]')?.click();
    });
    await settle();

    expect(writeText).toHaveBeenCalledWith('WIRE-006');
    expect(
      q('[data-testid="related-chip-board-task"]')?.getAttribute('aria-label'),
    ).toBe('board task copied');
  });
});

// ─── Failure paths ─────────────────────────────────────────────────────

describe('RelatedPanel — failures', () => {
  it('shows a subtle note on 404 TREE_NOT_FOUND, not a crash', async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(errorResponse(404, 'TREE_NOT_FOUND', 'tree not found')),
    );

    mount({ treeId: TREE_A });
    await settle();

    expect(q('[data-testid="related-panel-error"]')?.textContent).toBe(
      'tree not found',
    );
    // The panel is still there — the tree view above it is unaffected.
    expect(q('[data-testid="related-panel"]')).not.toBeNull();
    expect(q('[data-testid="related-empty"]')).toBeNull();
  });

  it('shows a subtle note on 503 SERVICE_UNAVAILABLE', async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        errorResponse(503, 'SERVICE_UNAVAILABLE', 'database unavailable'),
      ),
    );

    mount({ treeId: TREE_A });
    await settle();

    expect(q('[data-testid="related-panel-error"]')?.textContent).toBe(
      'database unavailable',
    );
  });

  it('never renders [object Object] from the structured error body', async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(errorResponse(500, 'INTERNAL', 'internal server error')),
    );

    mount({ treeId: TREE_A });
    await settle();

    expect(container.textContent).not.toContain('[object Object]');
    expect(q('[data-testid="related-panel-error"]')?.textContent).toBe(
      'internal server error',
    );
  });

  it('survives a rejected fetch', async () => {
    fetchMock.mockImplementation(() => Promise.reject(new Error('network down')));

    mount({ treeId: TREE_A });
    await settle();

    expect(q('[data-testid="related-panel-error"]')).not.toBeNull();
  });
});

// ─── Selection churn (the UI-02 loop shape) ────────────────────────────

describe('RelatedPanel — selection changes', () => {
  it('fetches exactly once per tree — a re-render is not a new request', async () => {
    mount({ treeId: TREE_A });
    await settle();
    mount({ treeId: TREE_A });
    mount({ treeId: TREE_A });
    await settle();

    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('refetches when the selection moves to another tree', async () => {
    mount({ treeId: TREE_A });
    await settle();
    mount({ treeId: TREE_B });
    await settle();

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(requestedUrls()[1]).toContain(TREE_B);
  });

  it('clears the panel when the selection is dropped', async () => {
    mount({ treeId: TREE_A });
    await settle();
    expect(q('[data-testid="related-panel"]')).not.toBeNull();

    mount({ treeId: null });
    await settle();
    expect(q('[data-testid="related-panel"]')).toBeNull();
  });

  /*
   * Out-of-order responses. The detail fetch walks session metadata
   * against Postgres, so a click-through of the tree list can easily
   * have tree A's reply land AFTER tree B's. Rendering A's associations
   * under B's selection would be a silent correctness bug — the panel
   * would confidently describe the wrong tree's lineage.
   */
  it('ignores a stale response that lands after the selection moved', async () => {
    const deferred: Array<(r: Response) => void> = [];
    fetchMock.mockImplementation(
      (url: string) =>
        new Promise<Response>((resolve) => {
          deferred.push(() =>
            resolve(
              okResponse(
                String(url).includes(TREE_B)
                  ? treeDetailBody({
                      id: TREE_B,
                      related: {
                        parent: { id: TREE_A, title: 'Parent A' },
                      },
                    })
                  : relatedBody(),
              ),
            ),
          );
        }),
    );

    mount({ treeId: TREE_A });
    mount({ treeId: TREE_B });

    // Resolve B (current) first, then A (stale) — the out-of-order case.
    act(() => {
      deferred[1]?.(undefined as unknown as Response);
    });
    await settle();
    act(() => {
      deferred[0]?.(undefined as unknown as Response);
    });
    await settle();

    expect(q('[data-testid="related-parent-section"]')?.textContent).toContain(
      'Parent A',
    );
    expect(q('[data-testid="related-children-section"]')).toBeNull();
  });

  it('aborts the in-flight request when the selection changes', async () => {
    const signals: AbortSignal[] = [];
    fetchMock.mockImplementation((_url: string, init?: RequestInit) => {
      if (init?.signal) signals.push(init.signal);
      return new Promise<Response>(() => {}); // never settles
    });

    mount({ treeId: TREE_A });
    mount({ treeId: TREE_B });
    await settle();

    expect(signals).toHaveLength(2);
    expect(signals[0]?.aborted).toBe(true);
    expect(signals[1]?.aborted).toBe(false);
  });

  it('aborts on unmount', async () => {
    const signals: AbortSignal[] = [];
    fetchMock.mockImplementation((_url: string, init?: RequestInit) => {
      if (init?.signal) signals.push(init.signal);
      return new Promise<Response>(() => {});
    });

    mount({ treeId: TREE_A });
    act(() => root.unmount());

    expect(signals[0]?.aborted).toBe(true);

    // Re-create so afterEach's unmount stays valid.
    root = createRoot(container);
  });
});
