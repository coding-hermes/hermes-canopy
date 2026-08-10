/**
 * Hermes Canopy — Reference Link rendering (TM-04)
 *
 * Spec: SPEC-TM-04 §9.2. Renders resolved references as colored links and
 * unresolved references with dashed orange underline.
 *
 * Used within message rendering to replace #slug text with interactive links.
 */

import { useState } from 'react';
import { token, palette, alpha } from '../../theme';

export interface ReferenceLinkProps {
  raw: string; // Original "#slug" text
  slug: string;
  topicId?: string;
  status?: 'active' | 'archived' | 'deleted';
  resolved: boolean;
  onClick?: (topicId?: string) => void;
}

export default function ReferenceLink({
  raw,
  slug,
  topicId,
  status = 'active',
  resolved,
  onClick,
}: ReferenceLinkProps) {
  const [hovered, setHovered] = useState(false);

  if (!resolved) {
    // Unresolved: dashed orange underline.
    return (
      <span
        className="inline-flex items-center gap-0.5 cursor-help"
        style={{
          textDecoration: 'underline',
          textDecorationStyle: 'dashed',
          textDecorationColor: palette.warning,
          color: palette.warning,
          textUnderlineOffset: '3px',
        }}
        onMouseEnter={() => setHovered(true)}
        onMouseLeave={() => setHovered(false)}
        title={`Topic not found: ${slug}`}
      >
        {raw}
        {hovered && (
          <span
            className="absolute z-50 mt-6 px-2 py-1 rounded text-xs whitespace-nowrap border"
            style={{
              backgroundColor: token.surfaceRaised,
              borderColor: token.line,
              color: token.contentPrimary,
            }}
          >
            Topic "{slug}" not found
          </span>
        )}
      </span>
    );
  }

  // Resolved: solid colored link, color by status.
  const color = status === 'archived'
    ? token.contentMuted
    : palette.accent2;

  return (
    <button
      type="button"
      className="inline-flex items-center gap-0.5 font-medium transition-opacity hover:opacity-80"
      style={{
        color,
        textDecoration: 'underline',
        textDecorationColor: alpha(color.startsWith('#') ? color : '#3b82f6', 0.4),
        textUnderlineOffset: '3px',
        background: 'none',
        border: 'none',
        padding: 0,
        cursor: 'pointer',
      }}
      onClick={() => onClick?.(topicId)}
      title={`${slug} — ${status}`}
    >
      {raw}
    </button>
  );
}
