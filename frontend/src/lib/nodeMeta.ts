/**
 * Hermes Canopy — Node card metadata derivation (UI-05, Phase 11 Mockup Parity)
 *
 * The Nodes page renders each node as a card (docs/mockups/mockup-2.png,
 * mockup-3.png): avatar + author, a timestamp, the body, a `#topic` pill
 * and a `···` overflow menu. Four things have to be derived from the raw
 * `NodeDetail` the list endpoint returns before a card can paint:
 *
 *   author names   the API's `authorDisplayName` is frequently empty and
 *                  `authorId` is a bare UUID — which the UI-04 humaniser
 *                  would render as "00000000 0000 0000". A UUID gets a
 *                  stable short label instead, fed to `describeNodeAvatar`
 *                  through its `names` map so the UI-04 primitive is
 *                  reused rather than forked.
 *   metadata       Go marshals the node's `[]byte` metadata column as a
 *                  BASE64 STRING (`"e30="` is `{}`), not a JSON object.
 *                  Everything downstream needs the decoded record.
 *   topic ref      a node may carry topic linkage in that metadata. The
 *                  pill surfaces whatever the data actually provides and
 *                  renders nothing at all when there is none — no topic
 *                  API is invented here.
 *   meta row       depth / reply count / edited, phrased for humans.
 *
 * Pure functions only, so each one is unit-testable without a renderer.
 */

// ─── Shapes ────────────────────────────────────────────────────────────

/** The subset of `NodeDetail` these helpers read. */
export interface NodeMetaSource {
  id: string;
  authorId: string;
  authorDisplayName?: string;
  content: string;
  nodeType: string;
  metadata?: unknown;
  depth: number;
  childCount: number;
  createdAt: string;
  editedAt?: string | null;
}

/** A topic reference surfaced on a card as a `#slug` pill. */
export interface NodeTopicRef {
  /** Topic id when the metadata carried one — used to resolve a title. */
  id: string | null;
  /** Slug form actually rendered in the pill, without the leading '#'. */
  slug: string;
  /** Human label — a resolved title when available, else the slug. */
  label: string;
}

/** Everything the card's meta row needs, already phrased. */
export interface NodeMetaSummary {
  typeLabel: string;
  depthLabel: string;
  /** `null` on a leaf — "0 replies" is noise. */
  replyLabel: string | null;
  edited: boolean;
  /** First uuid group, for `title`/aria only — never the card's headline. */
  shortId: string;
}

// ─── Author names ──────────────────────────────────────────────────────

const UUID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** True when an id is a bare UUID, which humanises to nothing readable. */
export function isUuidLike(value: string): boolean {
  return UUID_RE.test(value.trim());
}

/**
 * Stable short label for a UUID author: `User 0001`.
 *
 * Mirrors the `User dead` convention the UI-04 humaniser already uses for
 * opaque ids, but keyed on the LAST group — UUID v4/v7 prefixes are shared
 * by every row in a seeded tree, so a prefix-derived label collides while
 * the tail stays distinct.
 */
export function shortAuthorLabel(authorId: string): string {
  const tail = authorId.replace(/-/g, '').slice(-4).toUpperCase();
  return `User ${tail}`;
}

/**
 * Build the `names` map handed to `describeNodeAvatar`.
 *
 * Precedence: the API's display name → a short label for a UUID → nothing
 * (let the UI-04 humaniser handle a readable id like `demo_user_1`).
 */
export function nodeAuthorNames(
  nodes: readonly NodeMetaSource[],
): Map<string, string> {
  const names = new Map<string, string>();
  for (const node of nodes) {
    const id = node.authorId ?? '';
    if (!id || names.has(id)) continue;

    const given = (node.authorDisplayName ?? '').trim();
    if (given) {
      names.set(id, given);
      continue;
    }
    if (isUuidLike(id)) names.set(id, shortAuthorLabel(id));
  }
  return names;
}

// ─── Metadata ──────────────────────────────────────────────────────────

