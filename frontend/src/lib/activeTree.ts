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
  window.dispatchEvent(
    new CustomEvent(ACTIVE_TREE_STORAGE_KEY, { detail: { treeId } }),
  );
}

/**
 * The seeded demo tree — E2E-ONLY TEST FIXTURE (GAP-051).
 * Stable UUID — the E2E golden tree, never delete from the seed script.
 * Product code must NOT route normal users here: the sidebar Tree View nav
 * item was removed (GAP-051) and the demo tree is removed from the live DB
 * outside E2E windows (scripts/remove-demo-data.sql). This alias exists so
 * the E2E battery can reach /tree/demo deterministically.
 */
export const DEMO_TREE_UUID = 'b1655761-2d7f-4b3c-85d5-21396da15691';

/**
 * Resolve the `/tree/demo` alias to the real seeded demo tree id.
 *
 * E2E-only fixture path (GAP-051): the E2E battery navigates to /tree/demo
 * (tree-rendering, visual-regression, navigation tests), but the backend
 * has no 'demo' tree — raw ids 400 on every tree-scoped endpoint. Look the
 * tree up by label, fall back to the stable UUID so the alias always
 * resolves even when the search misses. Any non-'demo' id passes through.
 */
/**
 * Synchronous `/tree/demo` alias resolution. The seeded demo tree UUID is
 * canonical — components that just need a valid tree id (header, pages,
 * autocomplete) can use this without a round-trip. The async
 * `resolveDemoAlias` additionally verifies by label search for flows that
 * want to tolerate a reseeded demo tree.
 */
export function resolveDemoAliasSync(raw: string): string {
  return raw === 'demo' ? DEMO_TREE_UUID : raw;
}

export async function resolveDemoAlias(raw: string): Promise<string> {
  if (raw !== 'demo') return raw;
  try {
    const res = await fetch(`/api/v1/trees?search=${encodeURIComponent('UI-02 Rail Demo')}&limit=1`);
    if (!res.ok) return DEMO_TREE_UUID;
    const data = (await res.json()) as { trees?: { id: string }[] };
    const hit = data.trees?.find((t) => t.id);
    return hit?.id ?? DEMO_TREE_UUID;
  } catch {
    return DEMO_TREE_UUID;
  }
}

/** Notify listeners that the topic set changed without changing the tree. */
export function notifyTopicsChanged(): void {
  window.dispatchEvent(new Event(ACTIVE_TREE_STORAGE_KEY));
}
