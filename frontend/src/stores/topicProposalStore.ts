/**
 * Hermes Canopy — Topic Proposal Store (TM-02)
 *
 * Spec: SPEC-TM-02 §7 ("proposal cards live in the topic store, keyed by
 * proposal UUID") and §6 ("stale cards are reconciled by proposal ID when
 * SSE reconnects").
 *
 * Proposals are TRANSIENT UI state — they are NOT Yjs CRDT data. They live
 * in a module-level store keyed by proposalId, with a lightweight subscribe
 * API so React components can re-render on change without a global state
 * library.
 *
 * The store is idempotent (spec §10 "SSE reconnect": replay is de-duplicated
 * by proposalId). Adding the same proposalId twice is a no-op — the SSE
 * reconnect replay must NOT produce duplicate cards.
 */

import type {
  TopicProposal,
  TopicProposalEvent,
  TopicCreatedEvent,
  CreatedTopic,
  ProposalStatus,
} from '../types/topic-detection';

// ─── Card view model ───────────────────────────────────────────────────

/**
 * The card's display model. The SSE `topic_proposed` payload becomes a
 * `pending` card; the card transitions through states as the user acts.
 */
export interface ProposalCard {
  /** The proposal from the SSE event. */
  proposal: TopicProposal;
  /** UI lifecycle state. */
  status: ProposalStatus;
  /** When status==='created', the topic that was created. */
  createdTopic?: CreatedTopic;
  /** When status==='error', the error message to show inline. */
  error?: string;
}

// ─── Store ─────────────────────────────────────────────────────────────

type Listener = () => void;

/** Maps proposalId → card. Module-level: survives route changes. */
const cards = new Map<string, ProposalCard>();
const listeners = new Set<Listener>();
/** Monotonically increasing version — listeners use this to detect change. */
let version = 0;
/**
 * Snapshot caches — useSyncExternalStore REQUIRES getSnapshot to return a
 * stable reference between emits (a fresh array per call triggers
 * "Maximum update depth exceeded" — React re-renders forever). Invalidated
 * on every emit; rebuilt lazily on read.
 */
let cardsCache: ProposalCard[] | null = null;
const cardsByNodeCache = new Map<string, ProposalCard[]>();

function emit(): void {
  version++;
  cardsCache = null;
  cardsByNodeCache.clear();
  for (const fn of listeners) fn();
}

// ─── Public API ────────────────────────────────────────────────────────

/** Subscribe to store changes. Returns an unsubscribe function. */
export function subscribe(listener: Listener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/** Current version counter — components can compare to detect if state changed. */
export function getVersion(): number {
  return version;
}

/** Get a snapshot of all cards (for rendering). Returns a stable cached array. */
export function getCards(): ProposalCard[] {
  if (cardsCache === null) cardsCache = Array.from(cards.values());
  return cardsCache;
}

/** Get cards for a specific root node (the node the proposal is attached to). */
export function getCardsForNode(rootNodeId: string): ProposalCard[] {
  const cached = cardsByNodeCache.get(rootNodeId);
  if (cached !== undefined) return cached;
  const result = Array.from(cards.values()).filter(
    (c) => c.proposal.rootNodeId === rootNodeId,
  );
  cardsByNodeCache.set(rootNodeId, result);
  return result;
}

/** Get a single card by proposalId. */
export function getCard(proposalId: string): ProposalCard | undefined {
  return cards.get(proposalId);
}

/**
 * Add or update a proposal from a `topic_proposed` SSE event.
 * IDEMPOTENT: if a card already exists for this proposalId, it is NOT
 * overwritten — SSE reconnect replays unresolved events and must NOT
 * create duplicate cards (spec §10).
 */
export function addProposal(event: TopicProposalEvent): void {
  if (cards.has(event.proposalId)) return; // dedupe on reconnect
  const proposal: TopicProposal = {
    proposalId: event.proposalId,
    treeId: event.treeId,
    rootNodeId: event.rootNodeId,
    title: event.title,
    description: event.description,
    detectionType: event.detectionType,
    confidence: event.confidence,
    subjectKey: event.subjectKey,
    status: event.status || 'pending',
    expiresAt: event.expiresAt,
  };
  cards.set(event.proposalId, { proposal, status: 'pending' });
  emit();
}

/**
 * Transition a card to a new status. If the card doesn't exist, no-op.
 * Use this for 'confirming', 'created', 'rejected', 'error'.
 */
export function setCardStatus(
  proposalId: string,
  status: ProposalStatus,
  extra?: { createdTopic?: CreatedTopic; error?: string },
): void {
  const card = cards.get(proposalId);
  if (!card) return;
  cards.set(proposalId, {
    ...card,
    status,
    createdTopic: extra?.createdTopic ?? card.createdTopic,
    error: extra?.error,
  });
  emit();
}

/**
 * Handle a `topic_created` SSE event: mark the card as created (if present)
 * and remove it after a short delay so the confirmation shows, then the
 * card disappears and the topic list refreshes.
 *
 * Returns the created topic so the caller can refresh the topics rail.
 */
export function handleTopicCreated(event: TopicCreatedEvent): CreatedTopic {
  setCardStatus(event.proposalId, 'created', {
    createdTopic: event.topic,
  });
  return event.topic;
}

/** Remove a card entirely (after dismissal, rejection, or created confirmation). */
export function removeCard(proposalId: string): void {
  if (cards.delete(proposalId)) emit();
}

/**
 * Reconcile stale cards: remove any card whose proposalId is in the given
 * expired/resolved set. Used for stale-card reconciliation (spec §6, §11.2
 * scenario 6: "Expired card is removed after reconciliation").
 */
export function removeExpired(expiredIds: ReadonlySet<string>): void {
  let changed = false;
  for (const id of expiredIds) {
    if (cards.delete(id)) changed = true;
  }
  if (changed) emit();
}

/** Clear all cards (e.g., when switching trees). */
export function clearAll(): void {
  if (cards.size === 0) return;
  cards.clear();
  emit();
}
