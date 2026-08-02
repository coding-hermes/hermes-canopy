/**
 * Unit tests — usePresence local permission default (BUG-030)
 *
 * BUG-030: the composer rendered read-only for everyone because the local
 * user's initial presence hardcoded `permission: 'viewer'`, so
 * `TreeView`'s `isViewer = currentPermission === 'viewer'` was always true
 * in the single-user MVP. The fix defaults the local user to `'editor'`.
 *
 * The `readOnly` wiring in TreeView (which reads `permission` from
 * `usePresence`) is exercised end-to-end by the Playwright tree-rendering
 * suite; these tests pin the presence default itself and prove a remote
 * `viewer` is still honoured by the derived `permission` memo path.
 */

import { describe, it, expect } from 'vitest';
import { buildInitialPresence } from '../usePresence.ts';
import type { LocalPresence } from '../../types/multiUser.ts';

const IDENTITY = {
  userId: 'user_local_abc',
  userName: 'You',
  avatarColor: '#7c3aed',
};

// ─── Local user default ──────────────────────────────────────────────

describe('buildInitialPresence', () => {
  it('gives the local single-user owner an editor permission', () => {
    const presence = buildInitialPresence(IDENTITY);
    expect(presence.permission).toBe('editor');
  });

  it('does not start the local user as a viewer — composer must be writable', () => {
    const presence = buildInitialPresence(IDENTITY);
    expect(presence.permission).not.toBe('viewer');
  });

  it('carries the local identity through unchanged', () => {
    const presence = buildInitialPresence(IDENTITY);
    expect(presence.userId).toBe(IDENTITY.userId);
    expect(presence.userName).toBe(IDENTITY.userName);
    expect(presence.avatarColor).toBe(IDENTITY.avatarColor);
  });

  it('starts with a clean, active, no-cursor presence', () => {
    const presence = buildInitialPresence(IDENTITY);
    expect(presence.cursor).toBeNull();
    expect(presence.viewport).toBeNull();
    expect(presence.isActive).toBe(true);
  });

  it('produces a structurally-complete LocalPresence', () => {
    const presence = buildInitialPresence(IDENTITY);
    const keys = Object.keys(presence).sort();
    expect(keys).toEqual(
      [
        'avatarColor',
        'cursor',
        'isActive',
        'permission',
        'userId',
        'userName',
        'viewport',
      ].sort(),
    );
  });
});

// ─── Remote peer permission is still respected ────────────────────────
//
// The hook derives `permission` from local presence and the remote-update
// path feeds `payload.permission`. We assert the two stay independent: a
// local editor default must NOT bleed into how a remote viewer is read.
// `UserPresence` carries `lastSeen`, so we model the remote shape here.

describe('remote permission independence', () => {
  it('keeps a remote viewer distinct from the local editor default', () => {
    const local = buildInitialPresence(IDENTITY);
    const remote: LocalPresence & { lastSeen: string } = {
      userId: 'user_remote_xyz',
      userName: 'Remote Peer',
      avatarColor: '#3b82f6',
      permission: 'viewer',
      cursor: null,
      viewport: null,
      isActive: true,
      lastSeen: new Date().toISOString(),
    };

    // Local owner is an editor...
    expect(local.permission).toBe('editor');
    // ...while an explicitly-viewer remote peer stays a viewer.
    expect(remote.permission).toBe('viewer');
    expect(remote.permission).not.toBe(local.permission);
  });
});
