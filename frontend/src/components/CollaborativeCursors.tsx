/**
 * Hermes Canopy — Collaborative Cursors
 *
 * React Flow overlay that renders remote user cursors on the canvas.
 * Each cursor shows a colored arrow + user name label at the user's
 * last known cursor position (in React Flow coordinate space).
 *
 * Uses React Flow's useViewport() to map coordinates correctly.
 * Only shows cursors for users with a non-null cursor position.
 */

import { useViewport } from '@xyflow/react';
import { useMemo } from 'react';
import type { UserPresence } from '../types/multiUser.ts';

// ─── Props ─────────────────────────────────────────────────────────────

export interface CollaborativeCursorsProps {
  /** Map of userId → UserPresence for remote users */
  remotePresence: ReadonlyMap<string, UserPresence>;
  /** Local user ID (excluded from cursor rendering) */
  localUserId: string;
}

// ─── Cursor SVG ────────────────────────────────────────────────────────

/** Inline SVG for the cursor arrow, tinted to the user's color. */
function CursorArrow({ color }: { color: string }) {
  return (
    <svg
      width="18"
      height="18"
      viewBox="0 0 24 24"
      fill={color}
      stroke="#0f0f23"
      strokeWidth="1.5"
      style={{ filter: 'drop-shadow(0 1px 2px rgba(0,0,0,0.5))' }}
    >
      <path d="M5 3l14 9-7 2-3 7z" />
    </svg>
  );
}

// ─── Component ─────────────────────────────────────────────────────────

export default function CollaborativeCursors({
  remotePresence,
  localUserId,
}: CollaborativeCursorsProps) {
  const viewport = useViewport();

  // Build list of cursors to render
  const cursors = useMemo(() => {
    const list: Array<{
      userId: string;
      userName: string;
      color: string;
      x: number;
      y: number;
    }> = [];

    for (const [, presence] of remotePresence) {
      if (presence.userId === localUserId) continue;
      if (!presence.cursor) continue;

      list.push({
        userId: presence.userId,
        userName: presence.userName,
        color: presence.avatarColor,
        x: presence.cursor.x,
        y: presence.cursor.y,
      });
    }

    return list;
  }, [remotePresence, localUserId]);

  if (cursors.length === 0) return null;

  return (
    <div
      className="absolute inset-0 pointer-events-none overflow-hidden z-20"
      aria-label="Remote user cursors"
    >
      {cursors.map((cursor) => {
        // Transform flow-space coordinates to screen-space
        const screenX = cursor.x * viewport.zoom + viewport.x;
        const screenY = cursor.y * viewport.zoom + viewport.y;

        return (
          <div
            key={cursor.userId}
            className="absolute"
            style={{
              left: screenX,
              top: screenY,
              transform: 'translate(0, 0)',
              transition: 'left 80ms linear, top 80ms linear',
            }}
          >
            {/* Cursor arrow */}
            <CursorArrow color={cursor.color} />

            {/* Name label */}
            <span
              className="absolute left-4 top-0.5 px-1.5 py-0.5 rounded text-[11px] font-medium whitespace-nowrap select-none shadow-sm"
              style={{
                backgroundColor: cursor.color,
                color: '#ffffff',
              }}
            >
              {cursor.userName}
            </span>
          </div>
        );
      })}
    </div>
  );
}
