/**
 * Hermes Canopy — Agent Context Types
 *
 * Types for agent context visualization cards (ThinkingCard, IterationCard,
 * SearchResultCard). Aligned with SPEC-PL-04 dynamic thinking interface.
 */

// ─── Iteration Subtypes ────────────────────────────────────────────────

export type IterationSubtype =
  | 'iteration_search'
  | 'iteration_code_exec'
  | 'iteration_file_read'
  | 'iteration_thinking'
  | 'iteration_tool_call';

export type IterationState =
  | 'running'
  | 'waiting_for_user'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'interrupted';

// ─── Progress ──────────────────────────────────────────────────────────

export type ProgressType = 'search' | 'code_exec' | 'file_read' | 'thinking' | 'tool_call';

export type ProgressStatus = 'running' | 'completed' | 'failed' | 'cancelled';

export interface CardProgress {
  cardId?: string;
  parentCardId?: string;
  type: ProgressType;
  title: string;
  current: number;
  total: number;
  status: ProgressStatus;
  phase?: string;
  updatedAt?: string;
}

// ─── Thinking Card ─────────────────────────────────────────────────────

export interface ThoughtStep {
  id: string;
  title: string;
  status: 'pending' | 'active' | 'completed' | 'failed';
  content: string | null;
  duration_ms: number | null;
  error: string | null;
}

export interface ThinkingData {
  subtype: 'iteration_thinking';
  title: string;
  state: IterationState;
  agentId?: string;
  sessionId?: string;
  topicId?: string;
  progress: CardProgress;
  steps: ThoughtStep[];
  currentStepId: string | null;
  /** Collapse state (client-side, not persisted) */
  _collapsed?: boolean;
}

// ─── Iteration Card (generic / multi-subtype) ──────────────────────────

export type IterationCardSubtypeData =
  | SearchCardSubtypeData
  | CodeExecSubtypeData
  | FileReadSubtypeData
  | ThinkingData
  | ToolCallSubtypeData;

export interface SearchBatchItem {
  url: string;
  snippet: string;
  snippetId: string;
  status: 'queued' | 'searching' | 'retrieved' | 'approved' | 'rejected' | 'error';
}

export interface SearchCardSubtypeData {
  subtype: 'iteration_search';
  title: string;
  state: IterationState;
  agentId?: string;
  sessionId?: string;
  progress: CardProgress;
  urlsSearched: string[];
  currentBatch: SearchBatchItem[];
  focusUrls: string[] | null;
  /** Collapse state (client-side) */
  _collapsed?: boolean;
}

export interface CodeExecSubtypeData {
  subtype: 'iteration_code_exec';
  title: string;
  state: IterationState;
  agentId?: string;
  sessionId?: string;
  progress: CardProgress;
  command: string;
  workdir: string | null;
  status: 'running' | 'completed' | 'cancelled' | 'failed';
  stdout: string[];
  stderr: string[];
  exitCode: number | null;
  startTime: string | null;
  endTime: string | null;
  cancelled: boolean;
  /** Collapse state (client-side) */
  _collapsed?: boolean;
}

export interface FileHighlight {
  startLine: number;
  endLine: number;
  label: string | null;
  note: string | null;
  color: 'yellow' | 'green' | 'red' | 'blue';
}

export interface FileReadSubtypeData {
  subtype: 'iteration_file_read';
  title: string;
  state: IterationState;
  agentId?: string;
  sessionId?: string;
  progress: CardProgress;
  path: string;
  absolutePath: string;
  size: number;
  mimeType: string;
  language: string;
  lineCount: number;
  highlights: FileHighlight[];
  visibleLines: { start: number; end: number };
  /** Collapse state (client-side) */
  _collapsed?: boolean;
}

export interface ToolCallSubtypeData {
  subtype: 'iteration_tool_call';
  title: string;
  state: IterationState;
  agentId?: string;
  sessionId?: string;
  progress: CardProgress;
  toolName: string;
  params: Record<string, unknown>;
  result: unknown;
  status: 'pending_approval' | 'running' | 'completed' | 'denied' | 'failed';
  startTime: string | null;
  endTime: string | null;
  durationMs: number | null;
  error: string | null;
  gated: boolean;
  /** Collapse state (client-side) */
  _collapsed?: boolean;
}

// ─── Search Result (individual result, not the whole card) ─────────────

export interface SearchResult {
  url: string;
  snippet: string;
  snippetId: string;
  status: 'queued' | 'searching' | 'retrieved' | 'approved' | 'rejected' | 'error';
  /** Computed relevance score (0–1) for the indicator bar */
  relevance?: number;
  /** Whether this result is currently highlighted by the user */
  highlighted?: boolean;
}

// ─── Feedback payloads ─────────────────────────────────────────────────

export type FeedbackKind = 'relevance' | 'correction' | 'approve' | 'reject';

export interface SearchFeedbackPayload {
  kind: FeedbackKind;
  target: { url?: string; snippetId?: string };
  note?: string;
}

// ─── Agent Card Metadata (stored in Yjs node.metadata) ─────────────────

export interface AgentCardMetadata {
  cardType: 'iteration';
  cardSubtype: IterationSubtype;
  cardData: IterationCardSubtypeData;
}

/** Check if node metadata indicates an agent iteration card */
export function isAgentCardMetadata(
  metadata: Record<string, unknown> | undefined,
): boolean {
  if (!metadata) return false;
  return metadata.cardType === 'iteration' && typeof metadata.cardSubtype === 'string';
}

/** Safe cast helper — call only when isAgentCardMetadata returned true */
export function asAgentCardMetadata(
  metadata: Record<string, unknown>,
): AgentCardMetadata {
  return metadata as unknown as AgentCardMetadata;
}
