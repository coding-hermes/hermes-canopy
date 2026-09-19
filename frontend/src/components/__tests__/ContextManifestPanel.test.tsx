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

/**
 * A realistic 200 body for `GET /api/v1/gateway/models` (GAP-080 phase 2b),
 * shaped exactly as internal/handler marshals it.
 */
const MODELS_BODY = {
  models: [
    { id: 'big-model', context_window: 200000, desired_budget: 120000 },
    { id: 'small-model', context_window: 4096, desired_budget: 2457 },
  ],
  percent: 60,
  default_budget: 8000,
  source: 'window',
};

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

function isModelsCall(url: string): boolean {
  return url.includes('/gateway/models');
}

/** Only the manifest requests — the panel also fetches the model catalog. */
function contextUrls(): string[] {
  return requestedUrls().filter((url) => url.includes('/context/'));
}

function modelsUrls(): string[] {
  return requestedUrls().filter(isModelsCall);
}

/** The most recent manifest request, or `''` when there is none. */
function lastContextUrl(): string {
  const urls = contextUrls();
  return urls.length > 0 ? urls[urls.length - 1] : '';
}

/**
 * Drive a controlled input the way a browser does.
 *
 * React's input value tracker records a plain `.value =` assignment, so the
 * resulting event looks like "no change" and `onChange` never fires; the
 * PROTOTYPE setter is the standard way around it. Selects emit `change`,
 * range inputs emit `input`+`change`.
 */
function changeValue(
  el: HTMLInputElement | HTMLSelectElement,
  value: string,
  events: string[],
): void {
  const proto =
    el instanceof HTMLSelectElement
      ? HTMLSelectElement.prototype
      : HTMLInputElement.prototype;
  const setter = Object.getOwnPropertyDescriptor(proto, 'value')?.set;
  act(() => {
    setter?.call(el, value);
    for (const type of events) {
      el.dispatchEvent(new Event(type, { bubbles: true }));
    }
  });
}

function selectModel(value: string): void {
  const select = q('[data-testid="context-model-select"]') as HTMLSelectElement;
  changeValue(select, value, ['change']);
}

