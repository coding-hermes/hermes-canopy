/**
 * Hermes Canopy — App Card wire types, zod validation and canonical adapters
 * (SPEC-PL-03 §5.1 canonical types, §5.2 validation, §9.1/§9.3 SSE wire).
 *
 * Two layers live here and must not be confused:
 *
 *  1. **Wire schemas** (`CardSummaryWireSchema`, `CardEventWireSchema`,
 *     `CardSSEEnvelopeSchema`) validate the JSON the SHIPPED server actually
 *     sends. They constrain only what it guarantees — presence and JSON type.
 *     No UUIDv7 regex, no `.strict()`, no `revision`/`dismissedAt`/`archivedAt`
 *     demands: a schema that rejects a real server payload is a bug (§5.2's
 *     illustrative snippets describe a different, unimplemented wire).
 *  2. **Canonical types** (`Card`, `CardEvent`, …) are the §5.1 camelCase
 *     shape. Pure adapters below convert wire → canonical.
 *
 * Adaptations the shipped Go wire forced (each with its source location):
 *
 *  - **Snapshot ordering key = `last_event_seq`, not `revision`.** §5.3 orders
 *    snapshots by `Card.revision`; the wire has no such field. The snapshot
 *    body is `service.CardSummary` (`internal/service/card_service.go:24-37`),
 *    whose only monotonic counter is `last_event_seq`; the canonical `Card`
 *    therefore exposes it as `revision` and the store compares that.
 *  - **Nested card summary uses `type`, not `card_type`.** `CardSummary.Type`
 *    is tagged `json:"type"` (`internal/service/card_service.go:30`), unlike the
 *    domain `card.Card.CardType` (`internal/card/models.go:86`, `card_type`).
 *  - **Stored event rows are snake_case.** `service.CardEvent` is tagged
 *    `event_id`/`actor_kind`/`payload`/`created_at`
 *    (`internal/service/card_service.go:91-100`), where §5.1 shows camelCase.
 *    The adapters do the snake→camel mapping.
 *  - **The envelope's `card_type` is `omitempty` and `sequence` may be absent.**
 *    `cardSSEEnvelope` (`internal/handler/card_events_handler.go:63-70`) omits
 *    `card_type`, and a heartbeat body (`:347-358`) is emitted with `sequence`
 *    0 — so both fields are optional here and a heartbeat is never treated as a
 *    position in the log (§9.1: the snapshot frame deliberately carries no `id:`
 *    line, `:97-102`).
 *  - **No `id:` line reaches this layer.** The app's only SSE transport parses
 *    and DISCARDS `id:` (`frontend/src/lib/sse.ts:123`), so a rejected payload
 *    is reported with the envelope `sequence` and, when the nested row got far
 *    enough to carry one, its `event_id`.
 *
 * Policy: the wire schema is structural; the ADAPTERS enforce the §5.1 unions
 * (`CardType`, `CardStatus`, `CardEventType`, `CardActorKind`) and RETURN a
 * rejection — they never coerce an unrecognised value into a wrong-looking one,
 * and nothing in this module throws at the UI (§5.3: a payload that fails
 * validation becomes a non-destructive error).
 */

import { z } from 'zod';

// ── Canonical values (§5.1) ────────────────────────────────────────────

export const cardTypeValues = ['compact', 'expanded', 'iteration'] as const;
export type CardType = (typeof cardTypeValues)[number];

export const cardStatusValues = ['active', 'dismissed', 'archived'] as const;
export type CardStatus = (typeof cardStatusValues)[number];

export const cardEventTypeValues = [
  'card_created',
  'card_updated',
  'card_dismissed',
  'card_restored',
  'card_archived',
  'agent_progress',
  'agent_output',
  'agent_error',
  'user_feedback',
  'action_requested',
  'action_completed',
  'context_rebound',
  'sync_applied',
  'sync_conflict',
] as const;
export type CardEventType = (typeof cardEventTypeValues)[number];

export const cardActorKindValues = ['agent', 'user', 'system', 'sync'] as const;
export type CardActorKind = (typeof cardActorKindValues)[number];

/** The three SSE event names the card stream emits (§9.3). */
export const cardSSEEventNames = ['card_snapshot', 'card_event', 'heartbeat'] as const;
export type CardSSEEventName = (typeof cardSSEEventNames)[number];