/** Decode a base64 payload to text. Returns null when it isn't base64. */
function decodeBase64(value: string): string | null {
  if (typeof atob !== 'function') return null;
  try {
    const binary = atob(value);
    if (typeof TextDecoder === 'function') {
      const bytes = Uint8Array.from(binary, (c) => c.charCodeAt(0));
      return new TextDecoder().decode(bytes);
    }
    return binary;
  } catch {
    return null;
  }
}

/**
 * Normalise a node's `metadata` into a plain record.
 *
 * The Go handler marshals the `metadata` column (`[]byte`) as a BASE64
 * STRING — `"e30="` decodes to `{}`. Older/local payloads may already be
 * an object or a raw JSON string, so all three are accepted. Anything
 * unparsable yields an empty record rather than throwing: a malformed
 * metadata blob must not blank out a whole page of cards.
 */
export function parseNodeMetadata(metadata: unknown): Record<string, unknown> {
  if (!metadata) return {};

  if (typeof metadata === 'object') {
    return Array.isArray(metadata)
      ? {}
      : (metadata as Record<string, unknown>);
  }

  if (typeof metadata !== 'string') return {};

  const raw = metadata.trim();
  if (!raw) return {};

  const candidates = raw.startsWith('{')
    ? [raw]
    : [decodeBase64(raw), raw].filter((v): v is string => v !== null);

  for (const candidate of candidates) {
    try {
      const parsed: unknown = JSON.parse(candidate);
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
        return parsed as Record<string, unknown>;
      }
    } catch {
      // try the next interpretation
    }
  }
  return {};
}

// ─── Topic pill ────────────────────────────────────────────────────────

/** Lower-case, hyphenated slug — the form rendered inside the pill. */
export function slugifyTopic(value: string): string {
  return value
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
}

/** Keys inspected for topic linkage, in precedence order. */
const TOPIC_KEYS = [
  'topic',
  'topicSlug',
  'topic_slug',
  'topicTitle',
  'topic_title',
  'topicId',
  'topic_id',
] as const;

const ID_KEYS = new Set(['topicId', 'topic_id']);

function firstString(value: unknown): string | null {
  if (typeof value === 'string' && value.trim()) return value.trim();
  return null;
}

/**
 * Pull a topic reference out of a topic-ish metadata value.
 * Accepts a bare string or an object of `{ id, slug, title }`.
 */
function refFromValue(value: unknown, keyIsId: boolean): NodeTopicRef | null {
  const direct = firstString(value);
  if (direct) {
    return keyIsId || isUuidLike(direct)
      ? { id: direct, slug: slugifyTopic(direct), label: direct }
      : { id: null, slug: slugifyTopic(direct), label: direct };
  }

  if (value && typeof value === 'object' && !Array.isArray(value)) {
    const obj = value as Record<string, unknown>;
    const id = firstString(obj.id) ?? firstString(obj.topicId);
    const title = firstString(obj.title) ?? firstString(obj.name);
    const slug = firstString(obj.slug);
    if (title || slug) {
      return {
        id,
        slug: slug ? slugifyTopic(slug) : slugifyTopic(title!),
        label: title ?? slug!,
      };
    }
    if (id) return { id, slug: slugifyTopic(id), label: id };
  }
  return null;
}

/**
 * Resolve the `#topic` pill for a node, or `null` when the node carries
 * no topic linkage — in which case the card renders no pill at all.
 *
 * Sources, in order:
 *   1. node metadata — `topic`, `topicSlug`, `topicId`, … (also `topics[0]`)
 *   2. an inline `#reference` in the body, which is the syntax the product
 *      brief defines for topic references in message text
 *
 * `titles` optionally maps a topic id (or slug) to its real title, so a
 * metadata reference that carries only an id still renders a human label.
 * It comes from the existing `GET /topics?tree_id=…` endpoint; no new API.
 */
