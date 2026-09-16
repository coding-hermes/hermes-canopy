/**
 * Hermes Canopy — Design Tokens (TS mirror)
 *
 * The visual source of truth is the `@theme` block in `src/index.css`.
 * This module exposes the same tokens to TypeScript for the two cases
 * where a Tailwind utility class isn't available:
 *
 *   1. `token.*`   — `var(--color-…)` strings for inline `style={{}}`
 *                    props. These resolve against index.css, so there
 *                    is no duplicated colour value.
 *   2. `palette.*` — raw hex, required where a colour is consumed
 *                    outside CSS cascade resolution (React Flow
 *                    MiniMap node fills, SVG attribute values) or
 *                    composed with an alpha suffix (`${hex}22`).
 *
 * When changing a colour, change `index.css` first, then mirror it in
 * `palette` below. `token` needs no change — it references the var.
 */

// ─── CSS custom-property references (preferred) ────────────────────────

export const token = {
  surfaceBase: 'var(--color-surface-base)',
  surfacePanel: 'var(--color-surface-panel)',
  surfaceRaised: 'var(--color-surface-raised)',
  surfaceInput: 'var(--color-surface-input)',
  surfaceHover: 'var(--color-surface-hover)',

  contentPrimary: 'var(--color-content-primary)',
  contentSecondary: 'var(--color-content-secondary)',
  contentTertiary: 'var(--color-content-tertiary)',
  contentMuted: 'var(--color-content-muted)',
  contentFaint: 'var(--color-content-faint)',
  contentInverse: 'var(--color-content-inverse)',

  lineSubtle: 'var(--color-line-subtle)',
  line: 'var(--color-line)',
  lineStrong: 'var(--color-line-strong)',

  accent: 'var(--color-accent)',
  accent2: 'var(--color-accent-2)',
  accent2Strong: 'var(--color-accent-2-500)',
  accent3: 'var(--color-accent-3)',

  success: 'var(--color-status-success)',
  warning: 'var(--color-status-warning)',
  danger: 'var(--color-status-danger)',
} as const;

// ─── Raw hex mirror (canvas / alpha composition only) ──────────────────

export const palette = {
  surfaceBase: '#0B0D17',
  surfacePanel: '#151830',
  surfaceRaised: '#1C2039',
  surfaceInput: '#242940',
  surfaceHover: '#2C3250',

  contentPrimary: '#E6EAF9',
  contentSecondary: '#CDD4EE',
  contentMuted: '#8B95BA',
  contentFaint: '#7984AB',

  accent: '#22D3EE',
  accent2: '#A78BFA',
  accent2Strong: '#8B5CF6',
  accent3: '#F472B6',

  success: '#34D399',
  warning: '#FBBF24',
  danger: '#FB7185',
  info: '#60A5FA',
  neutral: '#7984AB',
} as const;

// ─── Node-type colour mapping (graph canvas + chips) ───────────────────

/**
 * Canonical accent per graph node type. Used by the canvas MiniMap,
 * composer context chips, and search-result badges so a node keeps the
 * same identity colour everywhere.
 */
export function nodeTypeColor(
  nodeType: string | undefined,
  opts: { isAgent?: boolean } = {},
): string {
  switch (nodeType) {
    case 'synthesis':
      return palette.warning;
    case 'card':
      return opts.isAgent ? palette.accent2 : palette.info;
    case 'topic':
      return palette.accent3;
    case 'system':
      return palette.info;
    case 'message':
      return opts.isAgent ? palette.accent2 : palette.success;
    default:
      return palette.neutral;
  }
}

// ─── Reference convergence palette (SPEC-PL-06 §7.2) ───────────────────

/**
 * Fixed accessible palette for multi-reference convergence edges.
 *
 * §7.2 pins these eight values by `color_key`: the server computes
 * `ref-(fnv1a64(tree:target:source) mod 8)` (§5.2) and the client may only
 * map the key to a colour — never invent one. Selections 9-20 reuse the
 * palette; the `R#` label and the selection index carry identity there, so a
 * repeated colour is never the sole identifier (§7.2).
 *
 * These are deliberately NOT part of `palette`: they are the spec's own
 * contrast-checked ramp, mirrored here for the same reason `palette` exists
 * (SVG stroke / canvas fill attributes sit outside the CSS cascade).
 */
export const referencePalette = {
  'ref-0': '#2563EB',
  'ref-1': '#059669',
  'ref-2': '#D97706',
  'ref-3': '#7C3AED',
  'ref-4': '#DB2777',
  'ref-5': '#0891B2',
  'ref-6': '#65A30D',
  'ref-7': '#C2410C',
} as const;

/** Every §7.2 color key, in palette order. */
export type ReferenceColorKey = keyof typeof referencePalette;

/** Compose a token hex with an 8-bit alpha suffix, e.g. alpha(accent2, 0.12). */
export function alpha(hex: string, amount: number): string {
  const clamped = Math.max(0, Math.min(1, amount));
  const suffix = Math.round(clamped * 255)
    .toString(16)
    .padStart(2, '0');
  return `${hex}${suffix}`;
}
