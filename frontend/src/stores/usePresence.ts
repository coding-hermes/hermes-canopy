/**
 * Hermes Canopy — usePresence Hook
 *
 * React hook wrapping Yjs awareness / SSE-based presence tracking.
 * Provides:
 *   - Remote users' presence states (cursor, permission, etc.)
 *   - Methods to update local cursor position
 *   - Methods to set local user identity
 */

import { useEffect, useRef, useState, useCallback, useMemo } from 'react';
import type {
  UserPresence,
  LocalPresence,
  CursorPosition,
  ViewportState,
  PermissionLevel,
} from '../types/multiUser.ts';
import type { SSESyncProvider } from './yjsProvider.ts';

// ─── Hook Types ───────────────────────────────────────────────────────

export interface UsePresenceResult {
  /** Map of userId → UserPresence for all remote users */
  remoteUsers: ReadonlyMap<string, UserPresence>;
  /** Array of remote users (sorted by lastSeen) for rendering */
  remoteUserList: readonly UserPresence[];
  /** Number of connected remote users */
  remoteUserCount: number;
  /** Set our identity and start broadcasting presence */
  setLocalUser: (presence: LocalPresence) => void;
  /** Update cursor position (debounced internally) */
  updateCursor: (cursor: CursorPosition) => void;
  /** Update viewport state */
  updateViewport: (viewport: ViewportState) => void;
}

// ─── Hook Implementation ──────────────────────────────────────────────

export function usePresence(
  provider: SSESyncProvider | null,
): UsePresenceResult {
  const providerRef = useRef<SSESyncProvider | null>(provider);
  providerRef.current = provider;

  const [remoteUsers, setRemoteUsers] =
    useState<ReadonlyMap<string, UserPresence>>(new Map());

  // Subscribe to presence changes from the provider
  useEffect(() => {
    const p = providerRef.current;
    if (!p) {
      setRemoteUsers(new Map());
      return;
    }

    // Set the owner's presence change callback
    // We need to re-bind the callback — the provider stores it in options
    // Rather than exposing onPresenceChange on the instance, we poll initially
    // and rely on the SSE transport path.

    // Get initial state
    setRemoteUsers(p.getRemotePresence());

    // Since we can't re-set options after construction, we use a polling approach
    // for the hook. The provider handles server-side sync; the hook updates
    // whenever our component re-renders from the Yjs doc changes.
    const interval = setInterval(() => {
      const current = providerRef.current;
      if (current) {
        setRemoteUsers(current.getRemotePresence());
      }
    }, 2000); // Poll every 2s as fallback

    return () => clearInterval(interval);
  }, [provider]);

  const remoteUserList = useMemo(() => {
    return Array.from(remoteUsers.values()).sort(
      (a, b) => new Date(b.lastSeen).getTime() - new Date(a.lastSeen).getTime(),
    );
  }, [remoteUsers]);

  const remoteUserCount = remoteUserList.length;

  const setLocalUser = useCallback((presence: LocalPresence): void => {
    providerRef.current?.setLocalPresence(presence);
  }, []);

  const updateCursor = useCallback((cursor: CursorPosition): void => {
    providerRef.current?.updateCursor(cursor);
  }, []);

  const updateViewport = useCallback((viewport: ViewportState): void => {
    providerRef.current?.updateViewport(viewport);
  }, []);

  return {
    remoteUsers,
    remoteUserList,
    remoteUserCount,
    setLocalUser,
    updateCursor,
    updateViewport,
  };
}

// ─── Demo helpers (for dev / testing) ──────────────────────────────────

/**
 * Create a demo local presence for development/testing.
 * Uses a random userId so multiple browser tabs show up as different users.
 */
export function createDemoPresence(
  permission: PermissionLevel = 'editor',
): LocalPresence {
  // Use localStorage to persist userId across refreshes in dev
  let userId = localStorage.getItem('canopy-demo-userId');
  if (!userId) {
    userId = `user-${crypto.randomUUID().slice(0, 8)}`;
    localStorage.setItem('canopy-demo-userId', userId);
  }

  const userName = `Demo ${userId.slice(-4).toUpperCase()}`;
  const avatarColor = `hsl(${Math.abs(hashString(userId)) % 360}, 70%, 55%)`;

  return {
    userId,
    userName,
    avatarColor,
    permission,
    cursor: null,
    viewport: null,
    isActive: true,
  };
}

function hashString(str: string): number {
  let hash = 0;
  for (let i = 0; i < str.length; i++) {
    hash = str.charCodeAt(i) + ((hash << 5) - hash);
  }
  return hash;
}
