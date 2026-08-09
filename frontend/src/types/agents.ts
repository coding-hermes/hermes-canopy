/**
 * Hermes Canopy — Agent roster types (SPEC-023-UI-003)
 *
 * Shared types for the agent roster surface. Mirrors the backend JSON
 * contracts in internal/handler/agent_handler.go (agentListItem +
 * agentDetailItem). Field names match the wire contract exactly
 * (snake_case).
 */

/** Agent trust tier (SPEC-023 §7 AgentState.tier). */
export type AgentTier = 'provisional' | 'established' | 'veteran';

/** Per-capability success/total tally (SPEC-023 §7). */
export interface CapabilityStat {
  success: number;
  total: number;
}

/** A single point on the trust timeline (SPEC-023 §5). */
export interface TrustHistoryEntry {
  score: number;
  at: string;
}

/** Roster fields shared by both the list and detail responses. */
export interface AgentBase {
  id: string;
  name: string;
  tier: AgentTier;
  trust_score: number;
  capabilities: Record<string, CapabilityStat>;
  incidents: number;
  last_active: string;
}

/** Roster list entry — GET /agents (no trust timeline). */
export type AgentListItem = AgentBase;

/** Agent detail — GET /agents/{id} (includes trust timeline). */
export interface AgentDetail extends AgentBase {
  trust_history: TrustHistoryEntry[];
}
