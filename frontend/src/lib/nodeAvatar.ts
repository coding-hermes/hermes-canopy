/**
 * Hermes Canopy — Node avatar derivation (UI-04, Phase 11 Mockup Parity)
 *
 * Every card on the branching canvas (docs/mockups/mockup-1.png) carries a
 * colour-coded circular avatar with the author's initials. Three things have
 * to be derived from the single piece of author data a node actually
 * carries — `authorId`:
 *
 *   display name  'local' → "You", an opaque id → a humanised label
 *   initials      at most two characters, upper-case
 *   colours       a deterministic fill (shared with presence avatars) plus
 *                 a foreground picked by *measuring* contrast, never guessed
 *
 * The fill comes from `getColorForUser` in types/multiUser so a person keeps
 * the same colour in the presence bar, the cursors overlay and the canvas.
 * The foreground is whichever of ink/white actually clears WCAG AA against
 * that fill — several palette entries (amber, emerald, cyan) are far too
 * light for white text, and eyeballing them is exactly how contrast bugs
 * ship.
 */

import { getColorForUser, getUserInitials } from '../types/multiUser';
import { palette } from '../theme';

// ─── Constants ─────────────────────────────────────────────────────────

/** Author id used by the local Yjs replica for nodes this user wrote. */
export const LOCAL_AUTHOR_ID = 'local';

/** Label shown for the local author. */
export const LOCAL_AUTHOR_NAME = 'You';

/** Label shown for agent-authored nodes with no resolvable identity. */
export const AGENT_AUTHOR_NAME = 'Agent';

/** Fallback when an author id is missing entirely. */
export const UNKNOWN_AUTHOR_NAME = 'Unknown';

/** Dark ink used on light avatar fills (matches --color-content-inverse). */
export const AVATAR_INK = palette.surfaceBase;

/** Light foreground used on dark avatar fills. */
export const AVATAR_LIGHT = '#ffffff';

/** WCAG AA threshold for normal-size text. */
export const AA_NORMAL = 4.5;

// ─── Contrast ──────────────────────────────────────────────────────────

/** Parse `#rgb` / `#rrggbb` into 0-255 channels. Returns null when unparsable. */
export function parseHex(hex: string): [number, number, number] | null {
  const raw = hex.trim().replace(/^#/, '');
  const full =
    raw.length === 3
      ? raw
          .split('')
          .map((c) => c + c)
          .join('')
      : raw;
  if (!/^[0-9a-fA-F]{6}$/.test(full)) return null;
  return [
    parseInt(full.slice(0, 2), 16),
    parseInt(full.slice(2, 4), 16),
    parseInt(full.slice(4, 6), 16),
  ];
}

/** Relative luminance per WCAG 2.1 §relative-luminance. */
export function relativeLuminance(hex: string): number {
  const rgb = parseHex(hex);
  if (!rgb) return 0;
  const [r, g, b] = rgb.map((channel) => {
    const s = channel / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  }) as [number, number, number];
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

/** WCAG contrast ratio between two hex colours (1 → 21). */
export function contrastRatio(a: string, b: string): number {
  const la = relativeLuminance(a);
  const lb = relativeLuminance(b);
  const light = Math.max(la, lb);
  const dark = Math.min(la, lb);
  return (light + 0.05) / (dark + 0.05);
}

/**
 * Pick the avatar's text colour by measurement: whichever of ink/white has
 * the higher contrast against the fill wins. Every colour in the shared
 * avatar palette clears AA with the winner (verified in the unit tests).
 */
export function avatarTextColor(background: string): string {
  return contrastRatio(background, AVATAR_INK) >=
    contrastRatio(background, AVATAR_LIGHT)
    ? AVATAR_INK
    : AVATAR_LIGHT;
}

// ─── Display name ──────────────────────────────────────────────────────

/**
 * Turn an opaque author id into something a human can read.
 *
 *   'local'              → 'You'
 *   'demo_user_1'        → 'Demo User 1'
 *   'user-sarah-chen'    → 'Sarah Chen'   (a leading 'user' segment is noise)
 *   '' / undefined       → 'Unknown'
 *
 * A caller that *has* real identities (presence, membership) passes them in
 * `names` and those always win — this is only the fallback.
 */
export function authorDisplayName(
  authorId: string | null | undefined,
  opts: { names?: ReadonlyMap<string, string>; isAgent?: boolean } = {},
): string {
  if (!authorId) return UNKNOWN_AUTHOR_NAME;

  const known = opts.names?.get(authorId);
  if (known && known.trim()) return known.trim();

  if (authorId === LOCAL_AUTHOR_ID) return LOCAL_AUTHOR_NAME;

  const segments = authorId
    .split(/[\s_\-.:]+/)
    .filter((s) => s.length > 0)
    .filter((s, i, all) => !(i === 0 && all.length > 1 && /^users?$/i.test(s)));

  if (segments.length === 0) {
    return opts.isAgent ? AGENT_AUTHOR_NAME : UNKNOWN_AUTHOR_NAME;
  }

  // A bare uuid-ish id humanises to nothing useful — prefer a role label.
  const looksOpaque =
    segments.length === 1 && /^[0-9a-f]{8,}$/i.test(segments[0] ?? '');
  if (looksOpaque) {
    return opts.isAgent ? AGENT_AUTHOR_NAME : `User ${segments[0]!.slice(0, 4)}`;
  }

  return segments
    .slice(0, 3)
    .map((s) => s.charAt(0).toUpperCase() + s.slice(1))
    .join(' ');
}

/** Initials (≤2 chars, upper-case) for a resolved display name. */
export function avatarInitials(displayName: string): string {
  const trimmed = displayName.trim();
  if (!trimmed) return '?';
  return getUserInitials(trimmed).toUpperCase().slice(0, 2);
}

// ─── Composite ─────────────────────────────────────────────────────────

/** Everything a node card needs to paint one avatar. */
export interface NodeAvatarDescriptor {
  /** Resolved human-readable author name. */
  name: string;
  /** Up to two upper-case initials. */
  initials: string;
  /** Deterministic fill colour (shared with presence avatars). */
  background: string;
  /** Foreground colour chosen by contrast measurement. */
  color: string;
  /** Measured contrast of `color` on `background` — asserted in tests. */
  contrast: number;
}

/**
 * Resolve the full avatar descriptor for a node author.
 * Deterministic: the same author id always yields the same colour.
 */
export function describeNodeAvatar(
  authorId: string | null | undefined,
  opts: { names?: ReadonlyMap<string, string>; isAgent?: boolean } = {},
): NodeAvatarDescriptor {
  const name = authorDisplayName(authorId, opts);
  const background = getColorForUser(authorId || UNKNOWN_AUTHOR_NAME);
  const color = avatarTextColor(background);
  return {
    name,
    initials: avatarInitials(name),
    background,
    color,
    contrast: contrastRatio(background, color),
  };
}
