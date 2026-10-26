/**
 * Hermes Canopy — iteration → plugin status mapping (SPEC-PL-04 §8.3).
 *
 * The short status string a card's plugin sees. Kept out of
 * IterationPluginRenderer.tsx so that module exports only its component (React
 * Fast Refresh), and so the mapping can be unit-tested without mounting the
 * sandbox.
 */

import type { IterationCardSubtypeData } from '../../types/agent.ts';

/** Map card lifecycle state to a short plugin-visible status string. */
export function iterationPluginStatus(data: IterationCardSubtypeData): string {
  const progress = data.progress;
  const counts = progress.total > 0 ? `${progress.current}/${progress.total}` : progress.status;
  return `${data.state === 'waiting_for_user' ? 'waiting for user' : data.state} · ${counts}`;
}
