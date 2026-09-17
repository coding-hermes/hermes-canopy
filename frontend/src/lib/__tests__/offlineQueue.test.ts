/**
 * Unit tests — offline-queue count reader (DF-HERMES-CANOPY-18)
 *
 * The defect: `specs/T1.4-offline-stack-research.md` promises a "N pending
 * changes" badge "sourced from IndexedDB count", and no page code read the
 * service worker's outbox — the reader did not exist.
 *
 * jsdom has NO IndexedDB, so a real-IDB test is not evidence (and adding
 * `fake-indexeddb` is out of scope). Every behavioural test below therefore
 * drives an injectable FAKE factory that models the real API's contract,
 * including the auto-commit rule:
 *
 *   - `transaction()` yields a transaction that is LIVE FOR ONE TASK; a
 *     microtask flips it dead. `count()` on a dead transaction answers with
 *     `InvalidStateError`, exactly as a real auto-committed transaction
 *     refuses. `autoCommitted` counts those refusals, so "issue the count in
 *     the same task as the transaction" is an ASSERTED property of the reader
 *     and not just a comment — an `await` inserted between the two would make
 *     the happy-path count fail.
 *   - `open()` and `count()` answer from their own `onsuccess`/`onerror` on
 *     the next microtask, so the reader's handler attachment is exercised
 *     rather than assumed.
 *
 * The last describe block pins the reader's store coordinates against the
 * service worker's own literals by reading `frontend/sw.ts` as TEXT: the two
 * files cannot import from each other, and a rename in `sw.ts` would
 * otherwise turn the badge into a permanently-0 (green, useless) indicator.
 */

import { describe, it, expect, vi } from 'vitest';
import {
  OFFLINE_QUEUE_DB_NAME,
  OFFLINE_QUEUE_DB_VERSION,
  OFFLINE_QUEUE_STORE_NAME,
  countPendingChanges,
  pendingChangesLabel,
} from '../offlineQueue.ts';

// ─── Fake IndexedDB ────────────────────────────────────────────────────

interface FakeQueueOptions {
  /** How many entries the fake store reports. */
  count?: number;
  /** Model a database whose store is gone (the worker's schema moved on). */
  missingStore?: boolean;
  /** Model an `open` that fails (e.g. a version the running worker bumped). */
  openError?: Error;
  /** Model a database that does not exist yet, so the open must upgrade. */
  freshDatabase?: boolean;
  /** Model a `count` request that fails. */
  countError?: Error;
}

interface FakeRequest {
  result: unknown;
  error: unknown;
  onsuccess: ((event?: unknown) => void) | null;
  onerror: ((event?: unknown) => void) | null;
  onupgradeneeded?: ((event?: unknown) => void) | null;
  onblocked?: ((event?: unknown) => void) | null;
}

interface FakeTransaction {
  objectStore: () => { count: () => FakeRequest };
  error: unknown;
  onabort: ((event?: unknown) => void) | null;
  abort: () => void;
}

/** A request whose handlers fire on the next microtask, as the real API does. */
function deferred(outcome: { result?: unknown; error?: unknown }): FakeRequest {
  const request: FakeRequest = {
    result: undefined,
    error: null,
    onsuccess: null,
    onerror: null,
  };
  queueMicrotask(() => {
    request.result = outcome.result;
    request.error = outcome.error ?? null;
    if (outcome.error) request.onerror?.();
    else request.onsuccess?.();
  });
  return request;
}

