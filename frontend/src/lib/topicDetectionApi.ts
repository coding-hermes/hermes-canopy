/**
 * Hermes Canopy — Topic Detection API client (TM-02)
 *
 * Spec: SPEC-TM-02 §8.2 (HTTP Handlers and Routes).
 * Uses the shared apiGet/apiPost/apiPut helpers from lib/api.ts.
 */

import { apiGet, apiPost, apiPut } from './api';
import type {
  DetectionConfig,
  ConfirmProposalResponse,
} from '../types/topic-detection';

// ── Confirm proposal ────────────────────────────────────────────────────

/**
 * Confirm (accept or rename) a topic proposal.
 * Spec §5.5–5.6: Accept calls Confirm(proposalID, ""); Rename collects a
 * non-empty title (1–200 chars) and calls Confirm(proposalID, override).
 */
export function confirmProposal(
  proposalId: string,
  titleOverride?: string,
): Promise<ConfirmProposalResponse> {
  const body =
    titleOverride !== undefined && titleOverride !== ''
      ? { titleOverride }
      : { titleOverride: '' };
  return apiPost<ConfirmProposalResponse>(
    `/topic-proposals/${encodeURIComponent(proposalId)}/confirm`,
    body,
  );
}

// ── Dismiss / reject proposal ───────────────────────────────────────────

/**
 * Dismiss (reject) a topic proposal.
 * Spec §5.7: no topic is created; the subject key enters cooldown.
 * Returns void — the endpoint is 204 or 200.
 */
export async function dismissProposal(proposalId: string): Promise<void> {
  await apiPost<void>(
    `/topic-proposals/${encodeURIComponent(proposalId)}/dismiss`,
    undefined,
  );
}

// ── Detection config (per-tree) ─────────────────────────────────────────

/** GET /trees/{treeId}/topic-detection — read the per-tree detection config. */
export function getDetectionConfig(treeId: string): Promise<DetectionConfig> {
  return apiGet<DetectionConfig>(
    `/trees/${encodeURIComponent(treeId)}/topic-detection`,
  );
}

/**
 * PUT /trees/{treeId}/topic-detection — update the per-tree config.
 * Only the changed fields need to be present in the patch (spec §8.2:
 * "all fields optional"). Returns the full updated config.
 */
export function updateDetectionConfig(
  treeId: string,
  patch: Partial<DetectionConfig>,
): Promise<DetectionConfig> {
  return apiPut<DetectionConfig>(
    `/trees/${encodeURIComponent(treeId)}/topic-detection`,
    patch,
  );
}
