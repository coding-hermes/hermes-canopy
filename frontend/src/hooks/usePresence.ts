/**
 * Hermes Canopy — usePresence Hook
 *
 * React hook for multi-user presence awareness.
 * Bridges SSESyncProvider presence state into React.
 * Auto-generates local user identity on first mount.
 *
 * Usage:
 *   const { remotePresence, localPresence, updateCursor, permission } =
 *     usePresence(providerRef.current, 'Alice');
 */

import { useState, useEffect, useCallback, useRef, useMemo } from 'react';
import type { SSESyncProvider } from '../stores/yjsProvider.ts';
import type {
  UserPresence,
  LocalPresence,
  CursorPosition,
  PermissionLevel,
} from '../types/multiUser.ts';
import { getColorForUser } from '../types/multiUser.ts';

// ─── Types ─────────────────────────────────────────────────────────────

export interface UsePresenceReturn {
  /** Read-only snapshot of remote user presence states */
  remotePresence: ReadonlyMap<string, UserPresence>;
  /** Local user presence state */
  localPresence: LocalPresence | null;
  /** Update local presence (merges with current) */
  setLocalPresence: (presence: Partial<LocalPresence>) => void;
  /** Debounced cursor position update */
  updateCursor: (cursor: CursorPosition) => void;
  /** Local user ID (auto-generated) */
  userId: string;
  /** Local user display name */
  userName: string;
  /** Avatar color for local user */
  avatarColor: string;
  /** Current permission level */
  permission: PermissionLevel;
}

// ─── Helpers ───────────────────────────────────────────────────────────

/** Generate a unique user ID (no external dependency). */
function generateUserId(): string {
  const rand = Math.random().toString(36).slice(2, 11);
  const ts = Date.now().toString(36);
  return `user_${rand}_${ts}`;
}

// ─── Initial presence builder ────────────────────────────────────────

/**
 * Build the local user's initial presence on mount.
 *
 * The local user owns the tree in the single-user MVP, so we default to
 * `'editor'` — there is no membership/role endpoint to derive a role from
 * yet (post-MVP). Remote peers still arrive via presence updates carrying
 * their own `permission`, which the remote-update path respects downstream.
 */
export function buildInitialPresence(identity: {
  userId: string;
  userName: string;
  avatarColor: string;
}): LocalPresence {
  return {
    userId: identity.userId,
    userName: identity.userName,
    avatarColor: identity.avatarColor,
    permission: 'editor',
    cursor: null,
    viewport: null,
    isActive: true,
  };
}

// ─── Hook ──────────────────────────────────────────────────────────────

export function usePresence(
  provider: SSESyncProvider | null,
  userName: string,
): UsePresenceReturn {
  // ── Local identity (stable across renders) ────────────────────────
  const identityRef = useRef({
    userId: generateUserId(),
    userName,
    avatarColor: '',
  });

  // Set avatar color on first render
  if (!identityRef.current.avatarColor) {
    identityRef.current.avatarColor = getColorForUser(identityRef.current.userId);
  }

  const providerRef = useRef(provider);
  providerRef.current = provider;

  const [remotePresence, setRemotePresence] = useState<
    ReadonlyMap<string, UserPresence>
  >(new Map());
  const [localPresenceState, setLocalPresenceState] =
    useState<LocalPresence | null>(null);

  // ── Poll remote presence (provider doesn't expose event subscription) ──
  useEffect(() => {
    if (!provider) return;

    const interval = setInterval(() => {
      const current = provider.getRemotePresence();
      setRemotePresence(new Map(current));
    }, 500);

    return () => clearInterval(interval);
  }, [provider]);

  // ── Set local presence ────────────────────────────────────────────
  const setLocalPresence = useCallback(
    (presence: Partial<LocalPresence>) => {
      const p = providerRef.current;
      if (!p) return;

      const id = identityRef.current;
      const prev = localPresenceState;

      const full: LocalPresence = {
        userId: id.userId,
        userName: presence.userName ?? prev?.userName ?? id.userName,
        avatarColor: id.avatarColor,
        permission: presence.permission ?? prev?.permission ?? 'viewer',
        cursor: presence.cursor ?? prev?.cursor ?? null,
        viewport: presence.viewport ?? prev?.viewport ?? null,
        isActive: presence.isActive ?? prev?.isActive ?? true,
      };

      p.setLocalPresence(full);
      setLocalPresenceState(full);
    },
    [localPresenceState],
  );

  // ── Update cursor ─────────────────────────────────────────────────
  const updateCursor = useCallback(
    (cursor: CursorPosition) => {
      providerRef.current?.updateCursor(cursor);
    },
    [],
  );

  // ── Auto-set initial local presence on mount ──────────────────────
  useEffect(() => {
    const p = providerRef.current;
    if (!p) return;

    const id = identityRef.current;
    const initial = buildInitialPresence(id);

    p.setLocalPresence(initial);
    setLocalPresenceState(initial);

    // Send leave on unmount
    return () => {
      p.clearLocalPresence();
    };
  }, [provider]);

  // ── Derived ───────────────────────────────────────────────────────
  const permission: PermissionLevel = useMemo(
    () => localPresenceState?.permission ?? 'viewer',
    [localPresenceState],
  );

  return {
    remotePresence,
    localPresence: localPresenceState,
    setLocalPresence,
    updateCursor,
    userId: identityRef.current.userId,
    userName: identityRef.current.userName,
    avatarColor: identityRef.current.avatarColor,
    permission,
  };
}
