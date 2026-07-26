/**
 * Hermes Canopy — Offline Indicator
 *
 * Shows a banner at the top of the screen when the browser goes offline.
 * Displays queued request count when available.
 * Fades out once connectivity is restored.
 */

import { useState, useEffect, useCallback, useRef, type JSX } from 'react';
import { isOnline, onOnlineChange, getStatus } from '../serviceWorkerRegistration.ts';

export function OfflineIndicator(): JSX.Element | null {
  const [online, setOnline] = useState<boolean>(isOnline());
  const [show, setShow] = useState<boolean>(false);
  const [swActive, setSwActive] = useState<boolean>(false);
  const hideTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Check SW status on mount
  useEffect(() => {
    const status = getStatus();
    setSwActive(status.active);
  }, []);

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

  if (!show) return null;

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
        <span>✓ Back online</span>
      ) : (
        <span>
          ⚡ Offline mode
          {swActive ? ' — changes saved locally' : ''}
        </span>
      )}
    </div>
  );
}
