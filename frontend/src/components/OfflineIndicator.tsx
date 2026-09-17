/**
 * Hermes Canopy — Offline Indicator
 *
 * Shows a banner at the top of the screen when the browser goes offline, and
 * keeps it up while the service worker still holds queued requests in its
 * IndexedDB outbox — the "N pending changes" badge
 * `specs/T1.4-offline-stack-research.md` promises. The count is read through
 * `lib/offlineQueue.ts`; the queue itself is written and drained by
 * `frontend/sw.ts`. Fades out once connectivity is restored AND the queue is
 * empty.
 */

import { useState, useEffect, useCallback, useRef, type JSX } from 'react';
import { isOnline, onOnlineChange, getStatus } from '../serviceWorkerRegistration.ts';
import { countPendingChanges, pendingChangesLabel } from '../lib/offlineQueue.ts';

/**
 * How often the queue is re-read while it can still change. The worker drains
 * it every 60 s while the app is open, so re-reading at a fraction of that
 * cadence is what keeps the badge from lagging a whole drain behind.
 */
export const PENDING_POLL_INTERVAL_MS = 15_000;

/** The real reader, hoisted so the default prop keeps a stable identity. */
const readPendingFromQueue = (): Promise<number> => countPendingChanges();

export interface OfflineIndicatorProps {
  /**
   * Reads how many requests are still in the worker's outbox. Injectable so
   * tests can drive the badge without a real IndexedDB (jsdom has none); the
   * default IS the production reader, so the un-stubbed path is the real one.
   */
  readPendingCount?: () => Promise<number>;
}

/** What the offline banner says after "⚡ Offline mode". */
function offlineDetail(pendingLabel: string | null, swActive: boolean): string {
  if (pendingLabel !== null) return ` — ${pendingLabel}`;
  return swActive ? ' — changes saved locally' : '';
}

export function OfflineIndicator({
  readPendingCount = readPendingFromQueue,
}: OfflineIndicatorProps): JSX.Element | null {
  const [online, setOnline] = useState<boolean>(isOnline());
  const [show, setShow] = useState<boolean>(false);
  const [swActive, setSwActive] = useState<boolean>(false);
  const [pending, setPending] = useState<number>(0);
  const hideTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Check SW status on mount
  useEffect(() => {
    const status = getStatus();
    setSwActive(status.active);
  }, []);

  const hasPending = pending > 0;

  /*
   * Read the queue on mount, on every online/offline transition (the effect
   * re-runs because `online` is a dependency) and on an interval WHILE there
   * is something to watch: offline the count only grows, and after
   * reconnecting the worker drains the outbox over the following minutes — a
   * one-shot read at the transition would leave a stale badge. With a
   * connection and an empty queue there is nothing to poll for.
   *
   * The dependency is the emptiness FLAG, not the count: a count that moves
   * from 3 to 5 must not tear down and re-arm the timer.
   */
  useEffect(() => {
    let cancelled = false;

    const refresh = () => {
      void readPendingCount()
        .then((count) => {
          if (!cancelled) setPending(count);
        })
        .catch(() => {
          // `countPendingChanges` is contractually total (every failure
          // resolves to 0), so a rejection can only come from an injected
          // seam. Treat it as "count unknown" instead of letting it become an
          // unhandled rejection inside a status banner.
          if (!cancelled) setPending(0);
        });
    };

    refresh();

    if (online && !hasPending) {
      return () => {
        cancelled = true;
      };
    }

    const timer = setInterval(refresh, PENDING_POLL_INTERVAL_MS);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, [online, hasPending, readPendingCount]);

  const updateOnline = useCallback((newOnline: boolean) => {
    setOnline(newOnline);

    if (!newOnline) {
      setShow(true);
      // Clear any pending hide timer
      if (hideTimer.current) {
        clearTimeout(hideTimer.current);
        hideTimer.current = null;
      }
    } else {
      // Show "back online" briefly then hide
      if (hideTimer.current) {
        clearTimeout(hideTimer.current);
      }
      hideTimer.current = setTimeout(() => {
        setShow(false);
        hideTimer.current = null;
      }, 2000);
    }
  }, []);

  useEffect(() => {
    const cleanup = onOnlineChange(updateOnline);
    return cleanup;
  }, [updateOnline]);

  // Don't leave a hide timer armed past unmount.
  useEffect(() => {
    return () => {
      if (hideTimer.current) {
        clearTimeout(hideTimer.current);
        hideTimer.current = null;
      }
    };
  }, []);

  const pendingLabel = pendingChangesLabel(pending);

  /*
   * Visibility: offline is always worth saying. Online, the "back online"
   * flash alone must never hide a queue that still holds work — that is the
   * whole point of the count badge — so a non-empty queue keeps the banner
   * (and its count) up until the worker has actually drained it.
   */
  const bannerVisible = online ? show || pendingLabel !== null : true;
  if (!bannerVisible) return null;

  return (
    <div
      role="status"
      aria-live="polite"
      className={[
        'fixed top-0 left-0 right-0 z-50 px-4 py-2 text-center text-sm font-medium transition-all duration-300',
        online
          ? 'bg-green-500 text-white'
          : 'bg-amber-500 text-black',
      ].join(' ')}
    >
      {online ? (
        <span>
          ✓ Back online
          {pendingLabel !== null ? ` — ${pendingLabel} still queued` : ''}
        </span>
      ) : (
        <span>
          ⚡ Offline mode
          {offlineDetail(pendingLabel, swActive)}
        </span>
      )}
    </div>
  );
}
