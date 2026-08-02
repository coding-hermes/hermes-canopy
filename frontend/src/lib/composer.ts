/**
 * Hermes Canopy — Composer helpers (UI-06, Phase 11 Mockup Parity)
 *
 * The floating composer bar (docs/mockups/mockup-1.png, bottom) has three
 * bits of logic that are worth keeping out of the component so they can be
 * tested without a renderer:
 *
 *   placeholder    which prompt the input shows depends on whether the user
 *                  may write at all and whether a ghost slot armed a reply
 *                  target (UI-04). The mockup's default copy is exact.
 *   wire payload   `POST /trees/{tree_id}/nodes` takes SNAKE_CASE JSON —
 *                  `CreateNodePayload` in types/tree.ts is the camelCase
 *                  client shape and does NOT match the wire. The builder
 *                  below is the single place that crosses that boundary,
 *                  so a rename on either side fails a test rather than a
 *                  request.
 *   cursor edits   the @ / # buttons type a trigger character for the user,
 *                  which is a small string splice with spacing rules.
 *
 * Pure functions only. No React, no fetch.
 */

// ─── Placeholders ──────────────────────────────────────────────────────

/** Mockup copy for the idle composer — matches docs/mockups/mockup-1.png. */
export const DEFAULT_PLACEHOLDER = 'Message... use @mention or #topic';

/** Shown once a ghost slot armed a reply target (UI-04). */
export const REPLY_PLACEHOLDER = 'Reply... use @mention or #topic';

/** Shown to viewers, who cannot contribute. */
export const VIEW_ONLY_PLACEHOLDER =
  'View-only mode — request edit access to contribute';

export interface ComposerPlaceholderState {
  /** Viewer permission — the composer is read-only. */
  readOnly?: boolean;
  /** A reply target is armed (TreeView `replyToNodeId`). */
  isReply?: boolean;
}

/**
 * Resolve the composer placeholder.
 *
 * Read-only wins over everything: a viewer who selected a node still cannot
 * reply, so promising them a reply box would be a lie.
 */
export function composerPlaceholder(
  state: ComposerPlaceholderState = {},
): string {
  if (state.readOnly) return VIEW_ONLY_PLACEHOLDER;
  if (state.isReply) return REPLY_PLACEHOLDER;
  return DEFAULT_PLACEHOLDER;
}

// ─── Node-create wire payload ──────────────────────────────────────────

/** Maximum content size the backend accepts (node_handler.go: 64KB). */
export const MAX_CONTENT_BYTES = 64 * 1024;

/**
 * The exact JSON body `POST /api/v1/trees/{tree_id}/nodes` expects.
 * Snake_case, deliberately — see internal/handler/node_handler.go.
 */
export interface CreateNodeBody {
  parent_id: string | null;
  content: string;
  content_format: string;
  node_type: string;
  metadata?: Record<string, unknown>;
}

export interface CreateNodeBodyInput {
  content: string;
  /** Reply target, or null/undefined for a root message. */
  parentId?: string | null;
  contentFormat?: string;
  nodeType?: string;
  metadata?: Record<string, unknown> | null;
}

/**
 * Build the snake_case create-node body.
 *
 * Throws on content the backend would reject anyway (empty after trimming,
 * or over 64KB) so the composer can show an inline message instead of
 * round-tripping to a 400.
 */
export function buildCreateNodeBody(
  input: CreateNodeBodyInput,
): CreateNodeBody {
  const content = input.content.trim();
  if (content.length === 0) {
    throw new Error('Message is empty.');
  }
  if (byteLength(content) > MAX_CONTENT_BYTES) {
    throw new Error('Message is too long (64KB maximum).');
  }

  const parent = input.parentId?.trim();
  const body: CreateNodeBody = {
    parent_id: parent ? parent : null,
    content,
    content_format: input.contentFormat ?? 'markdown',
    node_type: input.nodeType ?? 'message',
  };

  if (input.metadata && Object.keys(input.metadata).length > 0) {
    body.metadata = input.metadata;
  }
  return body;
}

/** UTF-8 byte length — the backend limit counts bytes, not code units. */
export function byteLength(text: string): number {
  return new TextEncoder().encode(text).length;
}

// ─── Send metadata ─────────────────────────────────────────────────────

/** The bits of `File` recorded alongside a message. */
export interface AttachmentLike {
  name: string;
  size: number;
  type: string;
}

export interface SendMetadataInput {
  /**
   * Files the user attached. There is no upload endpoint in MVP, so only
   * their descriptors travel — the bytes stay in the browser.
   */
  files?: readonly AttachmentLike[];
  /** Context nodes pinned into the message. */
  pinnedNodeIds?: readonly string[];
}

/**
 * Describe attachments and pinned context for the node's metadata column.
 * Returns null when there is nothing to say, so the body omits the field
 * entirely rather than sending `{}`.
 */
export function buildSendMetadata(
  input: SendMetadataInput,
): Record<string, unknown> | null {
  const meta: Record<string, unknown> = {};

  const files = input.files ?? [];
  if (files.length > 0) {
    meta.attachments = files.map((f) => ({
      name: f.name,
      size: f.size,
      type: f.type,
    }));
  }

  const pinned = input.pinnedNodeIds ?? [];
  if (pinned.length > 0) {
    meta.pinnedNodeIds = [...pinned];
  }

  return Object.keys(meta).length > 0 ? meta : null;
}

// ─── Cursor insertion (@ / # / emoji buttons) ──────────────────────────

export interface CursorEdit {
  /** The full text after the edit. */
  text: string;
  /** Where the caret should land afterwards. */
  cursor: number;
}

/**
 * Splice `insert` into `text` over the selection `[start, end)`.
 *
 * A trigger typed against a word (`hi` + `@`) would produce `hi@`, which
 * reads as an email fragment rather than a mention, so a separating space
 * is added when the preceding character is not already whitespace. The
 * caret always lands after the inserted text, ready for the name.
 */
export function insertAtCursor(
  text: string,
  start: number,
  end: number,
  insert: string,
): CursorEdit {
  const from = clamp(start, 0, text.length);
  const to = clamp(Math.max(end, from), 0, text.length);

  const before = text.slice(0, from);
  const after = text.slice(to);
  const needsSpace = before.length > 0 && !/\s$/.test(before);
  const payload = needsSpace ? ` ${insert}` : insert;

  return {
    text: `${before}${payload}${after}`,
    cursor: before.length + payload.length,
  };
}

function clamp(value: number, min: number, max: number): number {
  if (Number.isNaN(value)) return min;
  return Math.min(Math.max(value, min), max);
}

// ─── Errors ────────────────────────────────────────────────────────────

/** Fallback copy when a thrown value carries no usable message. */
export const GENERIC_SEND_ERROR = 'Could not send message. Please try again.';

/**
 * Turn whatever `apiPost` rejected with into a sentence for the inline
 * error row. The API helper already unwraps `{"error":{"message":…}}`, so
 * an Error normally carries the server's own words.
 */
export function describeSendError(err: unknown): string {
  if (err instanceof Error && err.message.trim()) return err.message.trim();
  if (typeof err === 'string' && err.trim()) return err.trim();
  return GENERIC_SEND_ERROR;
}
