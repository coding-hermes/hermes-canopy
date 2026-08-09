/**
 * Hermes Canopy — Workspace types (SPEC-023-UI-002)
 *
 * Shared types for the workspace channels surface. Mirrors the backend
 * JSON contracts in internal/handler/workspace_handler.go.
 */

/** A workspace channel (general, agents, …). */
export interface ChannelSummary {
  id: string;
  name: string;
  /** Best-effort subscriber count from the live SSE hub (optional). */
  subscriber_count?: number;
}

/** A single channel message delivered over SSE or returned from POST. */
export interface ChannelMessage {
  message_id: string;
  channel_id: string;
  sender_id: string;
  content: string;
  sent_at: string;
}

/** Response shape for POST /workspace/channels/{id}/message (HTTP 202). */
export interface SendMessageResponse {
  message_id: string;
  channel_id: string;
  content: string;
}
