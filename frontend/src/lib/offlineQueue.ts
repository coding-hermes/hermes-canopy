/**
 * Hermes Canopy — Offline-queue count reader (DF-HERMES-CANOPY-18)
 *
 * `specs/T1.4-offline-stack-research.md` promises the user "always sees
 * '2 pending changes' badge sourced from IndexedDB count". The queue itself
 * lives in the SERVICE WORKER (`frontend/sw.ts`): it owns the schema, queues
 * mutating requests while the device is offline and drains them every 60 s.
 * Nothing under `src/` ever read that store, so the promised badge had no
 * source at all.
 *
 * IndexedDB is same-origin, so a page-side module counts the very same store
 * the worker writes. This is that reader. It is deliberately small:
 *
 *   - `pendingChangesLabel` is pure — no DOM, no IndexedDB — and reuses
 *     `countLabel` so the count and its noun can never disagree (UI-08).
 *   - `countPendingChanges` takes an injectable `IDBFactory` (the
 *     injectable-factory pattern) because jsdom has no IndexedDB: a fake
 *     factory is the only way this path can be tested at all.
 *
 * Two IndexedDB rules are load-bearing here:
 *
 *   1. A transaction auto-commits as soon as the event loop turns with no
 *      pending request, and a dead transaction's `count()` never answers.
 *      So `store.count()` is issued IMMEDIATELY after `db.transaction(...)`,
 *      in the same task, and the value is taken from that request's
 *      `onsuccess`. Nothing is awaited in between.
 *   2. The connection is closed on every path. The page and the worker share
 *      the database, and a page-side connection left open would block the
 *      worker's next `DB_VERSION` bump behind a `blocked` event.
 *
 * Everything that can go wrong — no IndexedDB at all (jsdom, Safari private
 * mode), a refused open, a store that is not there, a version the worker has
 * since bumped — is not something the UI can act on, so every failure
 * resolves to 0. The badge is a courtesy; it must never throw into React's
 * render path, and it must never wedge waiting for an open that will not
 * settle.
 *
 * The sibling half of this subsystem (what the worker does with the entries
 * it holds) is policy-only and lives in `lib/offlineQueuePolicy.ts`.
 */

import { countLabel } from './pluralize.ts';

/**
 * Store coordinates. These MUST stay equal to the service worker's own
 * constants in `frontend/sw.ts` (`DB_NAME` / `DB_VERSION` / `STORE_NAME`) —
 * the worker is the schema OWNER, this module only reads what it writes.
 *
 * The two files cannot import from each other (`sw.ts` runs in the worker
 * global scope, `src` in the page), so the fact is written twice and pinned
 * by a drift test in `__tests__/offlineQueue.test.ts` that reads `sw.ts` as
 * text and fails if the literals ever disagree.
 */
export const OFFLINE_QUEUE_DB_NAME = 'canopy-offline-queue';
export const OFFLINE_QUEUE_DB_VERSION = 1;
export const OFFLINE_QUEUE_STORE_NAME = 'requests';

/**
 * `"1 pending change"` / `"2 pending changes"` — or `null` when there is
 * nothing to say, so callers branch on a single value instead of re-deriving
 * "is this count worth showing" from the number. Non-finite counts read as
 * nothing to say, not as "NaN pending changes".
 */
export function pendingChangesLabel(count: number): string | null {
  if (!Number.isFinite(count) || count <= 0) return null;
  return countLabel(count, 'pending change', 'pending changes');
}

/** Injection seam for the reader — `undefined` means "use the real one". */
export interface OfflineQueueReaderDeps {
  /**
   * The IndexedDB implementation to read through. `null` explicitly means
   * "none is available" (as on an insecure origin); an omitted key falls back
   * to the ambient `globalThis.indexedDB`.
   */
  indexedDB?: IDBFactory | null;
}