// ── Canonical types (§5.1) ─────────────────────────────────────────────

/** A declared card action (§5.1). Text-only: the label is rendered as-is. */
export interface CardAction {
  label: string;
  handler: string;
}

/**
 * The materialised card (§5.1), as returned inside a `card_snapshot` frame.
 *
 * `revision` is the snapshot ordering key. The wire carries no `revision`; it
 * is taken from the summary's `last_event_seq` (see the module header).
 * `treeId`/`nodeId` are additive: the wire carries them and discarding them
 * would lose the card's graph attachment.
 */
export interface Card {
  id: string;
  treeId: string;
  nodeId: string;
  appId: string;
  cardType: CardType;
  data: Record<string, unknown>;
  actions: CardAction[];
  status: CardStatus;
  contextHash: string;
  revision: number;
  createdAt: string;
  updatedAt?: string;
  dismissedAt?: string;
  archivedAt?: string;
}

/** One stored card event (§5.1), as returned inside a `card_event` frame. */
export interface CardEvent {
  sequence: number;
  eventId: string;
  cardId: string;
  eventType: CardEventType;
  actorKind: CardActorKind;
  actorId: string;
  payload: Record<string, unknown>;
  createdAt: string;
}

/** The canonical SSE envelope (§5.1) — snake_case, as the spec declares it. */
export interface CardSSEEvent {
  event_type: CardSSEEventName;
  card_type?: CardType;
  card_id: string;
  sequence?: number;
  timestamp: string;
  data: CardEvent | Card | { lastSequence: number };
}

// ── Wire schemas ───────────────────────────────────────────────────────

/** A declared action as stored (`card.CardAction`, `internal/card/models.go:75-78`). */
export const CardActionWireSchema = z.object({
  label: z.string().min(1),
  handler: z.string().min(1),
});
export type CardActionWire = z.infer<typeof CardActionWireSchema>;

/**
 * `service.CardSummary` — the body of both the REST card routes and the
 * `card_snapshot` frame (`internal/service/card_service.go:24-37`,
 * `internal/handler/card_events_handler.go:329-342`).
 *
 * `actions` is `.nullish()` because a nil Go slice marshals as `null`
 * (precedent: `frontend/src/types/fileviewer.ts` header note 1).
 */
export const CardSummaryWireSchema = z.object({
  id: z.string().min(1),
  tree_id: z.string(),
  node_id: z.string(),
  app_id: z.string(),
  /** `CardSummary.Type` — the WIRE name is `type`, not `card_type`. */
  type: z.string().min(1),
  status: z.string().min(1),
  context_hash: z.string(),
  data: z.unknown(),
  actions: z.array(CardActionWireSchema).nullish(),
  last_event_seq: z.number().int().nonnegative(),
  created_at: z.string().datetime({ offset: true }),
});
export type CardSummaryWire = z.infer<typeof CardSummaryWireSchema>;

/**
 * `service.CardEvent` — one stored row, snake_case, as framed by the
 * `card_event` event (`internal/service/card_service.go:91-100`).
 */
export const CardEventWireSchema = z.object({
  sequence: z.number().int().nonnegative(),
  event_id: z.string().min(1),
  card_id: z.string().min(1),
  event_type: z.string().min(1),
  actor_kind: z.string().min(1),
  actor_id: z.string(),
  payload: z.unknown(),
  created_at: z.string().datetime({ offset: true }),
});
export type CardEventWire = z.infer<typeof CardEventWireSchema>;

/**
 * The `data:` line of any card SSE frame (`cardSSEEnvelope`,
 * `internal/handler/card_events_handler.go:63-70`).
 *
 * `card_type` is `omitempty` on the Go struct and `sequence` is optional here
 * to match the heartbeat body, which is emitted with sequence 0 and no card
 * type. `data` is validated per event type by `parseCardStreamData`.
 */
export const CardSSEEnvelopeSchema = z.object({
  event_type: z.enum(cardSSEEventNames),
  card_id: z.string().min(1),
  card_type: z.string().optional(),
  sequence: z.number().int().nonnegative().optional(),
  timestamp: z.string().datetime({ offset: true }),
  data: z.unknown(),
});
export type CardSSEEnvelopeWire = z.infer<typeof CardSSEEnvelopeSchema>;