function fakeQueue(options: FakeQueueOptions = {}) {
  const entryCount = options.count ?? 0;
  const state = { autoCommitted: 0, countRequests: 0, upgrades: 0 };
  const openedWith: Array<{ name: string; version: number }> = [];

  // The reader only ever has ONE transaction outstanding, so one flag models
  // the auto-commit for the whole fake.
  let live = true;

  const close = vi.fn();
  const createObjectStore = vi.fn();

  const transaction = vi.fn((_name: string, _mode?: string): FakeTransaction => {
    if (options.missingStore) {
      throw new DOMException('The object store was not found', 'NotFoundError');
    }
    live = true;
    queueMicrotask(() => {
      live = false;
    });
    return {
      objectStore: () => ({
        count: () => {
          state.countRequests += 1;
          if (!live) {
            state.autoCommitted += 1;
            return deferred({
              error: new DOMException(
                'The transaction is not active',
                'InvalidStateError',
              ),
            });
          }
          if (options.countError) return deferred({ error: options.countError });
          return deferred({ result: entryCount });
        },
      }),
      error: null,
      onabort: null,
      abort: vi.fn(),
    };
  });

  const db = {
    // A store that is "there" only when neither of the two absent-store
    // scenarios is being modelled.
    objectStoreNames: {
      contains: () => !options.missingStore && !options.freshDatabase,
    },
    createObjectStore,
    close,
    transaction,
  };

  const open = vi.fn((name: string, version: number) => {
    openedWith.push({ name, version });
    const request: FakeRequest = {
      result: undefined,
      error: null,
      onsuccess: null,
      onerror: null,
      onupgradeneeded: null,
      onblocked: null,
    };
    // The real API exposes the (upgrading) connection as `result` BEFORE
    // `onupgradeneeded` fires, which is what the upgrade handler writes to.
    if (!options.openError) request.result = db;
    // Queued BEFORE the success microtask, so an upgrade is seen first.
    if (options.freshDatabase && !options.openError) {
      queueMicrotask(() => {
        state.upgrades += 1;
        request.onupgradeneeded?.();
      });
    }
    queueMicrotask(() => {
      if (options.openError) {
        request.error = options.openError;
        request.onerror?.();
        return;
      }
      request.onsuccess?.();
    });
    return request;
  });

  return {
    factory: { open } as unknown as IDBFactory,
    db,
    close,
    createObjectStore,
    openedWith,
    get autoCommitted() {
      return state.autoCommitted;
    },
    get countRequests() {
      return state.countRequests;
    },
    get upgrades() {
      return state.upgrades;
    },
    /** Build a transaction directly, to drive the fake's own contract. */
    startTransaction: () => transaction(OFFLINE_QUEUE_STORE_NAME, 'readonly'),
  };
}

// ─── The label ─────────────────────────────────────────────────────────

describe('pendingChangesLabel', () => {
  it('inflects the noun for the count', () => {
    expect(pendingChangesLabel(1)).toBe('1 pending change');
    expect(pendingChangesLabel(2)).toBe('2 pending changes');
    expect(pendingChangesLabel(37)).toBe('37 pending changes');
  });

  it('says nothing when nothing is queued', () => {
    expect(pendingChangesLabel(0)).toBeNull();
    expect(pendingChangesLabel(-1)).toBeNull();
  });

  it('says nothing for a non-finite count instead of "NaN pending changes"', () => {
    expect(pendingChangesLabel(NaN)).toBeNull();
    expect(pendingChangesLabel(Infinity)).toBeNull();
    expect(pendingChangesLabel(-Infinity)).toBeNull();
  });
});

// ─── The reader ────────────────────────────────────────────────────────

