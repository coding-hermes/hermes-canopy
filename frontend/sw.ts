/**
 * Hermes Canopy — Service Worker
 *
 * Caches static assets for offline use and provides a fallback page
 * when the network is unavailable. Follows cache-first strategy for
 * static assets, network-first for API calls.
 *
 * Build: compiled to dist/sw.js via vite build --config vite.sw.config.ts
 */

/// <reference lib="webworker" />

const SW_VERSION = '1.0.0';
const CACHE_NAME = `canopy-static-v${SW_VERSION}`;
const API_CACHE_NAME = `canopy-api-v${SW_VERSION}`;
const STATIC_URLS = [
  '/',
  '/index.html',
];

// ─── Install: pre-cache static assets ────────────────────────────────

self.addEventListener('install', (event: ExtendableEvent) => {
  void (async () => {
    const cache = await caches.open(CACHE_NAME);
    await cache.addAll(STATIC_URLS);
  })();
  // Don't wait — activate immediately even if pages are open
  self.skipWaiting();
});

// ─── Activate: clean old caches ───────────────────────────────────────

self.addEventListener('activate', (event: ExtendableEvent) => {
  void (async () => {
    const keys = await caches.keys();
    await Promise.all(
      keys
        .filter((k) => k.startsWith('canopy-') && k !== CACHE_NAME && k !== API_CACHE_NAME)
        .map((k) => caches.delete(k)),
    );
  })();
  // Take control of all clients immediately
  void self.clients.claim();
});

// ─── Fetch: cache-first for static, network-first for API ────────────

self.addEventListener('fetch', (event: FetchEvent) => {
  const url = new URL(event.request.url);

  // API requests: network-first (so users see fresh data when online)
  if (url.pathname.startsWith('/api/')) {
    event.respondWith(networkFirstWithQueue(event.request));
    return;
  }

  // Vite HMR (dev only) — never cache
  if (url.pathname.startsWith('/@')) {
    return;
  }

  // Static assets: cache-first (instant offline)
  event.respondWith(cacheFirst(event.request));
});

// ─── Cache strategies ─────────────────────────────────────────────────

async function cacheFirst(request: Request): Promise<Response> {
  const cached = await caches.match(request);
  if (cached) return cached;

  try {
    const response = await fetch(request);
    if (response.ok && response.type === 'basic') {
      const cache = await caches.open(CACHE_NAME);
      void cache.put(request, response.clone());
    }
    return response;
  } catch {
    // Offline and not in cache — return index.html for SPA routing
    const index = await caches.match('/index.html');
    if (index) return index;
    return new Response('Offline', { status: 503 });
  }
}

async function networkFirstWithQueue(request: Request): Promise<Response> {
  try {
    const response = await fetch(request);
    if (response.ok) {
      const cache = await caches.open(API_CACHE_NAME);
      void cache.put(request, response.clone());
    }
    return response;
  } catch {
    // Network failed — try cache
    const cached = await caches.match(request);
    if (cached) return cached;

    // Queue for background sync when back online
    await queueRequest(request);
    return new Response(
      JSON.stringify({ error: 'offline', message: 'Request queued for retry' }),
      {
        status: 503,
        headers: { 'Content-Type': 'application/json' },
      },
    );
  }
}

// ─── Background Sync Queue ─────────────────────────────────────────────

const DB_NAME = 'canopy-offline-queue';
const DB_VERSION = 1;
const STORE_NAME = 'requests';

function openQueueDB(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DB_NAME, DB_VERSION);
    request.onupgradeneeded = () => {
      request.result.createObjectStore(STORE_NAME, {
        keyPath: 'id',
        autoIncrement: true,
      });
    };
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

async function queueRequest(request: Request): Promise<void> {
  // Only queue mutating requests (POST, PUT, PATCH, DELETE)
  if (request.method === 'GET') return;

  try {
    const db = await openQueueDB();
    const tx = db.transaction(STORE_NAME, 'readwrite');
    const store = tx.objectStore(STORE_NAME);
    store.add({
      method: request.method,
      url: request.url,
      headers: Object.fromEntries(request.headers.entries()),
      body: request.method !== 'GET' && request.method !== 'HEAD'
        ? await request.clone().text()
        : undefined,
      timestamp: Date.now(),
    });
  } catch {
    // Best-effort queueing — if IndexedDB is unavailable, drop the request
  }
}

// ─── Listen for sync events (periodic) ────────────────────────────────

async function replayQueuedRequests(): Promise<void> {
  try {
    const db = await openQueueDB();
    const tx = db.transaction(STORE_NAME, 'readonly');
    const store = tx.objectStore(STORE_NAME);
    const all = await new Promise<unknown[]>((resolve, reject) => {
      const req = store.getAll();
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });

    for (const entry of all as Array<{ id: number; method: string; url: string; headers: Record<string, string>; body?: string }>) {
      try {
        const response = await fetch(entry.url, {
          method: entry.method,
          headers: entry.headers,
          body: entry.body,
        });
        if (response.ok) {
          // Remove from queue
          const deleteTx = db.transaction(STORE_NAME, 'readwrite');
          deleteTx.objectStore(STORE_NAME).delete(entry.id);
        }
      } catch {
        // Still offline — leave in queue for next attempt
      }
    }
  } catch {
    // Best-effort replay
  }
}

// Periodically check when online and replay
self.addEventListener('message', (event: ExtendableMessageEvent) => {
  if (event.data?.type === 'ONLINE') {
    void replayQueuedRequests();
  }
});

// Also check periodically
setInterval(() => {
  void replayQueuedRequests();
}, 60000); // every 60 seconds
