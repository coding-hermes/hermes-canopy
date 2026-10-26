/**
 * Hermes Canopy — dashboard resume list (GAP-094).
 *
 * Fetches and orders the trees the dashboard's "recent trees" section shows.
 * Kept in lib rather than beside DashboardPage so the page module exports only
 * its component (React Fast Refresh) and the ordering rule can be unit-tested
 * without rendering the page.
 */

import { apiGet } from './api';

/**
 * The subset of GET /trees the resume list needs. `last_activity` is the
 * newest live node's created_at (MAX(nodes.created_at), documented in
 * docs/API.md); it is absent for trees with no live nodes — those sort last by
 * created_at instead of inventing activity they don't have.
 */
export interface RecentTree {
  id: string;
  title: string;
  node_count?: number;
  last_activity?: string;
  created_at?: string;
}

const RECENT_TREES_LIMIT = 8;

/** Fetch a page of trees and order it for the resume list. */
export async function loadRecentTrees(): Promise<RecentTree[]> {
  const body = await apiGet<{ trees?: RecentTree[] }>(
    '/trees?limit=50&sort=created_desc',
  );
  const trees = body.trees ?? [];
  return [...trees]
    .sort((a, b) => {
      const at = a.last_activity ? Date.parse(a.last_activity) : NaN;
      const bt = b.last_activity ? Date.parse(b.last_activity) : NaN;
      const aValid = Number.isFinite(at);
      const bValid = Number.isFinite(bt);
      if (aValid && bValid && at !== bt) return bt - at;
      if (aValid !== bValid) return aValid ? -1 : 1;
      // Neither has activity (or they tie): fall back to creation, newest first.
      const ac = a.created_at ? Date.parse(a.created_at) : 0;
      const bc = b.created_at ? Date.parse(b.created_at) : 0;
      return bc - ac;
    })
    .slice(0, RECENT_TREES_LIMIT);
}