describe('countPendingChanges', () => {
  it('counts the worker’s store and reads it in one task', async () => {
    const fake = fakeQueue({ count: 3 });

    await expect(countPendingChanges({ indexedDB: fake.factory })).resolves.toBe(3);

    // It really opened the worker's database at the worker's version…
    expect(fake.openedWith).toEqual([
      { name: OFFLINE_QUEUE_DB_NAME, version: OFFLINE_QUEUE_DB_VERSION },
    ]);
    // …and read the worker's store, not some other one.
    expect(fake.db.transaction).toHaveBeenCalledWith(
      OFFLINE_QUEUE_STORE_NAME,
      'readonly',
    );
    // No refusal: the transaction was still live when the count was issued.
    expect(fake.autoCommitted).toBe(0);
    expect(fake.countRequests).toBe(1);
  });

  it('resolves 0 for an empty queue', async () => {
    const fake = fakeQueue({ count: 0 });
    await expect(countPendingChanges({ indexedDB: fake.factory })).resolves.toBe(0);
  });

  it('returns the real count for 1 and 3 entries', async () => {
    await expect(
      countPendingChanges({ indexedDB: fakeQueue({ count: 1 }).factory }),
    ).resolves.toBe(1);
    await expect(
      countPendingChanges({ indexedDB: fakeQueue({ count: 3 }).factory }),
    ).resolves.toBe(3);
  });

  it('closes the connection after a successful read (leak guard)', async () => {
    const fake = fakeQueue({ count: 2 });
    await countPendingChanges({ indexedDB: fake.factory });
    expect(fake.close).toHaveBeenCalledTimes(1);
  });

  it('resolves 0 when the store is missing, and still closes', async () => {
    const fake = fakeQueue({ missingStore: true });
    await expect(countPendingChanges({ indexedDB: fake.factory })).resolves.toBe(0);
    // The connection came back from a successful open, so it must be released
    // even though the read failed.
    expect(fake.close).toHaveBeenCalledTimes(1);
    expect(fake.countRequests).toBe(0);
  });

  it('resolves 0 when the count request fails, and still closes', async () => {
    const fake = fakeQueue({ countError: new Error('aborted') });
    await expect(countPendingChanges({ indexedDB: fake.factory })).resolves.toBe(0);
    expect(fake.close).toHaveBeenCalledTimes(1);
  });

  it('resolves 0 when the open fails', async () => {
    const fake = fakeQueue({ openError: new Error('VersionError') });
    await expect(countPendingChanges({ indexedDB: fake.factory })).resolves.toBe(0);
    // Nothing was handed back, so there is nothing to close.
    expect(fake.close).not.toHaveBeenCalled();
  });

  it('resolves 0 when the factory itself throws', async () => {
    const factory = {
      open: () => {
        throw new DOMException('denied', 'SecurityError');
      },
    } as unknown as IDBFactory;
    await expect(countPendingChanges({ indexedDB: factory })).resolves.toBe(0);
  });

  it('resolves 0 when no IndexedDB is available at all', async () => {
    // An explicit null means "none here" — never a fallback to the ambient one.
    await expect(countPendingChanges({ indexedDB: null })).resolves.toBe(0);

    // Premise for the default path below: jsdom defines no ambient IDB, so
    // the omitted-key fallback is exercised for real.
    expect('indexedDB' in globalThis).toBe(false);
    await expect(countPendingChanges()).resolves.toBe(0);
  });

  it('creates the worker’s store when the page boots before the worker', async () => {
    const fake = fakeQueue({ count: 0, freshDatabase: true });

    await expect(countPendingChanges({ indexedDB: fake.factory })).resolves.toBe(0);

    expect(fake.upgrades).toBe(1);
    // The store shape must be the worker's, or its own `add()` would throw.
    expect(fake.createObjectStore).toHaveBeenCalledWith(OFFLINE_QUEUE_STORE_NAME, {
      keyPath: 'id',
      autoIncrement: true,
    });
  });

  it('the fake really models auto-commit (premise for the same-task claim)', async () => {
    const fake = fakeQueue({ count: 3 });

    const tx = fake.startTransaction();
    // Let the auto-commit microtask run before the count is issued — the shape
    // an `await` between transaction and count would produce.
    await Promise.resolve();

    let refused = false;
    const request = tx.objectStore().count();
    request.onerror = () => {
      refused = true;
    };
    await Promise.resolve();

    expect(refused).toBe(true);
    expect(fake.autoCommitted).toBe(1);

    // A fresh reader run is unaffected: the live transaction answers.
    await expect(countPendingChanges({ indexedDB: fake.factory })).resolves.toBe(3);
    expect(fake.autoCommitted).toBe(1);
  });
});

// ─── Drift: the reader's coordinates vs the worker's ───────────────────

/**
 * `frontend/sw.ts` as raw text. `import.meta.glob` keeps this node-free (the
 * app tsconfig only loads `vite/client` types) and root-absolute, which is
 * how a file OUTSIDE `src/` is reached.
 */
const WORKER_SOURCES = import.meta.glob('/sw.ts', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;

/** Read a `const NAME = <string|number>` literal out of the worker source. */
function workerLiteral(source: string, constName: string): unknown {
  const text = new RegExp(`const ${constName} = '([^']*)'`).exec(source);
  if (text) return text[1];
  const numeric = new RegExp(`const ${constName} = (\\d+)`).exec(source);
  return numeric ? Number(numeric[1]) : undefined;
}

describe('the reader reads the worker’s actual store', () => {
  const source = WORKER_SOURCES['/sw.ts'] ?? '';

  it('read the worker source (guards against a vacuous pass)', () => {
    expect(source.length).toBeGreaterThan(1000);
    expect(source).toContain('replayQueuedRequests');
    expect(source).toContain('indexedDB.open(DB_NAME, DB_VERSION)');
  });

  it('agrees with the worker on the database name, version and store', () => {
    expect(workerLiteral(source, 'DB_NAME')).toBe(OFFLINE_QUEUE_DB_NAME);
    expect(workerLiteral(source, 'DB_VERSION')).toBe(OFFLINE_QUEUE_DB_VERSION);
    expect(workerLiteral(source, 'STORE_NAME')).toBe(OFFLINE_QUEUE_STORE_NAME);
  });

  it('agrees with the worker on the store’s shape', () => {
    // A keyPath/autoIncrement drift would make the page-side create (and the
    // worker's own add) fail, so both halves of the schema are pinned.
    expect(source).toContain(`createObjectStore(STORE_NAME, {`);
    expect(source).toContain("keyPath: 'id'");
    expect(source).toContain('autoIncrement: true');
  });
});
