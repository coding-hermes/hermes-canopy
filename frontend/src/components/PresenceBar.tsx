/**
 * Hermes Canopy — Presence Bar
 *
 * Horizontal bar showing online users as colored avatar circles.
 * Each avatar shows user initials, colored background, and a tooltip
 * with the user name and permission badge.
 *
 * Active users: full opacity. Idle users (>5 min since lastSeen): reduced.
 * Avatars are stacked with -8px overlap.
 */

import { useMemo } from 'react';
import type { UserPresence } from '../types/multiUser.ts';
import {
  getUserInitials,
  getPermissionLabel,
  getPermissionStyle,
} from '../types/multiUser.ts';

// ─── Props ─────────────────────────────────────────────────────────────

export interface PresenceBarProps {
  /** Map of userId → UserPresence for remote users */
  remotePresence: ReadonlyMap<string, UserPresence>;
  /** Local user ID (excluded from avatar list — shown separately or not at all) */
  localUserId: string;
  /** Total online count (remote + self) */
  onlineCount?: number;
}

// ─── Constants ─────────────────────────────────────────────────────────

/** Users idle for more than this many ms are shown at reduced opacity */
const IDLE_THRESHOLD_MS = 5 * 60 * 1000; // 5 minutes

// ─── Avatar size ───────────────────────────────────────────────────────

const AVATAR_SIZE = 32;
const AVATAR_OVERLAP = -8;

// ─── Component ─────────────────────────────────────────────────────────

export default function PresenceBar({
  remotePresence,
  localUserId,
  onlineCount,
}: PresenceBarProps) {
  const users = useMemo(() => {
    const list: UserPresence[] = [];
    for (const [, presence] of remotePresence) {
      if (presence.userId !== localUserId) {
        list.push(presence);
      }
    }
    // Sort by lastSeen desc (most recent first)
    list.sort(
      (a, b) =>
        new Date(b.lastSeen).getTime() - new Date(a.lastSeen).getTime(),
    );
    return list;
  }, [remotePresence, localUserId]);

  const totalOnline =
    onlineCount ?? (users.length + (localUserId ? 1 : 0));

  if (users.length === 0) {
    return (
      <div className="flex items-center gap-2 px-4 py-1.5">
        <span className="text-xs" style={{ color: '#4a4a6a' }}>
          {totalOnline > 0
            ? `${totalOnline} online`
            : 'No other users online'}
        </span>
      </div>
    );
  }

  return (
    <div className="flex items-center gap-2 px-4 py-1.5">
      {/* Online count */}
      <span className="text-xs flex-shrink-0" style={{ color: '#4a4a6a' }}>
        {totalOnline} online
      </span>

      {/* Avatar stack */}
      <div className="flex items-center">
        {users.map((user) => {
          const now = Date.now();
          const lastSeen = new Date(user.lastSeen).getTime();
          const isIdle = now - lastSeen > IDLE_THRESHOLD_MS;
          const initials = getUserInitials(user.userName);
          const permStyle = getPermissionStyle(user.permission);

          return (
            <div
              key={user.userId}
              className="relative group"
              style={{ marginLeft: AVATAR_OVERLAP }}
              title={`${user.userName} — ${getPermissionLabel(user.permission)}${isIdle ? ' (idle)' : ''}`}
            >
              {/* Avatar circle */}
              <div
                className="flex items-center justify-center rounded-full border-2 font-semibold text-xs select-none transition-opacity"
                style={{
                  width: AVATAR_SIZE,
                  height: AVATAR_SIZE,
                  backgroundColor: user.avatarColor,
                  borderColor: '#0f0f1a',
                  color: '#ffffff',
                  opacity: isIdle ? 0.4 : 1,
                }}
              >
                {initials}
              </div>

              {/* Online indicator dot */}
              {user.isActive && !isIdle && (
                <div
                  className="absolute bottom-0 right-0 rounded-full border-2"
                  style={{
                    width: 10,
                    height: 10,
                    backgroundColor: '#22c55e',
                    borderColor: '#0f0f1a',
                  }}
                />
              )}

              {/* Tooltip */}
              <div
                className="absolute bottom-full left-1/2 -translate-x-1/2 mb-2 px-2 py-1 rounded-md text-xs whitespace-nowrap opacity-0 group-hover:opacity-100 transition-opacity pointer-events-none z-50 shadow-lg"
                style={{
                  backgroundColor: '#1a1a2e',
                  border: '1px solid #2d2d4a',
                  color: '#e2e8f0',
                }}
              >
                <div className="font-medium">{user.userName}</div>
                <span
                  className="inline-block px-1.5 py-0.5 rounded text-[10px] font-medium mt-1"
                  style={{
                    backgroundColor: permStyle.bg,
                    color: permStyle.text,
                    border: `1px solid ${permStyle.border}`,
                  }}
                >
                  {getPermissionLabel(user.permission)}
                </span>
                {isIdle && (
                  <span className="ml-1 text-[10px]" style={{ color: '#94a3b8' }}>
                    Idle
                  </span>
                )}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}
