/**
 * Hermes Canopy — Auto-Topic Detection types (TM-02)
 *
 * Spec: SPEC-TM-02 §5 (Agent Proposal Flow), §6 (User Interaction Model),
 * §8.3 (SSE Payloads). Mirrors the backend Go structs (DetectionConfig,
 * TopicProposal, DetectionType) and the SSE event payloads.
 *
 * Confidence is displayed as a BAND, never a raw probability (spec §6).
 */

// ─── Detection signals ─────────────────────────────────────────────────

/** The signal class that produced a proposal. */
export type DetectionType = 'explicit' | 'implicit' | 'structural';

/** Human-readable label for a detection signal type. */
export const DETECTION_TYPE_LABELS: Record<DetectionType, string> = {
  explicit: 'Direct request',
  implicit: 'Conversation shift',
  structural: 'Branch boundary',
};

// ─── Confidence band ───────────────────────────────────────────────────

/**
 * Display band for a confidence score. Spec §6: "confidence band (not raw
 * model probabilities)". Bands: low <0.65, medium 0.65–0.85, high >0.85.
 */
export type ConfidenceBand = 'low' | 'medium' | 'high';

/** Thresholds per spec §3.2 and §6. */
export const CONFIDENCE_THRESHOLD_LOW = 0.65;
export const CONFIDENCE_THRESHOLD_HIGH = 0.85;

/** Classify a raw confidence [0,1] into a display band. */
export function confidenceBand(confidence: number): ConfidenceBand {
  if (confidence > CONFIDENCE_THRESHOLD_HIGH) return 'high';
  if (confidence >= CONFIDENCE_THRESHOLD_LOW) return 'medium';
  return 'low';
}

/** Display label for a confidence band. */
export const CONFIDENCE_BAND_LABELS: Record<ConfidenceBand, string> = {
  low: 'Possible',
  medium: 'Likely',
  high: 'Confident',
};

// ─── Detection config (per-tree) ───────────────────────────────────────

/** Detection sensitivity level. Spec §2 "Disabled mode" / §4 algorithm. */
export type DetectionLevel = 'off' | 'explicit_only' | 'full';

/** Labels for the settings selector. */
export const DETECTION_LEVEL_LABELS: Record<DetectionLevel, string> = {
  off: 'Off',
  explicit_only: 'Explicit only',
  full: 'Full',
};

/**
 * Per-tree detection configuration. Matches the backend `DetectionConfig`
 * struct (SPEC-TM-02 §8.1) and the `GET/PUT /trees/{id}/topic-detection`
 * JSON shape.
 */
export interface DetectionConfig {
  auto_create: boolean;
  always_ask: boolean;
  detection_level: DetectionLevel;
  min_messages_per_topic: number;
  proposal_cooldown: number;
}

/** Defaults per spec §7: AlwaysAsk=true, AutoCreate=false, full, 3, 10. */
export const DEFAULT_DETECTION_CONFIG: DetectionConfig = {
  auto_create: false,
  always_ask: true,
  detection_level: 'full',
  min_messages_per_topic: 3,
  proposal_cooldown: 10,
};

// ─── Proposal lifecycle ────────────────────────────────────────────────

/** Proposal status as carried by the SSE payload and card state machine. */
export type ProposalStatus =
  | 'pending' // card visible, awaiting user action
  | 'confirming' // accept/rename in-flight (button disabled)
  | 'created' // topic created — show confirmation + link
  | 'rejected' // dismissed/rejected — card hidden
  | 'error'; // server error — card stays, show inline error

/**
 * A topic proposal as delivered by the `topic_proposed` SSE event.
 * Matches the backend `TopicProposal` wire shape (SPEC-TM-02 §8.3).
 */
export interface TopicProposal {
  proposalId: string;
  treeId: string;
  rootNodeId: string;
  title: string;
  description: string;
  detectionType: DetectionType;
  confidence: number;
  subjectKey: string;
  status: string;
  expiresAt: string;
}

/** A created topic returned from confirm or the `topic_created` SSE event. */
export interface CreatedTopic {
  id: string;
  title: string;
  slug: string;
}

// ─── SSE event payloads ────────────────────────────────────────────────

/** `topic_proposed` SSE event data field. */
export interface TopicProposalEvent {
  proposalId: string;
  treeId: string;
  rootNodeId: string;
  title: string;
  description: string;
  detectionType: DetectionType;
  confidence: number;
  subjectKey: string;
  status: string;
  expiresAt: string;
}

/** `topic_created` SSE event data field. */
export interface TopicCreatedEvent {
  proposalId: string;
  topic: CreatedTopic;
}

/** Confirm endpoint success response. */
export interface ConfirmProposalResponse {
  topic: CreatedTopic;
}
