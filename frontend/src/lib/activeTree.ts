/**
 * Hermes Canopy — Active tree selection (UI-02)
 *
 * The topics API is tree-scoped (`GET /topics?tree_id=…` → 400
 * MISSING_TREE_ID without it), so the persistent topics rail and the
 * Topics page must agree on which tree is "current". That choice is
 * persisted in `localStorage` and broadcast as a same-tab DOM event, so
 * selecting a tree on the page immediately refreshes the rail (and vice
 * versa) without prop-drilling through the router `Outlet`.
 *
 * `storage` events only fire in *other* tabs, hence the explicit
 * `window.dispatchEvent` — cross-tab sync comes free from the same key.
 */

export const ACTIVE_TREE_STORAGE_KEY = 'canopy.activeTreeId';

/** Read the persisted tree id. Returns '' when unset or unavailable. */
export function readStoredTreeId(): string {
  try {
    return window.localStorage.getItem(ACTIVE_TREE_STORAGE_KEY) ?? '';
  } catch {
    // Private-mode / storage-disabled browsers: degrade to no memory.
    return '';
  }
}

/**
 * Persist the active tree id and notify listeners in this tab.
 * Passing '' clears the selection.
 *
 * The notification only fires when the value actually CHANGES. Listeners
 * typically respond by re-fetching, and a re-fetch usually re-stores the
 * same id — an unconditional dispatch would feed straight back into the
 * listener and spin an infinite fetch loop that crashes the renderer.
 */
export function storeTreeId(treeId: string): void {
  if (readStoredTreeId() === treeId) return;
  try {
    if (treeId) {
      window.localStorage.setItem(ACTIVE_TREE_STORAGE_KEY, treeId);
    } else {
      window.localStorage.removeItem(ACTIVE_TREE_STORAGE_KEY);
    }
  } catch {
    // Ignore quota/permission failures — the event still fires below.
  }
  window.dispatchEvent(new Event(ACTIVE_TREE_STORAGE_KEY));
}

/** Notify listeners that the topic set changed without changing the tree. */
export function notifyTopicsChanged(): void {
  window.dispatchEvent(new Event(ACTIVE_TREE_STORAGE_KEY));
}
