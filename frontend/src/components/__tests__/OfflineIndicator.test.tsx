/**
 * Component tests — OfflineIndicator pending-changes badge (DF-HERMES-CANOPY-18)
 *
 * `specs/T1.4-offline-stack-research.md` promises the user always sees a
 * "2 pending changes" badge. These tests pin the RENDERED TEXT, driven by the
 * real `online`/`offline` events the browser dispatches (never by poking
 * state), on both sides of the promise:
 *
 *   - offline with a non-empty queue   → "⚡ Offline mode — 2 pending changes"
 *   - reconnecting with a non-empty queue → the count stays up (the 2 s
 *     "back online" flash must not hide work that has not been replayed)
 *   - reconnecting with an empty queue → today's 2 s flash, then hidden
 *
 * Two ways the count is driven, deliberately:
 *   1. an injected `readPendingCount` seam, so every branch is exercised
 *      without a real IndexedDB (jsdom has none), and
 *   2. the DEFAULT prop — the production reader — over a fake `indexedDB`
 *      global, which is what proves the component is actually wired to
 *      `lib/offlineQueue.ts` and not merely to a test double.
 *
 * React 19's `act` + `react-dom/client` are used directly, matching
 * `ContextRunIndicator.test.tsx`: the project has no @testing-library
 * dependency and adding one is out of scope.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { OfflineIndicator, PENDING_POLL_INTERVAL_MS } from '../OfflineIndicator.tsx';
import type { OfflineIndicatorProps } from '../OfflineIndicator.tsx';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

// ─── Harness ───────────────────────────────────────────────────────────

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

/** Let the reader's promise chain and React's work settle. */
async function flush(): Promise<void> {
  await act(async () => {
    for (let i = 0; i < 20; i += 1) {
      await Promise.resolve();
    }
  });
}

async function renderIndicator(
  props: OfflineIndicatorProps = {},
): Promise<void> {
  await act(async () => {
    root.render(createElement(OfflineIndicator, props));
  });
  await flush();
}

/** Dispatch the event the browser actually fires, then settle. */
async function transition(event: 'online' | 'offline'): Promise<void> {
  await act(async () => {
    window.dispatchEvent(new Event(event));
  });
  await flush();
}

/**
 * A minimal stand-in for the worker's outbox: `entryCount` entries, with the
 * real API's handler-on-the-next-microtask timing. The auto-commit modelling
 * (and every failure path) is covered in `lib/__tests__/offlineQueue.test.ts`;
 * this one exists to prove the DEFAULT wiring reads a store.
 */
function fakeWorkerQueue(entryCount: number): IDBFactory {
  const request = (settle: () => { result?: unknown; error?: unknown }) => {
    const req = {
      result: undefined as unknown,
      error: null as unknown,
      onsuccess: null as (() => void) | null,
      onerror: null as (() => void) | null,
    };
    queueMicrotask(() => {
      const outcome = settle();
      req.result = outcome.result;
      req.error = outcome.error ?? null;
      req.onsuccess?.();
    });
    return req;
  };

  const db = {
    objectStoreNames: { contains: () => true },
    close: () => {},
    transaction: () => ({
      objectStore: () => ({
        count: () => request(() => ({ result: entryCount })),
      }),
      error: null,
      onabort: null,
    }),
  };

  return {
    open: () => request(() => ({ result: db })),
  } as unknown as IDBFactory;
}

// ─── Tests ─────────────────────────────────────────────────────────────

describe('OfflineIndicator — pending changes badge', () => {
  it('shows the worker’s count while offline, through the DEFAULT reader', async () => {
    // No props: the production path (component → lib/offlineQueue.ts → IDB).
    vi.stubGlobal('indexedDB', fakeWorkerQueue(2));

    await renderIndicator();
    expect(container.textContent).toBe(
      '✓ Back online — 2 pending changes still queued',
    );

    await transition('offline');
    expect(container.textContent).toBe('⚡ Offline mode — 2 pending changes');
  });

  it('inflects the singular for a single queued change', async () => {
    await renderIndicator({ readPendingCount: async () => 1 });

    await transition('offline');

    expect(container.textContent).toBe('⚡ Offline mode — 1 pending change');
  });

  it('says nothing about a count while the queue is empty', async () => {
    await renderIndicator({ readPendingCount: async () => 0 });
    // Online with an empty queue: nothing to report.
    expect(container.textContent).toBe('');

    await transition('offline');
    // Offline, with the worker inactive (jsdom registers none): no count line,
    // and no "changes saved locally" claim either.
    expect(container.textContent).toBe('⚡ Offline mode');
  });

  it('keeps a non-empty queue visible after reconnecting, until it drains', async () => {
    vi.useFakeTimers();
    let queued = 2;
    await renderIndicator({ readPendingCount: async () => queued });

    await transition('offline');
    expect(container.textContent).toBe('⚡ Offline mode — 2 pending changes');

    await transition('online');
    expect(container.textContent).toContain('2 pending changes');

    // Past the 2 s auto-hide: the flash is gone, the queue is not.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    await flush();
    expect(container.textContent).toBe(
      '✓ Back online — 2 pending changes still queued',
    );

    // The worker drains it — the banner goes away with the last entry.
    queued = 0;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(PENDING_POLL_INTERVAL_MS);
    });
    await flush();
    expect(container.textContent).toBe('');
  });

  it('keeps the 2 s auto-hide when reconnecting with an empty queue', async () => {
    vi.useFakeTimers();
    await renderIndicator({ readPendingCount: async () => 0 });

    await transition('offline');
    expect(container.textContent).toBe('⚡ Offline mode');

    await transition('online');
    expect(container.textContent).toBe('✓ Back online');

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    await flush();
    expect(container.textContent).toBe('');
  });

  it('re-reads on an interval while offline, and stops once unmounted', async () => {
    vi.useFakeTimers();
    const read = vi.fn(async () => 3);

    await renderIndicator({ readPendingCount: read });
    // Mount, plus the emptiness transition (0 → 3) that re-arms the polling.
    expect(read).toHaveBeenCalledTimes(2);

    await transition('offline');
    expect(read).toHaveBeenCalledTimes(3);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(PENDING_POLL_INTERVAL_MS);
    });
    await flush();
    expect(read).toHaveBeenCalledTimes(4);

    act(() => root.unmount());
    root = createRoot(container);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(PENDING_POLL_INTERVAL_MS * 3);
    });
    await flush();

    // The interval was cleared with the component: no reads after unmount.
    expect(read).toHaveBeenCalledTimes(4);
  });

  it('survives a reader that rejects without hiding the banner', async () => {
    await renderIndicator({
      readPendingCount: () => Promise.reject(new Error('idb denied')),
    });

    await transition('offline');

    // The count is unknown, so no number is invented — but going offline is
    // still reported.
    expect(container.textContent).toBe('⚡ Offline mode');
  });
});