function moveSlider(value: number): void {
  const slider = q('[data-testid="context-budget-slider"]') as HTMLInputElement;
  changeValue(slider, String(value), ['input', 'change']);
}

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  // Two request surfaces share `fetch`: the manifest and the model catalog.
  fetchMock = vi.fn((url: string) =>
    Promise.resolve(
      okResponse(isModelsCall(String(url)) ? MODELS_BODY : compiledBody()),
    ),
  );
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
  /*
   * GAP-080 phase 2b. This assertion used to lock the defect: the panel
   * pinned `?budget=8000` on every request, so the server's window-derived
   * default could never be observed from the product. It is REWRITTEN to
   * the new contract — the absence of `budget=` IS the feature (it is what
   * "Auto" means) — not deleted.
   */
  it('calls the endpoint the backend serves with NO budget parameter', async () => {
    mount({ nodeId: NODE_A });
    await settle();

    expect(contextUrls()).toHaveLength(1);
    expect(contextUrls()[0]).toBe(`/api/v1/context/${NODE_A}`);
    expect(contextUrls()[0]).not.toContain('budget=');
  });

  it('forwards a caller-supplied budget', async () => {
    mount({ nodeId: NODE_A, budget: 2000 });
    await settle();

    expect(contextUrls()[0]).toBe(`/api/v1/context/${NODE_A}?budget=2000`);
  });

  it('renders nothing and fetches no context without a selection', async () => {
    mount({ nodeId: null });
    await settle();

    expect(contextUrls()).toHaveLength(0);
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

    expect(contextUrls()).toHaveLength(0);
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

    expect(contextUrls()).toHaveLength(1);
  });

  it('refetches when the selection moves to another node', async () => {
    mount({ nodeId: NODE_A });
    await settle();
    mount({ nodeId: NODE_B });
    await settle();

    expect(contextUrls()).toHaveLength(2);
    expect(contextUrls()[1]).toContain(NODE_B);
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
    fetchMock.mockImplementation((url: string) => {
      // The catalog answers immediately; only the manifest calls are held,
      // so `deferred` indexes CONTEXT requests (0 = node A, 1 = node B).
      if (isModelsCall(String(url))) {
        return Promise.resolve(okResponse(MODELS_BODY)) as unknown as Promise<Response>;
      }
      return new Promise<Response>((resolve) => {
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
      });
    });

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

// ─── Retrieved tier (GAP-080 phase 4b) ─────────────────────────────────

describe('ContextManifestPanel — retrieved tier', () => {
  /** A body with a populated retrieved tier. */
  function retrievedResponse(overrides: Record<string, unknown>) {
    fetchMock.mockImplementation((url: string) =>
      Promise.resolve(
        okResponse(
          isModelsCall(String(url))
            ? MODELS_BODY
            : compiledBody({
                retrieved: [
                  {
                    id: 'topic-a',
                    kind: 'retrieved_topic',
                    title: 'architecture',
                    tokenCount: 96,
                    truncated: false,
                    relevance: 0.91,
                  },
                  {
                    id: 'topic-b',
                    kind: 'retrieved_topic',
                    title: 'retrieval-design',
                    tokenCount: 140,
                    truncated: false,
                    relevance: 0.74,
                  },
                ],
                retrievalBudget: 960,
                ...overrides,
              }),
        ),
      ),
    );
  }

  it('renders a labelled Retrieved section only when items are present', async () => {
    retrievedResponse({});

    mount({ nodeId: NODE_A });
    await settle();
    act(() => q('[data-testid="context-manifest-toggle"]')?.click());

    const section = q('[data-testid="context-section-retrieved"]');
    expect(section).not.toBeNull();
    expect(section?.textContent).toContain('Retrieved · 2');

    const items = container.querySelectorAll(
      '[data-testid="context-manifest-item"][data-kind="retrieved_topic"]',
    );
    expect(items).toHaveLength(2);
    expect(items[0]?.textContent).toContain('architecture');
    expect(items[0]?.textContent).toContain('96');
    expect(items[0]?.textContent).toContain('91% relevance');
    expect(items[1]?.textContent).toContain('140');
    expect(items[1]?.textContent).toContain('74% relevance');
  });

  it('shows the tier allocation when retrievalBudget is non-zero', async () => {
    retrievedResponse({});

    mount({ nodeId: NODE_A });
    await settle();
    act(() => q('[data-testid="context-manifest-toggle"]')?.click());

    const budget = q('[data-testid="context-section-retrieved-budget"]');
    expect(budget).not.toBeNull();
    expect(budget?.textContent).toContain('960');
  });

  it('renders NO Retrieved section for an absent tier (disabled)', async () => {
    // The default compiledBody has no `retrieved` key at all.
    mount({ nodeId: NODE_A });
    await settle();
    act(() => q('[data-testid="context-manifest-toggle"]')?.click());

    expect(q('[data-testid="context-section-retrieved"]')).toBeNull();
  });

  it('renders NO Retrieved section for an explicitly empty tier', async () => {
    fetchedRetrieved([]);
    mount({ nodeId: NODE_A });
    await settle();
    act(() => q('[data-testid="context-manifest-toggle"]')?.click());

    expect(q('[data-testid="context-section-retrieved"]')).toBeNull();
  });

  it('does not mislabel a zero retrievalBudget — no (0 allocated) badge', async () => {
    // Tier ran but returned nothing AND budget omitted on the wire.
    retrievedResponse({ retrieved: [], retrievalBudget: 0 });

    mount({ nodeId: NODE_A });
    await settle();
    act(() => q('[data-testid="context-manifest-toggle"]')?.click());

    expect(q('[data-testid="context-section-retrieved"]')).toBeNull();
  });

  /** Helper: respond with a specific retrieved array (and no budget). */
  function fetchedRetrieved(items: unknown[]) {
    fetchMock.mockImplementation((url: string) =>
      Promise.resolve(
        okResponse(
          isModelsCall(String(url))
            ? MODELS_BODY
            : compiledBody({ retrieved: items as Record<string, unknown>[] }),
        ),
      ),
    );
  }
});

// ─── The two request knobs (GAP-080 phase 2b) ──────────────────────────

describe('ContextManifestPanel — model choice', () => {
  it('offers the gateway models, Server default selected', async () => {
    mount({ nodeId: NODE_A });
    await settle();

    const select = q('[data-testid="context-model-select"]') as HTMLSelectElement;
    expect(select).not.toBeNull();
    expect(Array.from(select.options).map((o) => o.value)).toEqual([
      '',
      'big-model',
      'small-model',
    ]);
    expect(select.value).toBe('');
    expect(modelsUrls()).toHaveLength(1);
  });

  it('fetches the catalog once per mount, not once per render', async () => {
    mount({ nodeId: NODE_A });
    await settle();
    mount({ nodeId: NODE_A });
    mount({ nodeId: NODE_A });
    await settle();

    expect(modelsUrls()).toHaveLength(1);
  });

  it('requests the model it was given, leaving the budget to the server', async () => {
    mount({ nodeId: NODE_A });
    await settle();

    selectModel('big-model');
    await settle();

    expect(lastContextUrl()).toBe(`/api/v1/context/${NODE_A}?model=big-model`);
  });

  /*
   * The degraded path. Every failure of the catalog is the SAME failure: no
   * choices, no error state, no spinner — and the manifest still compiles,
   * because a panel that cannot list models can still show context.
   */
  it('degrades to Server default when the models route fails', async () => {
    fetchMock.mockImplementation((url: string) =>
      Promise.resolve(
        isModelsCall(String(url))
          ? errorResponse(503, 'SERVICE_UNAVAILABLE', 'database unavailable')
          : okResponse(compiledBody()),
      ),
    );

    mount({ nodeId: NODE_A });
    await settle();

    const select = q('[data-testid="context-model-select"]') as HTMLSelectElement;
    expect(Array.from(select.options).map((o) => o.value)).toEqual(['']);
    expect(select.value).toBe('');
    expect(lastContextUrl()).not.toContain('model=');
    expect(q('[data-testid="context-token-usage"]')?.textContent).toBe(
      '1,240 / 8,000 tokens',
    );
    expect(q('[data-testid="context-manifest-error"]')).toBeNull();
  });

  it('degrades to Server default when the models route answers junk', async () => {
    fetchMock.mockImplementation((url: string) =>
      Promise.resolve(
        okResponse(
          isModelsCall(String(url)) ? { models: null } : compiledBody(),
        ),
      ),
    );

    mount({ nodeId: NODE_A });
    await settle();

    const select = q('[data-testid="context-model-select"]') as HTMLSelectElement;
    expect(Array.from(select.options).map((o) => o.value)).toEqual(['']);
  });
});

describe('ContextManifestPanel — budget control', () => {
  it('exposes an accessible range: min, step, ceiling and the effective value', async () => {
    mount({ nodeId: NODE_A });
    await settle();

    const slider = q('[data-testid="context-budget-slider"]') as HTMLInputElement;
    expect(slider).not.toBeNull();
    expect(slider.type).toBe('range');
    expect(slider.getAttribute('aria-valuemin')).toBe('256');
    expect(slider.getAttribute('aria-valuemax')).toBe('80000');
    // In Auto the control follows the budget the server granted.
    expect(slider.getAttribute('aria-valuenow')).toBe('8000');
    expect(slider.step).toBe('256');
    expect(q('[data-testid="context-budget-value"]')?.textContent).toBe('8,000');
    expect(q('[data-testid="context-budget-mode"]')?.textContent).toBe('Auto');
  });

  it("sizes the slider from the selected model's desired budget", async () => {
    mount({ nodeId: NODE_A });
    await settle();

    selectModel('small-model');
    await settle();

    expect(
      q('[data-testid="context-budget-slider"]')?.getAttribute('aria-valuemax'),
    ).toBe('2457');
  });

  it('sends an explicit budget when the slider moves', async () => {
    mount({ nodeId: NODE_A });
    await settle();
    expect(contextUrls()).toHaveLength(1);
    expect(lastContextUrl()).not.toContain('budget=');

    moveSlider(4096);
    await settle();

    expect(contextUrls()).toHaveLength(2);
    expect(lastContextUrl()).toBe(`/api/v1/context/${NODE_A}?budget=4096`);
    expect(q('[data-testid="context-budget-mode"]')?.textContent).toBe('Custom');
  });

  it('returns to Auto — no budget parameter at all — on the Auto button', async () => {
    mount({ nodeId: NODE_A, budget: 2000 });
    await settle();
    expect(contextUrls()[0]).toBe(`/api/v1/context/${NODE_A}?budget=2000`);
    expect(q('[data-testid="context-budget-mode"]')?.textContent).toBe('Custom');

    const auto = q('[data-testid="context-budget-auto"]');
    expect(auto).not.toBeNull();
    act(() => auto?.click());
    await settle();

    expect(contextUrls()).toHaveLength(2);
    expect(lastContextUrl()).toBe(`/api/v1/context/${NODE_A}`);
    expect(lastContextUrl()).not.toContain('budget=');
  });

  it('combines an explicit budget with a chosen model in a stable order', async () => {
    mount({ nodeId: NODE_A, budget: 4096 });
    await settle();

    selectModel('small-model');
    await settle();

    expect(lastContextUrl()).toBe(
      `/api/v1/context/${NODE_A}?budget=4096&model=small-model`,
    );
  });
});

/*
 * The honesty requirement. The note is driven by the RESPONSE — the manifest's
 * tokenBudget — never by what the panel asked for: a control that displays the
 * requested number as if it were in force misreports what the model was sent.
 */
describe('ContextManifestPanel — capped requests', () => {
  function respond(manifestOverrides: Record<string, unknown>) {
    fetchMock.mockImplementation((url: string) =>
      Promise.resolve(
        okResponse(
          isModelsCall(String(url))
            ? MODELS_BODY
            : compiledBody(manifestOverrides),
        ),
      ),
    );
  }

  it('names both numbers when the server granted less than requested', async () => {
    respond({ tokenBudget: 4096 });

    mount({ nodeId: NODE_A, budget: 8000 });
    await settle();

    const note = q('[data-testid="context-budget-capped"]');
    expect(note).not.toBeNull();
    expect(note?.textContent).toContain('4,096');
    expect(note?.textContent).toContain('8,000');
    // The effective budget everywhere else is the SERVER's.
    expect(q('[data-testid="context-token-usage"]')?.textContent).toBe(
      '1,240 / 4,096 tokens',
    );
    expect(
      q('[data-testid="context-budget-meter"]')?.getAttribute('aria-valuemax'),
    ).toBe('4096');
  });

  it('is silent when the request was granted exactly', async () => {
    respond({ tokenBudget: 8000 });

    mount({ nodeId: NODE_A, budget: 8000 });
    await settle();

    expect(q('[data-testid="context-budget-capped"]')).toBeNull();
  });

  it('is silent for an Auto request, with no number to be capped below', async () => {
    respond({ tokenBudget: 4096 });

    mount({ nodeId: NODE_A });
    await settle();

    expect(lastContextUrl()).not.toContain('budget=');
    expect(q('[data-testid="context-budget-capped"]')).toBeNull();
    expect(q('[data-testid="context-token-usage"]')?.textContent).toBe(
      '1,240 / 4,096 tokens',
    );
  });

  it('is silent when the server granted more than was requested', async () => {
    respond({ tokenBudget: 120000 });

    mount({ nodeId: NODE_A, budget: 2000 });
    await settle();

    expect(q('[data-testid="context-budget-capped"]')).toBeNull();
  });
});

/*
 * GAP-080 phase 5a. The panel is the BEFORE side of the manifest-digest
 * comparison: it shows the digest of the payload the compiler WOULD send, and
 * the run indicator shows the digest of the manifest a run WAS given, in the
 * same short form — equal short forms are the visible proof that the preview
 * and the run describe the same content.
 *
 * The digest is visible while the panel is COLLAPSED: the comparison is made
 * at a glance or not at all.
 */
describe('ContextManifestPanel — manifest digest', () => {
  /** A realistic 64-hex digest, as internal/context emits it. */
  const HASH =
    '91a2e5d22c17e5870f61ea6e9d501da80c2ac2735d15d5f3b6efb87c8c92856f';
  const SHORT = HASH.slice(0, 12);

  function respond(manifestOverrides: Record<string, unknown>) {
    fetchMock.mockImplementation((url: string) =>
      Promise.resolve(
        okResponse(
          isModelsCall(String(url))
            ? MODELS_BODY
            : compiledBody(manifestOverrides),
        ),
      ),
    );
  }

  it('renders the short digest with the full value in title, while collapsed', async () => {
    respond({ manifestHash: HASH });

    mount({ nodeId: NODE_A });
    await settle();

    const chip = q('[data-testid="context-manifest-hash"]');
    expect(chip).not.toBeNull();
    expect(chip?.textContent).toBe(SHORT);
    // The full 64-char value is the copyable one (no click handler needed).
    expect(chip?.getAttribute('title')).toBe(HASH);
    // Collapsed by default — the digest is not behind a disclosure.
    expect(q('[data-testid="context-manifest-detail"]')).toBeNull();
  });

  it('renders nothing when the manifest carries no digest (an older record)', async () => {
    // The default body predates the field: no `manifestHash` key at all.
    mount({ nodeId: NODE_A });
    await settle();

    expect(q('[data-testid="context-manifest-panel"]')).not.toBeNull();
    expect(q('[data-testid="context-manifest-hash"]')).toBeNull();
  });

  it('renders nothing for an empty digest rather than an empty chip', async () => {
    respond({ manifestHash: '' });

    mount({ nodeId: NODE_A });
    await settle();

    expect(q('[data-testid="context-manifest-hash"]')).toBeNull();
  });

  /*
   * The point of the feature, driven end to end through the panel: the digest
   * a reader sees is the digest of the manifest the server sent, so it can be
   * compared against a run record's.
   */
  it('shows the digest of the manifest the response carried', async () => {
    respond({ manifestHash: HASH, tokensUsed: 999 });

    mount({ nodeId: NODE_A });
    await settle();

    expect(q('[data-testid="context-manifest-hash"]')?.textContent).toBe(
      '91a2e5d22c17',
    );
    expect(q('[data-testid="context-token-usage"]')?.textContent).toBe(
      '999 / 8,000 tokens',
    );
  });
});
