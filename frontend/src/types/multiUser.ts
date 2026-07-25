/**
 * Hermes Canopy — Multi-User Types
 *
 * Types for presence, collaborative cursors, permissions, and sharing.
 * Used by usePresence hook, PresenceBar, CollaborativeCursors, and ShareDialog.
 */

// ─── Permission Levels ──────────────────────────────────────────────

export type PermissionLevel = 'viewer' | 'editor' | 'admin';

// ─── User Presence ──────────────────────────────────────────────────

/**
 * Full presence state for a single user.
 * Broadcast via Yjs awareness / SSE transport.
 */
export interface UserPresence {
  /** Unique user identifier */
  userId: string;
  /** Display name */
  userName: string;
  /** Avatar background color (HSL or hex) */
  avatarColor: string;
  /** Permission level on this tree */
  permission: PermissionLevel;
  /** Cursor position on the React Flow canvas (viewport coords) */
  cursor: CursorPosition | null;
  /** User's viewport state */
  viewport: ViewportState | null;
  /** Whether the user is currently active (vs idle) */
  isActive: boolean;
  /** ISO timestamp of last activity */
  lastSeen: string;
}

// ─── Cursor / Viewport ──────────────────────────────────────────────

export interface CursorPosition {
  /** X position in React Flow coordinate space */
  x: number;
  /** Y position in React Flow coordinate space */
  y: number;
}

export interface ViewportState {
  x: number;
  y: number;
  zoom: number;
}

// ─── Local presence (what we set for ourselves) ─────────────────────

export type LocalPresence = Omit<UserPresence, 'lastSeen'>;

// ─── Share Dialog ───────────────────────────────────────────────────

export interface ShareInvitePayload {
  email: string;
  permission: PermissionLevel;
  message?: string;
}

// ─── Presence store event types ─────────────────────────────────────

export type PresenceChangeHandler = (states: ReadonlyMap<string, UserPresence>) => void;

// ─── Color palette for avatars ──────────────────────────────────────

const AVATAR_COLORS = [
  '#7c3aed', // purple
  '#3b82f6', // blue
  '#22c55e', // green
  '#f59e0b', // amber
  '#ef4444', // red
  '#ec4899', // pink
  '#06b6d4', // cyan
  '#f97316', // orange
  '#8b5cf6', // violet
  '#10b981', // emerald
];

/** Assign a deterministic color from userId hash. */
export function getColorForUser(userId: string): string {
  let hash = 0;
  for (let i = 0; i < userId.length; i++) {
    hash = userId.charCodeAt(i) + ((hash << 5) - hash);
  }
  const idx = Math.abs(hash) % AVATAR_COLORS.length;
  return AVATAR_COLORS[idx] ?? AVATAR_COLORS[0];
}

/** Get initials from a user name (max 2 chars). */
export function getUserInitials(userName: string): string {
  const parts = userName.trim().split(/\s+/);
  if (parts.length >= 2) {
    return (parts[0]?.[0] ?? '') + (parts[1]?.[0] ?? '');
  }
  return (userName.trim().slice(0, 2) || '?').toUpperCase();
}

/** Permission level display label. */
export function getPermissionLabel(level: PermissionLevel): string {
  switch (level) {
    case 'admin':
      return 'Admin';
    case 'editor':
      return 'Editor';
    case 'viewer':
      return 'Viewer';
  }
}

/** Permission badge style lookup. */
export function getPermissionStyle(level: PermissionLevel): {
  bg: string;
  text: string;
  border: string;
} {
  switch (level) {
    case 'admin':
      return {
        bg: 'rgba(124, 58, 237, 0.15)',
        text: '#a78bfa',
        border: 'rgba(124, 58, 237, 0.4)',
      };
    case 'editor':
      return {
        bg: 'rgba(59, 130, 246, 0.15)',
        text: '#93c5fd',
        border: 'rgba(59, 130, 246, 0.4)',
      };
    case 'viewer':
      return {
        bg: 'rgba(148, 163, 184, 0.12)',
        text: '#94a3b8',
        border: 'rgba(148, 163, 184, 0.3)',
      };
  }
}