// ── Parse result ───────────────────────────────────────────────────────

/**
 * A parse that never throws (§5.3). The success branch carries the value; the
 * failure branch carries human-readable issues plus the best-effort position of
 * the offending payload (`sequence` / `eventId`) so a rejection can be filed
 * against the event that caused it.
 */
export type ParseResult<T> =
  | { ok: true; value: T }
  | { ok: false; issues: string[]; sequence: number | null; eventId: string | null };

function issuesOf(error: z.ZodError): string[] {
  return error.issues.map((issue) => {
    const path = issue.path.length > 0 ? issue.path.join('.') : '<root>';
    return `${path}: ${issue.message}`;
  });
}

function parseWith<T>(schema: z.ZodType<T>, raw: unknown): ParseResult<T> {
  const parsed = schema.safeParse(raw);
  if (parsed.success) return { ok: true, value: parsed.data };
  return { ok: false, issues: issuesOf(parsed.error), sequence: null, eventId: null };
}

/** Validate a `service.CardSummary` body. */
export function parseCardSummaryWire(raw: unknown): ParseResult<CardSummaryWire> {
  return parseWith(CardSummaryWireSchema, raw);
}

/** Validate a stored card-event row. */
export function parseCardEventWire(raw: unknown): ParseResult<CardEventWire> {
  return parseWith(CardEventWireSchema, raw);
}

/** Validate an SSE envelope (its `data` is validated separately). */
export function parseCardSSEEnvelope(raw: unknown): ParseResult<CardSSEEnvelopeWire> {
  return parseWith(CardSSEEnvelopeSchema, raw);
}

// ── Adapters (wire → canonical) ────────────────────────────────────────

function memberOf<T extends string>(values: readonly T[], raw: string): T | null {
  return (values as readonly string[]).includes(raw) ? (raw as T) : null;
}

/** A JSON object as a plain record; anything else (array, scalar, null) → {}. */
export function asRecord(raw: unknown): Record<string, unknown> {
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) return {};
  return raw as Record<string, unknown>;
}

function rejection<T>(issue: string): ParseResult<T> {
  return { ok: false, issues: [issue], sequence: null, eventId: null };
}

/**
 * `service.CardSummary` → canonical `Card`.
 *
 * Rejects (rather than mis-labels) a `type`/`status` outside the §5.1 unions —
 * the panel renders both verbatim, so an unknown value is a payload we cannot
 * display, and §5.3 says that is an error, not a guess.
 */
export function cardFromSummaryWire(wire: CardSummaryWire): ParseResult<Card> {
  const cardType = memberOf(cardTypeValues, wire.type);
  if (cardType === null) {
    return rejection(`type: unrecognized card type ${JSON.stringify(wire.type)}`);
  }
  const status = memberOf(cardStatusValues, wire.status);
  if (status === null) {
    return rejection(`status: unrecognized card status ${JSON.stringify(wire.status)}`);
  }

  return {
    ok: true,
    value: {
      id: wire.id,
      treeId: wire.tree_id,
      nodeId: wire.node_id,
      appId: wire.app_id,
      cardType,
      data: asRecord(wire.data),
      actions: (wire.actions ?? []).map((a) => ({ label: a.label, handler: a.handler })),
      status,
      contextHash: wire.context_hash,
      // The wire's only monotonic card counter (see the module header).
      revision: wire.last_event_seq,
      createdAt: wire.created_at,
      updatedAt: undefined,
      dismissedAt: undefined,
      archivedAt: undefined,
    },
  };
}

/** A stored event row → canonical `CardEvent` (snake_case → camelCase). */
export function cardEventFromWire(wire: CardEventWire): ParseResult<CardEvent> {
  const eventType = memberOf(cardEventTypeValues, wire.event_type);
  if (eventType === null) {
    return rejection(`event_type: unrecognized card event type ${JSON.stringify(wire.event_type)}`);
  }
  const actorKind = memberOf(cardActorKindValues, wire.actor_kind);
  if (actorKind === null) {
    return rejection(`actor_kind: unrecognized card actor kind ${JSON.stringify(wire.actor_kind)}`);
  }

  return {
    ok: true,
    value: {
      sequence: wire.sequence,
      eventId: wire.event_id,
      cardId: wire.card_id,
      eventType,
      actorKind,
      actorId: wire.actor_id,
      payload: asRecord(wire.payload),
      createdAt: wire.created_at,
    },
  };
}

