/**
 * Hermes Canopy — PR Review types (SPEC-023-UI-004)
 *
 * Shared types for the PR review panel surface. Mirrors the backend JSON
 * contracts in internal/handler/review_handler.go (reviewListItem +
 * reviewDetailItem + chimeraVerdict + blastRadius). Field names match the
 * wire contract exactly (snake_case).
 */

/** Review lifecycle status (SPEC-023 §4). */
export type ReviewStatus =
  | 'pending'
  | 'reviewing'
  | 'approved'
  | 'requested_changes';

/** Chimera multi-model verdict type. */
export type ChimeraVerdictType = 'approve' | 'request_changes' | 'error';

/** Blast radius summary (files touched + downstream dependents). */
export interface BlastRadius {
  files_touched: string[];
  dependents_count: number;
}

/** A Chimera multi-model review verdict. */
export interface ChimeraVerdict {
  verdict: ChimeraVerdictType;
  model_formation: string;
  summary: string;
  confidence: number;
  at: string;
}

/** Review list entry — GET /reviews (no blast radius or verdict). */
export interface ReviewListItem {
  id: string;
  pr: string;
  title: string;
  author: string;
  status: ReviewStatus;
  risk_score: number;
  updated_at: string;
}

/** Review detail — GET /reviews/{id} (includes blast radius + verdict). */
export interface ReviewDetail {
  id: string;
  pr: string;
  title: string;
  author: string;
  status: ReviewStatus;
  risk_score: number;
  blast_radius: BlastRadius;
  /** Null when the review has not yet run (status pending/reviewing). */
  verdict: ChimeraVerdict | null;
  created_at: string;
  updated_at: string;
}

/** SSE review_event payload (broadcast on the workspace channel feed). */
export interface ReviewEvent {
  review_id: string;
  pr: string;
  title: string;
  status: ReviewStatus;
  verdict: ChimeraVerdictType;
  risk_score: number;
  triggered_at: string;
}
