/**
 * Hermes Canopy — Service Worker Registration
 *
 * Registers the service worker for offline support.
 * On production builds, the SW is compiled to dist/sw.js.
 * In dev mode, the SW is served via Vite's static file server
 * by copying it to public/ during development.
 */

const SW_PATH = '/sw.js';

let _registered = false;
let _registration: ServiceWorkerRegistration | null = null;

export interface SWStatus {
  supported: boolean;
  registered: boolean;
  active: boolean;
  scope: string | null;
}

/**
 * Register the service worker for offline support.
 * Call once at app startup (main.tsx).
 */
export async function registerSW(): Promise<SWStatus> {
  if (!('serviceWorker' in navigator)) {
    return { supported: false, registered: false, active: false, scope: null };
  }

  if (_registered) {
    return getStatus();
  }

  try {
    _registration = await navigator.serviceWorker.register(SW_PATH, {
      type: 'module',
      scope: '/',
    });

    _registered = true;

    _registration.addEventListener('updatefound', () => {
      const installing = _registration?.installing;
      if (installing) {
        // Notify app about SW update for UI refresh prompts
        window.dispatchEvent(new CustomEvent('sw-update-found'));
      }
    });

    // Notify SW when we come back online
    window.addEventListener('online', () => {
      _registration?.active?.postMessage({ type: 'ONLINE' });
    });

    return getStatus();
  } catch (err) {
    console.warn('[SW] Registration failed:', err);
    return { supported: true, registered: false, active: false, scope: null };
  }
}

/** Get current service worker status */
export function getStatus(): SWStatus {
  if (!('serviceWorker' in navigator)) {
    return { supported: false, registered: false, active: false, scope: null };
  }

  return {
    supported: true,
    registered: _registered,
    active: _registration?.active !== null,
    scope: _registration?.scope ?? null,
  };
}

/** Check if the browser is currently online */
export function isOnline(): boolean {
  return navigator.onLine;
}

/** Listen for online/offline changes */
export function onOnlineChange(handler: (online: boolean) => void): () => void {
  const onlineHandler = () => handler(true);
  const offlineHandler = () => handler(false);

  window.addEventListener('online', onlineHandler);
  window.addEventListener('offline', offlineHandler);

  return () => {
    window.removeEventListener('online', onlineHandler);
    window.removeEventListener('offline', offlineHandler);
  };
}