// ── Frames ─────────────────────────────────────────────────────────────

/**
 * One validated frame, discriminated by what the store must do with it
 * (§9.3): a snapshot replaces the card, an event appends by sequence, a
 * heartbeat only proves liveness.
 */
export type CardStreamFrame =
  | { kind: 'snapshot'; card: Card; sequence: number; timestamp: string }
  | { kind: 'event'; event: CardEvent; sequence: number; timestamp: string }
  | { kind: 'heartbeat'; cardId: string; timestamp: string };

/** Best-effort `event_id` of a payload that failed nested validation. */
function nestedEventId(raw: unknown): string | null {
  if (typeof raw !== 'object' || raw === null) return null;
  const data = (raw as { data?: unknown }).data;
  if (typeof data !== 'object' || data === null) return null;
  const id = (data as { event_id?: unknown }).event_id;
  return typeof id === 'string' && id.length > 0 ? id : null;
}

/** Best-effort envelope sequence of a payload that failed nested validation. */
function envelopeSequence(raw: unknown): number | null {
  if (typeof raw !== 'object' || raw === null) return null;
  const sequence = (raw as { sequence?: unknown }).sequence;
  return typeof sequence === 'number' && Number.isInteger(sequence) && sequence >= 0
    ? sequence
    : null;
}

/**
 * Parse one raw `data:` line into a validated frame (§9.1/§9.3).
 *
 * This is the single seam the store consumes: JSON, then the envelope, then the
 * body the envelope's `event_type` names. Nothing throws — a failure is a
 * `ParseResult` failure the store files as a non-destructive error.
 */
export function parseCardStreamData(raw: string): ParseResult<CardStreamFrame> {
  let decoded: unknown;
  try {
    decoded = JSON.parse(raw);
  } catch (err) {
    return {
      ok: false,
      issues: [`data: invalid JSON (${err instanceof Error ? err.message : String(err)})`],
      sequence: null,
      eventId: null,
    };
  }

  const envelope = parseCardSSEEnvelope(decoded);
  if (!envelope.ok) {
    return {
      ok: false,
      issues: envelope.issues,
      sequence: envelopeSequence(decoded),
      eventId: nestedEventId(decoded),
    };
  }

  const env = envelope.value;
  const position = { sequence: env.sequence ?? null, eventId: nestedEventId(decoded) };

  switch (env.event_type) {
    case 'card_snapshot': {
      const summary = parseCardSummaryWire(env.data);
      if (!summary.ok) return { ok: false, issues: summary.issues, ...position };
      const card = cardFromSummaryWire(summary.value);
      if (!card.ok) return { ok: false, issues: card.issues, ...position };
      return {
        ok: true,
        value: {
          kind: 'snapshot',
          card: card.value,
          // Display only. §9.1: a snapshot is NOT a position in the log, so the
          // store must never advance its cursor from this field.
          sequence: env.sequence ?? card.value.revision,
          timestamp: env.timestamp,
        },
      };
    }
    case 'card_event': {
      const event = parseCardEventWire(env.data);
      if (!event.ok) return { ok: false, issues: event.issues, ...position };
      const canonical = cardEventFromWire(event.value);
      if (!canonical.ok) return { ok: false, issues: canonical.issues, ...position };
      return {
        ok: true,
        value: {
          kind: 'event',
          event: canonical.value,
          sequence: canonical.value.sequence,
          timestamp: env.timestamp,
        },
      };
    }
    case 'heartbeat':
      return {
        ok: true,
        value: { kind: 'heartbeat', cardId: env.card_id, timestamp: env.timestamp },
      };
  }
}

// ── Display helpers ────────────────────────────────────────────────────

/** Human label for a canonical card type (the three MVP adapters). */
export function cardTypeLabel(cardType: CardType): string {
  switch (cardType) {
    case 'compact':
      return 'Compact';
    case 'expanded':
      return 'Expanded';
    case 'iteration':
      return 'Iteration';
  }
}

/** Human label for a canonical card status. */
export function cardStatusLabel(status: CardStatus): string {
  switch (status) {
    case 'active':
      return 'Active';
    case 'dismissed':
      return 'Dismissed';
    case 'archived':
      return 'Archived';
  }
}
