/**
 * Component tests — ContextManifestPanel (WIRE-002)
 *
 * The derivations are pinned in `lib/__tests__/contextManifest.test.ts`;
 * this file pins the WIRING against a real DOM and a mocked `fetch`:
 * that the panel calls the endpoint the backend actually serves, renders
 * the manifest, survives the 404/503 paths without taking the tree view
 * down, and — the failure mode that produced the UI-02 renderer crash —
 * issues exactly ONE request per node and never renders a stale response
 * against the wrong node.
 *
 * Driven with React 19's `act` + `react-dom/client` rather than a testing
 * library, matching `hooks/__tests__/useShortcuts.test.ts`: the project
 * has no @testing-library dependency and adding one is out of scope.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import ContextManifestPanel from '../ContextManifestPanel.tsx';

// `act` needs this flag set in the test environment to flush renders
// correctly and stay quiet — the project has no global vitest setup file.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// ─── Fixtures ──────────────────────────────────────────────────────────

const NODE_A = '019fb0c2-cab0-70c5-a477-fa10f136e000';
const NODE_B = '019fb0c2-cad5-75b5-a291-2dde84047400';

/** A realistic 200 body, shaped exactly as internal/context marshals it. */
function compiledBody(overrides: Record<string, unknown> = {}) {
  return {
    content: '--- node … ---',
    manifest: {
      requestId: 'req-1',
      nodeId: NODE_A,
      compiledAt: '2026-08-08T10:00:00Z',
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

function mount(props: { nodeId: string | null; budget?: number }) {
  act(() => {
    root.render(createElement(ContextManifestPanel, props));
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
  fetchMock = vi.fn(() => Promise.resolve(okResponse(compiledBody())));
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

// ─── The request ───────────────────────────────────────────────────────

describe('ContextManifestPanel — request', () => {
  it('calls the endpoint the backend actually serves', async () => {
    mount({ nodeId: NODE_A });
    await settle();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(requestedUrls()[0]).toBe(`/api/v1/context/${NODE_A}?budget=8000`);
  });

  it('forwards a caller-supplied budget', async () => {
    mount({ nodeId: NODE_A, budget: 2000 });
    await settle();

    expect(requestedUrls()[0]).toContain('budget=2000');
  });

  it('renders nothing and fetches nothing without a selection', async () => {
    mount({ nodeId: null });
    await settle();

    expect(fetchMock).not.toHaveBeenCalled();
    expect(q('[data-testid="context-manifest-panel"]')).toBeNull();
  });

  /*
   * A locally-seeded demo node (`__canopySeedDemoTree` mints
   * `crypto.randomUUID()` ids that never reached Postgres) is a real
   * UUID, but a ghost slot is not — `parseNodeID` 400s on it, so the
   * click must not become a request at all.
   */
  it('does not request a synthetic canvas id', async () => {
    mount({ nodeId: `ghost:${NODE_A}` });
    await settle();

    expect(fetchMock).not.toHaveBeenCalled();
  });
});

// ─── The render ────────────────────────────────────────────────────────

describe('ContextManifestPanel — render', () => {
  it('renders the token budget headline', async () => {
    mount({ nodeId: NODE_A });
    await settle();

    expect(q('[data-testid="context-token-usage"]')?.textContent).toBe(
      '1,240 / 8,000 tokens',
    );
  });

  it('renders a budget meter with accessible bounds', async () => {
    mount({ nodeId: NODE_A });
    await settle();

    const meter = q('[data-testid="context-budget-meter"]');
    expect(meter).not.toBeNull();
    expect(meter?.getAttribute('role')).toBe('meter');
    expect(meter?.getAttribute('aria-valuenow')).toBe('1240');
    expect(meter?.getAttribute('aria-valuemax')).toBe('8000');
    expect(meter?.getAttribute('data-severity')).toBe('ok');
  });

  it('surfaces the warning count while collapsed', async () => {
    mount({ nodeId: NODE_A });
    await settle();

    expect(q('[data-testid="context-warning-count"]')?.textContent).toContain(
      '1 warning',
    );
  });

  it('is collapsed by default and expands on click', async () => {
    mount({ nodeId: NODE_A });
    await settle();

    expect(q('[data-testid="context-manifest-detail"]')).toBeNull();

    const toggle = q('[data-testid="context-manifest-toggle"]');
    expect(toggle?.getAttribute('aria-expanded')).toBe('false');
    act(() => {
      toggle?.click();
    });

    expect(q('[data-testid="context-manifest-detail"]')).not.toBeNull();
    expect(
      q('[data-testid="context-manifest-toggle"]')?.getAttribute('aria-expanded'),
    ).toBe('true');
  });

  it('renders the ancestry chain, omission note and truncation markers', async () => {
    mount({ nodeId: NODE_A });
    await settle();
    act(() => q('[data-testid="context-manifest-toggle"]')?.click());

    const items = container.querySelectorAll(
      '[data-testid="context-manifest-item"]',
    );
    expect(items).toHaveLength(2);
    expect(items[0]?.textContent).toContain('Welcome to Hermes Canopy');
    expect(items[0]?.textContent).toContain('412');
    expect(items[1]?.textContent).toContain('Child 1: Architecture');

    expect(q('[data-testid="context-omission-note"]')?.textContent).toBe(
      '3 items omitted (budget)',
    );
    expect(q('[data-testid="context-manifest-detail"]')?.textContent).toContain(
      '3 messages omitted',
    );
    expect(q('[data-testid="context-warnings"]')?.textContent).toContain(
      'context becoming unfocused',
    );
  });

  /*
   * The healthy root-node payload: Go marshals every empty slice as
   * `null`. Rendering it must not throw — this is the shape a brand-new
   * tree's first node returns.
   */
  it('renders a payload whose slices are all Go nulls', async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        okResponse({
          content: '',
          manifest: {
            requestId: 'req-2',
            nodeId: NODE_A,
            compiledAt: '2026-08-08T10:00:00Z',
            tokenBudget: 8000,
            tokensUsed: 42,
            ancestry: null,
            references: null,
            cards: null,
            truncationMarkers: null,
            warnings: null,
          },
        }),
      ),
    );

    mount({ nodeId: NODE_A });
    await settle();
    act(() => q('[data-testid="context-manifest-toggle"]')?.click());

    expect(q('[data-testid="context-token-usage"]')?.textContent).toBe(
      '42 / 8,000 tokens',
    );
    expect(
      container.querySelectorAll('[data-testid="context-manifest-item"]'),
    ).toHaveLength(0);
    expect(q('[data-testid="context-manifest-detail"]')?.textContent).toContain(
      'Nothing compiled into this context',
    );
    expect(q('[data-testid="context-warning-count"]')).toBeNull();
  });
});

// ─── Failure paths ─────────────────────────────────────────────────────

describe('ContextManifestPanel — failures', () => {
  it('shows a subtle note on 404 NODE_NOT_FOUND, not a crash', async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(errorResponse(404, 'NODE_NOT_FOUND', 'node not found')),
    );

    mount({ nodeId: NODE_A });
    await settle();

    expect(q('[data-testid="context-manifest-error"]')?.textContent).toBe(
      'No compiled context for this node.',
    );
    // The panel is still there — the tree view above it is unaffected.
    expect(q('[data-testid="context-manifest-panel"]')).not.toBeNull();
    expect(q('[data-testid="context-budget-meter"]')).toBeNull();
  });

  it('shows a subtle note on 503 SERVICE_UNAVAILABLE', async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        errorResponse(503, 'SERVICE_UNAVAILABLE', 'database unavailable'),
      ),
    );

    mount({ nodeId: NODE_A });
    await settle();

    expect(q('[data-testid="context-manifest-error"]')?.textContent).toBe(
      'Context service unavailable.',
    );
  });

  it('never renders [object Object] from the structured error body', async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(errorResponse(500, 'CONTEXT_COMPILE_ERROR', 'internal server error')),
    );

    mount({ nodeId: NODE_A });
    await settle();

    expect(container.textContent).not.toContain('[object Object]');
    expect(q('[data-testid="context-manifest-error"]')?.textContent).toBe(
      'Context unavailable.',
    );
  });

  it('survives a rejected fetch', async () => {
    fetchMock.mockImplementation(() => Promise.reject(new Error('network down')));

    mount({ nodeId: NODE_A });
    await settle();

    expect(q('[data-testid="context-manifest-error"]')).not.toBeNull();
  });
});