export function parseTopicFromNode(
  node: Pick<NodeMetaSource, 'content' | 'metadata'>,
  titles?: ReadonlyMap<string, string>,
): NodeTopicRef | null {
  const meta = parseNodeMetadata(node.metadata);

  let ref: NodeTopicRef | null = null;

  for (const key of TOPIC_KEYS) {
    if (!(key in meta)) continue;
    ref = refFromValue(meta[key], ID_KEYS.has(key));
    if (ref) break;
  }

  if (!ref && Array.isArray(meta.topics)) {
    for (const entry of meta.topics as unknown[]) {
      ref = refFromValue(entry, false);
      if (ref) break;
    }
  }

  if (!ref) {
    const inline = /(?:^|\s)#([a-z][a-z0-9]*(?:[-_][a-z0-9]+)*)/i.exec(
      node.content ?? '',
    );
    if (inline?.[1]) {
      ref = { id: null, slug: slugifyTopic(inline[1]), label: inline[1] };
    }
  }

  if (!ref || !ref.slug) return null;

  const resolved =
    (ref.id ? titles?.get(ref.id) : undefined) ?? titles?.get(ref.slug);

  return resolved ? { ...ref, label: resolved, slug: slugifyTopic(resolved) } : ref;
}

/** The pill's rendered text — always exactly one leading '#'. */
export function topicPillLabel(ref: NodeTopicRef): string {
  return `#${ref.slug}`;
}

/**
 * Index topics by id AND slug so a metadata reference resolves to a real
 * title whichever form it used.
 */
export function indexTopicTitles(
  topics: readonly { id: string; title: string; slug?: string }[],
): Map<string, string> {
  const index = new Map<string, string>();
  for (const topic of topics) {
    const title = (topic.title ?? '').trim();
    if (!title) continue;
    if (topic.id) index.set(topic.id, title);
    const slug = (topic.slug ?? '').trim() || slugifyTopic(title);
    if (slug) index.set(slug, title);
  }
  return index;
}

// ─── Meta row ──────────────────────────────────────────────────────────

const TYPE_LABELS: Readonly<Record<string, string>> = {
  message: 'Message',
  synthesis: 'Synthesis',
  system: 'System',
  card: 'Card',
  topic: 'Topic',
};

/** Human label for a node type; an unknown type passes through verbatim. */
export function nodeTypeLabel(nodeType: string): string {
  return TYPE_LABELS[nodeType] ?? nodeType;
}

/** Everything the card's subtitle row renders, already phrased. */
export function formatNodeMeta(node: NodeMetaSource): NodeMetaSummary {
  const childCount = Number.isFinite(node.childCount) ? node.childCount : 0;
  const depth = Number.isFinite(node.depth) ? node.depth : 0;

  return {
    typeLabel: nodeTypeLabel(node.nodeType),
    depthLabel: `Depth ${depth}`,
    replyLabel:
      childCount > 0
        ? `${childCount} ${childCount === 1 ? 'reply' : 'replies'}`
        : null,
    edited: Boolean(node.editedAt),
    shortId: (node.id ?? '').split('-')[0] ?? '',
  };
}

// ─── Timestamp ─────────────────────────────────────────────────────────

/**
 * Coarse "time ago" for the card's top-right corner.
 *
 * Returns an empty string for an unparsable date so a bad record shows a
 * blank corner rather than the literal "Invalid Date" (same contract as
 * `formatNodeTime` in lib/nodeCard).
 */
export function formatTimeAgo(iso: string, now: number = Date.now()): string {
  const parsed = new Date(iso).getTime();
  if (Number.isNaN(parsed)) return '';

  const sec = Math.floor((now - parsed) / 1000);
  if (sec < 0) return 'just now';
  if (sec < 60) return `${sec}s ago`;

  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m ago`;

  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h ago`;

  return `${Math.floor(hr / 24)}d ago`;
}

// ─── Accessible label ──────────────────────────────────────────────────

/**
 * Full aria-label for a card. The raw id is deliberately kept OUT of the
 * visible card (UI-05 replaces the mono-id row) but stays here so the node
 * is still identifiable to assistive tech and to `title` tooltips.
 */
export function nodeCardAriaLabel(
  node: NodeMetaSource,
  authorName: string,
): string {
  const meta = formatNodeMeta(node);
  const parts = [
    `${meta.typeLabel} by ${authorName}`,
    meta.depthLabel,
    meta.replyLabel,
    meta.edited ? 'edited' : null,
    `id ${meta.shortId}`,
  ].filter((p): p is string => Boolean(p));
  return parts.join(', ');
}
