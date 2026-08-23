/**
 * Teardown cleanup for integration suites that create throwaway trees in the
 * LIVE compose PostgreSQL (BUG-044).
 *
 * Battery tests create uniquely-named scratch trees through the Vite dev proxy
 * and historically left them behind — hundreds accumulated in the real app.
 * Each suite records the trees it creates in a {@link TreeCleanup} tracker and
 * deletes them from `afterAll`, which runs even when a case fails mid-way.
 *
 * Safety contract (mirrors scripts/sweep-e2e-test-trees.py):
 *   - only trees owned by the dev JWT user are deletable,
 *   - trees carrying session metadata are NEVER touched,
 *   - the shared demo tree is protected by title.
 *
 * The tracker uses its own APIRequestContext (the proxy injects the dev JWT
 * server-side), so cleanup still works after the suite closed its browsers.
 */
import { request, type APIRequestContext } from '@playwright/test';
import { BASE_URL } from './setup';

/** Owner of everything created through the Vite dev-proxy JWT. */
export const DEV_USER_ID = '00000000-0000-0000-0000-000000000001';

/** Real content that must never be deleted by battery cleanup. */
const PROTECTED_TITLES = new Set(['UI-02 Rail Demo']);

/** Shape of the backend's flat create/list tree payload we inspect. */
interface SweepableTree {
  id?: string;
  title?: string;
  owner_id?: string;
  session_id?: string;
}

/**
 * True when a tree payload is a battery-created scratch tree that is safe to
 * delete: dev-user owned, no session metadata, not a protected title.
 */
export function isSweepable(tree: SweepableTree): boolean {
  return (
    typeof tree.id === 'string' &&
    tree.id.length > 0 &&
    !tree.session_id &&
    tree.owner_id === DEV_USER_ID &&
    typeof tree.title === 'string' &&
    !PROTECTED_TITLES.has(tree.title)
  );
}

/**
 * Collects tree ids during a suite and deletes them all in `afterAll`.
 *
 * - Skips silently when nothing was tracked (server was down / create failed).
 * - Tolerates already-deleted trees (404).
 * - Never throws: cleanup failures are warned, not fatal — a cleanup bug must
 *   not mask the test result it accompanies.
 */
export class TreeCleanup {
  private readonly ids = new Set<string>();
  private apiPromise: Promise<APIRequestContext> | null = null;

  /** Record a tree id for deletion at teardown. */
  track(id: string): void {
    if (id) this.ids.add(id);
  }

  /**
   * Inspect a POST /api/v1/trees (201) response body and track the tree if it
   * passes the {@link isSweepable} guard. Never throws.
   */
  trackFromCreateBody(body: unknown): void {
    try {
      const tree = body as SweepableTree;
      if (isSweepable(tree)) this.track(tree.id as string);
    } catch {
      /* tracking is best-effort */
    }
  }

  private api(): Promise<APIRequestContext> {
    if (!this.apiPromise) {
      // Dedicated context: survives the suite's own browser/page teardown and
      // is authenticated because the Vite proxy injects the dev JWT itself.
      this.apiPromise = request.newContext({ baseURL: BASE_URL });
    }
    return this.apiPromise;
  }

  /** Delete every tracked tree, then dispose the cleanup request context. */
  async sweep(): Promise<void> {
    if (this.ids.size === 0) return;
    let api: APIRequestContext | null = null;
    try {
      api = await this.api();
      for (const id of [...this.ids]) {
        try {
          const resp = await api.delete(`/api/v1/trees/${id}`);
          if (!resp.ok() && resp.status() !== 404) {
            console.warn(
              `⚠ BUG-044 cleanup: DELETE /api/v1/trees/${id} → ${resp.status()} ${await resp.text()}`,
            );
          }
        } catch (err) {
          console.warn(`⚠ BUG-044 cleanup: DELETE /api/v1/trees/${id} failed:`, err);
        }
        this.ids.delete(id);
      }
    } catch (err) {
      console.warn('⚠ BUG-044 cleanup failed (non-fatal):', err);
    } finally {
      await api?.dispose().catch(() => {});
      this.apiPromise = null;
    }
  }
}