// ─── Selection churn (the UI-02 loop shape) ────────────────────────────

describe('ContextManifestPanel — selection changes', () => {
  it('fetches exactly once per node — a re-render is not a new request', async () => {
    mount({ nodeId: NODE_A });
    await settle();
    mount({ nodeId: NODE_A });
    mount({ nodeId: NODE_A });
    await settle();

    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('refetches when the selection moves to another node', async () => {
    mount({ nodeId: NODE_A });
    await settle();
    mount({ nodeId: NODE_B });
    await settle();

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(requestedUrls()[1]).toContain(NODE_B);
  });

  it('clears the panel when the selection is dropped', async () => {
    mount({ nodeId: NODE_A });
    await settle();
    expect(q('[data-testid="context-manifest-panel"]')).not.toBeNull();

    mount({ nodeId: null });
    await settle();
    expect(q('[data-testid="context-manifest-panel"]')).toBeNull();
  });

  /*
   * Out-of-order responses. The compiler walks ancestry against Postgres,
   * so a click-through of a big tree can easily have node A's reply land
   * AFTER node B's. Rendering A's manifest under B's selection would be a
   * silent correctness bug — the panel would confidently describe the
   * wrong node's context.
   */
  it('ignores a stale response that lands after the selection moved', async () => {
    const deferred: Array<(r: Response) => void> = [];
    fetchMock.mockImplementation(
      (url: string) =>
        new Promise<Response>((resolve) => {
          deferred.push(() =>
            resolve(
              okResponse(
                compiledBody(
                  String(url).includes(NODE_B)
                    ? { nodeId: NODE_B, tokensUsed: 77, ancestry: null }
                    : { nodeId: NODE_A, tokensUsed: 1240 },
                ),
              ),
            ),
          );
        }),
    );

    mount({ nodeId: NODE_A });
    mount({ nodeId: NODE_B });

    // Resolve B (current) first, then A (stale) — the out-of-order case.
    act(() => {
      deferred[1]?.(undefined as unknown as Response);
    });
    await settle();
    act(() => {
      deferred[0]?.(undefined as unknown as Response);
    });
    await settle();

    expect(q('[data-testid="context-token-usage"]')?.textContent).toBe(
      '77 / 8,000 tokens',
    );
  });

  it('aborts the in-flight request when the selection changes', async () => {
    const signals: AbortSignal[] = [];
    fetchMock.mockImplementation((_url: string, init?: RequestInit) => {
      if (init?.signal) signals.push(init.signal);
      return new Promise<Response>(() => {}); // never settles
    });

    mount({ nodeId: NODE_A });
    mount({ nodeId: NODE_B });
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

    mount({ nodeId: NODE_A });
    act(() => root.unmount());

    expect(signals[0]?.aborted).toBe(true);

    // Re-create so afterEach's unmount stays valid.
    root = createRoot(container);
  });
});