/** The ambient IndexedDB implementation, or `null` when the browser has none. */
function globalIndexedDB(): IDBFactory | null {
  const factory = (globalThis as { indexedDB?: IDBFactory | null }).indexedDB;
  return factory ?? null;
}

/**
 * Open the worker's outbox, creating the database and store when this page
 * boots before the worker ever ran.
 *
 * Creating the store here is not optional: the worker opens at the SAME
 * version, so a version-1 database that exists without the store is never
 * upgraded by it — `queueRequest` would then throw on every offline mutation
 * and silently drop the request. The store is created with the worker's exact
 * shape (`keyPath: 'id', autoIncrement: true`) so either side may write first.
 */
function openQueueDatabase(factory: IDBFactory): Promise<IDBDatabase> {
  return new Promise<IDBDatabase>((resolve, reject) => {
    let settled = false;

    try {
      const request = factory.open(
        OFFLINE_QUEUE_DB_NAME,
        OFFLINE_QUEUE_DB_VERSION,
      );

      /**
       * A version change is waiting behind another connection, so this open
       * would never settle. Give up rather than leave a pending promise (and
       * a count the badge waits on) forever.
       */
      request.onblocked = () => {
        settled = true;
        reject(new Error('offline queue open blocked by another connection'));
      };

      /**
       * MUST stay synchronous. Awaiting inside a versionchange transaction
       * deactivates it and the store is never created — the same constraint
       * the worker documents for its own handler.
       */
      request.onupgradeneeded = () => {
        const db = request.result;
        if (!db.objectStoreNames.contains(OFFLINE_QUEUE_STORE_NAME)) {
          db.createObjectStore(OFFLINE_QUEUE_STORE_NAME, {
            keyPath: 'id',
            autoIncrement: true,
          });
        }
      };

      request.onsuccess = () => {
        if (settled) {
          // Lost the race to `onblocked` — close it here or it stays open and
          // blocks the worker's next upgrade.
          request.result.close();
          return;
        }
        settled = true;
        resolve(request.result);
      };

      request.onerror = () => {
        if (settled) return;
        settled = true;
        reject(request.error);
      };
    } catch (err) {
      // A synchronous throw out of `open` (an implementation that refuses to
      // even start) must not escape as an exception into a React effect.
      if (!settled) {
        settled = true;
        reject(err);
      }
    }
  });
}

/**
 * Count the entries in the outbox. The transaction and the count request are
 * created in one task, with nothing awaited between them, and the promise
 * settles from the request's own handlers.
 */
function countQueued(db: IDBDatabase): Promise<number> {
  return new Promise<number>((resolve, reject) => {
    try {
      const tx = db.transaction(OFFLINE_QUEUE_STORE_NAME, 'readonly');
      const request = tx.objectStore(OFFLINE_QUEUE_STORE_NAME).count();

      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
      // A transaction can abort after its request succeeded; without this the
      // promise would hang on a connection we are about to close.
      tx.onabort = () => reject(tx.error);
    } catch (err) {
      // Thrown synchronously for an unknown store (NotFoundError) and for a
      // closed connection (InvalidStateError).
      reject(err);
    }
  });
}

/**
 * How many requests the service worker is still holding for replay.
 *
 * Never throws and never hangs: every failure resolves to 0, and the
 * connection is closed on every path.
 */
export async function countPendingChanges(
  deps: OfflineQueueReaderDeps = {},
): Promise<number> {
  // `null` is a deliberate input (no IndexedDB here), so only an ABSENT key
  // falls back to the ambient implementation.
  const factory = deps.indexedDB !== undefined ? deps.indexedDB : globalIndexedDB();
  if (!factory) return 0;

  let db: IDBDatabase | null = null;
  try {
    db = await openQueueDatabase(factory);
    return await countQueued(db);
  } catch {
    return 0;
  } finally {
    // `close()` waits for in-flight transactions and then prevents new ones.
    db?.close();
  }
}
