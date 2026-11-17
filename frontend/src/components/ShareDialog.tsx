/**
 * Hermes Canopy — Share Dialog
 *
 * Modal dialog for sharing a tree with other users.
 *   - Invitee input (email OR user UUID) + permission dropdown → send invite
 *   - The tree's REAL membership roster with permission change + remove
 *
 * WIRE-004 / BUG-024: the invite POSTs to the real
 * `/api/v1/trees/:id/share` endpoint.
 *
 * CAN-MEM-03: the roster is real too. The member list used to be seeded from
 * a hard-coded demo roster in TreeView; the dialog now owns the data and reads
 * it from `GET /api/v1/trees/:id/members` (SPEC-API-06 §9) every time it opens,
 * with role changes via `PATCH .../members/:member_id` (§10) and removals via
 * `DELETE .../members/:member_id` (§11) — each followed by a fresh read of the
 * roster, so what is rendered is always what the server stores (no optimistic
 * rows, no fabricated members). A failed read renders an explicit error with a
 * Retry affordance instead of an empty list.
 *
 * DF-HERMES-CANOPY-89: a value that parses as a UUID is sent as `user_id`
 * instead of `email`. Dev-mode provisioning creates the account without an
 * email, so `user_id` is the only path that can complete an invite for a user
 * who has signed in; the email path still works for users that have one.
 */

import { useCallback, useEffect, useState } from 'react';
import { X, Mail, Shield, Trash2, Loader2, AlertTriangle, RefreshCw } from 'lucide-react';
import { token } from '../theme.ts';
import { apiUrl, authInit } from '../lib/api.ts';
import { isUuidLike } from '../lib/nodeMeta.ts';
import {
  fetchTreeMembers,
  removeTreeMember,
  updateTreeMember,
  type TreeMember,
} from '../lib/treeMembers.ts';
import type {
  PermissionLevel,
  ShareInvitePayload,
  ShareInviteResult,
} from '../types/multiUser.ts';
import {
  getPermissionLabel,
  getPermissionStyle,
  getUserInitials,
} from '../types/multiUser.ts';

// ─── Props ─────────────────────────────────────────────────────────────

export interface ShareDialogProps {
  /** Whether the dialog is open */
  open: boolean;
  /** Called to close the dialog */
  onClose: () => void;
  /** Tree ID for API calls */
  treeId: string;
  /**
   * Called when an invite succeeds, with the request payload and — when the
   * server answered a parseable 201 body — the created share record. The
   * roster itself is re-read from the API, so the parent does not have to
   * merge anything into a member list.
   */
  onInvite: (payload: ShareInvitePayload, result?: ShareInviteResult) => void;
}

// ─── Permission options ────────────────────────────────────────────────

const PERMISSION_OPTIONS: Array<{
  value: PermissionLevel;
  label: string;
}> = [
  { value: 'viewer', label: 'Viewer — read only' },
  { value: 'editor', label: 'Editor — can edit' },
  { value: 'admin', label: 'Admin — full access' },
];

// ─── Roster state ──────────────────────────────────────────────────────

/**
 * `loading` until the first read settles; `error` keeps the failed read's
 * reason on screen (with Retry) and NEVER degrades into an empty list.
 */
type RosterState = 'loading' | 'ready' | 'error';

// ─── Component ─────────────────────────────────────────────────────────

export default function ShareDialog({
  open,
  onClose,
  treeId,
  onInvite,
}: ShareDialogProps) {
  const [invitee, setInvitee] = useState('');
  const [permission, setPermission] = useState<PermissionLevel>('editor');
  const [message, setMessage] = useState('');
  const [sending, setSending] = useState(false);
  const [status, setStatus] = useState<'idle' | 'success' | 'error'>('idle');
  const [statusText, setStatusText] = useState('');

  // Real roster (CAN-MEM-03) — starts EMPTY and is filled from the API.
  const [members, setMembers] = useState<TreeMember[]>([]);
  const [rosterState, setRosterState] = useState<RosterState>('loading');
  const [rosterError, setRosterError] = useState('');
  /** Bumped by Retry and after every successful mutation to re-read. */
  const [rosterNonce, setRosterNonce] = useState(0);
  /** Membership id whose write is in flight (its row's controls go quiet). */
  const [busyMemberId, setBusyMemberId] = useState<string | null>(null);
  /** Last role/remove failure, shown under the roster. */
  const [actionError, setActionError] = useState('');

  // ── Read the roster (exactly one GET per open; re-read on demand) ──
  useEffect(() => {
    if (!open || treeId === '') return;

    // React 19 StrictMode double-invokes effects in dev; the flag makes the
    // abandoned run's result a no-op so the rendered roster is the newest one.
    let ignore = false;
    setRosterState('loading');
    setRosterError('');

    void fetchTreeMembers(treeId).then(
      (roster) => {
        if (ignore) return;
        setMembers(roster);
        setRosterState('ready');
      },
      (err: unknown) => {
        if (ignore) return;
        // Drop whatever was rendered before: a stale roster next to an error
        // reads as "these are the members", which is exactly the lie the mock
        // seeding produced.
        setMembers([]);
        setRosterState('error');
        setRosterError(err instanceof Error ? err.message : String(err));
      },
    );

    return () => {
      ignore = true;
    };
  }, [open, treeId, rosterNonce]);

  // ── Reset on close ────────────────────────────────────────────────
  const handleClose = useCallback(() => {
    setInvitee('');
    setPermission('editor');
    setMessage('');
    setStatus('idle');
    setStatusText('');
    setActionError('');
    onClose();
  }, [onClose]);

  const retryRoster = useCallback(() => {
    setRosterNonce((n) => n + 1);
  }, []);

  // ── Send invite (real POST to /api/v1/trees/:id/share) ────────────
  const handleSendInvite = useCallback(async () => {
    const target = invitee.trim();
    if (!target) return;

    setSending(true);
    setStatus('idle');
    setStatusText('');

    // DF-HERMES-CANOPY-89: a UUID is the user_id path — the only one that can
    // reach an account provisioned by signing in (those have no email). Any
    // other value is the email path. The backend resolves user_id first.
    const payload: ShareInvitePayload = isUuidLike(target)
      ? { user_id: target, permission, message: message.trim() || undefined }
      : { email: target, permission, message: message.trim() || undefined };

    try {
      const response = await fetch(apiUrl(`/trees/${encodeURIComponent(treeId)}/share`), authInit({
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
        credentials: 'include',
      }));

      if (!response.ok) {
        // Extract the coded error message from the backend envelope.
        let detail = `Server error (${response.status})`;
        try {
          const body = (await response.json()) as {
            error?: { message?: string };
          };
          if (body?.error?.message) detail = body.error.message;
        } catch {
          // Non-JSON error body — keep the generic status text.
        }
        setStatus('error');
        setStatusText(detail);
        setSending(false);
        return;
      }

      // The 201 body names the membership that was created. Best-effort: a 2xx
      // with an unparseable body still succeeded, so it never fails the invite.
      let result: ShareInviteResult | undefined;
      try {
        result = (await response.json()) as ShareInviteResult;
      } catch {
        result = undefined;
      }

      setStatus('success');
      setStatusText(`Invitation sent to ${result?.email || target}`);
      onInvite(payload, result);
      setInvitee('');
      setMessage('');
      // The roster is server-owned: re-read it so the new member appears with
      // the role the membership row actually carries.
      setRosterNonce((n) => n + 1);
    } catch (err) {
      // Network error / API down.
      const detail =
        err instanceof Error ? err.message : 'Network error — try again';
      setStatus('error');
      setStatusText(detail);
    } finally {
      setSending(false);
    }
  }, [invitee, permission, message, treeId, onInvite]);

  // ── Role change → PATCH, then re-read the roster ──────────────────
  const handlePermissionChange = useCallback(
    async (member: TreeMember, next: PermissionLevel) => {
      if (busyMemberId !== null || next === member.permission) return;
      setBusyMemberId(member.memberId);
      setActionError('');
      try {
        await updateTreeMember(treeId, member.memberId, next);
        setRosterNonce((n) => n + 1);
      } catch (err) {
        // No local mutation happened, so the row still shows the stored role;
        // the failure is named instead of being swallowed.
        setActionError(
          `Could not change ${member.userName}'s role: ${
            err instanceof Error ? err.message : String(err)
          }`,
        );
      } finally {
        setBusyMemberId(null);
      }
    },
    [busyMemberId, treeId],
  );

  // ── Remove → DELETE, then re-read the roster ──────────────────────
  const handleRemoveMember = useCallback(
    async (member: TreeMember) => {
      if (busyMemberId !== null) return;
      setBusyMemberId(member.memberId);
      setActionError('');
      try {
        await removeTreeMember(treeId, member.memberId);
        setRosterNonce((n) => n + 1);
      } catch (err) {
        setActionError(
          `Could not remove ${member.userName}: ${
            err instanceof Error ? err.message : String(err)
          }`,
        );
      } finally {
        setBusyMemberId(null);
      }
    },
    [busyMemberId, treeId],
  );

  if (!open) return null;

  return (
    <>
      {/* Backdrop */}
      <div
        className="fixed inset-0 z-50 flex items-center justify-center"
        style={{ backgroundColor: 'rgba(0,0,0,0.6)' }}
        onClick={handleClose}
      >
        {/* Dialog */}
        <div
          className="relative w-full max-w-md mx-4 rounded-xl border shadow-2xl"
          style={{
            backgroundColor: token.surfaceRaised,
            borderColor: token.lineSubtle,
          }}
          onClick={(e) => e.stopPropagation()}
        >
          {/* Header */}
          <div
            className="flex items-center justify-between px-5 py-4 border-b"
            style={{ borderColor: token.lineSubtle }}
          >
            <div className="flex items-center gap-2">
              <Shield className="w-4 h-4" style={{ color: token.accent2 }} />
              <h2 className="text-base font-semibold" style={{ color: token.contentPrimary }}>
                Share Tree
              </h2>
            </div>
            <button
              onClick={handleClose}
              className="p-1 rounded-md transition-colors hover:bg-white/5"
              style={{ color: token.contentMuted }}
              aria-label="Close share dialog"
            >
              <X className="w-4 h-4" />
            </button>
          </div>

          {/* Body */}
          <div className="px-5 py-4 space-y-4">
            {/* ── Invite form ──────────────────────────────────────── */}
            <div className="space-y-3">
              <h3 className="text-sm font-medium" style={{ color: token.contentMuted }}>
                Invite by email or user ID
              </h3>

              {/* Invitee input — accepts an email address or a user UUID */}
              <div className="relative">
                <Mail
                  className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4"
                  style={{ color: token.contentFaint }}
                  aria-hidden="true"
                />
                <input
                  type="text"
                  id="share-invite-email"
                  value={invitee}
                  onChange={(e) => setInvitee(e.target.value)}
                  placeholder="colleague@example.com or user UUID"
                  className="w-full pl-10 pr-3 py-2 rounded-lg border text-sm outline-none transition-colors focus:ring-1 focus:ring-accent"
                  style={{
                    backgroundColor: token.surfaceInput,
                    borderColor: token.lineSubtle,
                    color: token.contentPrimary,
                  }}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') void handleSendInvite();
                  }}
                  aria-required="true"
                  aria-describedby="share-invite-hint"
                />
              </div>

              {/* Why a UUID is offered: an account created by signing in carries
                  no email, so an email invite can never resolve it. */}
              <p
                id="share-invite-hint"
                className="text-xs leading-snug"
                style={{ color: token.contentFaint }}
              >
                They must have signed in to this instance once. Accounts created
                that way have no email on record — invite those by their user
                UUID.
              </p>

              {/* Permission select */}
              <label htmlFor="share-invite-permission" className="sr-only">Permission level</label>
              <select
                id="share-invite-permission"
                value={permission}
                onChange={(e) => setPermission(e.target.value as PermissionLevel)}
                className="w-full px-3 py-2 rounded-lg border text-sm outline-none transition-colors focus:ring-1 focus:ring-accent cursor-pointer"
                style={{
                  backgroundColor: token.surfaceInput,
                  borderColor: token.lineSubtle,
                  color: token.contentPrimary,
                }}
              >
                {PERMISSION_OPTIONS.map((opt) => (
                  <option key={opt.value} value={opt.value}>
                    {opt.label}
                  </option>
                ))}
              </select>

              {/* Optional message */}
              <label htmlFor="share-invite-message" className="sr-only">Personal message (optional)</label>
              <input
                id="share-invite-message"
                type="text"
                value={message}
                onChange={(e) => setMessage(e.target.value)}
                placeholder="Add a personal message (optional)"
                className="w-full px-3 py-2 rounded-lg border text-sm outline-none transition-colors focus:ring-1 focus:ring-accent"
                style={{
                  backgroundColor: token.surfaceInput,
                  borderColor: token.lineSubtle,
                  color: token.contentPrimary,
                }}
              />

              {/* Status message */}
              {status !== 'idle' && (
                <div
                  className="px-3 py-2 rounded-lg text-xs"
                  style={{
                    backgroundColor:
                      status === 'success'
                        ? 'rgba(34, 197, 94, 0.1)'
                        : 'rgba(239, 68, 68, 0.1)',
                    color:
                      status === 'success' ? token.success : token.danger,
                  }}
                >
                  {statusText}
                </div>
              )}

              {/* Send button */}
              <button
                onClick={() => void handleSendInvite()}
                disabled={!invitee.trim() || sending}
                className="w-full py-2 rounded-lg text-sm font-medium transition-all flex items-center justify-center gap-2"
                style={{
                  backgroundColor: invitee.trim()
                    ? 'var(--color-accent-2-600)'
                    : token.lineSubtle,
                  color: invitee.trim() ? '#ffffff' : token.contentFaint,
                  cursor: invitee.trim() ? 'pointer' : 'not-allowed',
                  opacity: sending ? 0.7 : 1,
                }}
              >
                {sending ? (
                  <>
                    <Loader2 className="w-4 h-4 animate-spin" />
                    Sending...
                  </>
                ) : (
                  'Send Invite'
                )}
              </button>
            </div>

            {/* ── Members (real roster: GET /trees/:id/members) ────── */}
            <div className="space-y-3">
              <h3 className="text-sm font-medium" style={{ color: token.contentMuted }}>
                {rosterState === 'ready' ? `Members (${members.length})` : 'Members'}
              </h3>

              {rosterState === 'loading' && (
                <div
                  className="flex items-center gap-2 px-3 py-2 text-xs"
                  style={{ color: token.contentFaint }}
                >
                  <Loader2 className="w-3.5 h-3.5 animate-spin" aria-hidden="true" />
                  Loading members…
                </div>
              )}

              {rosterState === 'error' && (
                <div
                  className="px-3 py-2 rounded-lg text-xs space-y-2"
                  role="alert"
                  data-testid="share-members-error"
                  style={{
                    backgroundColor: 'rgba(239, 68, 68, 0.1)',
                    color: token.danger,
                  }}
                >
                  <div className="flex items-start gap-2">
                    <AlertTriangle className="w-3.5 h-3.5 mt-0.5 shrink-0" aria-hidden="true" />
                    <span>{`Could not load members: ${rosterError}`}</span>
                  </div>
                  <button
                    onClick={retryRoster}
                    className="flex items-center gap-1 px-2 py-1 rounded border text-xs font-medium transition-colors hover:bg-white/5"
                    style={{
                      borderColor: token.lineSubtle,
                      color: token.contentPrimary,
                    }}
                  >
                    <RefreshCw className="w-3 h-3" aria-hidden="true" />
                    Retry
                  </button>
                </div>
              )}

              {actionError !== '' && (
                <div
                  className="px-3 py-2 rounded-lg text-xs"
                  role="alert"
                  data-testid="share-action-error"
                  style={{
                    backgroundColor: 'rgba(239, 68, 68, 0.1)',
                    color: token.danger,
                  }}
                >
                  {actionError}
                </div>
              )}

              {rosterState === 'ready' && members.length > 0 && (
                <div className="space-y-1.5 max-h-48 overflow-y-auto">
                  {members.map((member) => {
                    const initials = getUserInitials(member.userName);
                    const permStyle = getPermissionStyle(member.permission);
                    const color =
                      member.avatarColor || token.surfaceInput;
                    // `owner` is not an assignable role on the members routes
                    // (SPEC-API-06 §10.2) — the ownership row is read-only.
                    const isOwner = member.role === 'owner';
                    const busy = busyMemberId === member.memberId;

                    return (
                      <div
                        key={member.memberId}
                        data-testid={`share-member-${member.memberId}`}
                        className="flex items-center gap-3 px-3 py-2 rounded-lg"
                        style={{
                          backgroundColor: token.surfaceInput,
                          opacity: busy ? 0.6 : 1,
                        }}
                      >
                        {/* Avatar */}
                        <div
                          className="flex-shrink-0 flex items-center justify-center rounded-full text-xs font-semibold"
                          style={{
                            width: 28,
                            height: 28,
                            backgroundColor: color,
                            color: '#ffffff',
                          }}
                        >
                          {initials}
                        </div>

                        {/* Name + identity */}
                        <div className="flex-1 min-w-0">
                          <div
                            className="text-sm font-medium truncate"
                            style={{ color: token.contentPrimary }}
                          >
                            {member.userName}
                          </div>
                          {member.detail !== '' && (
                            <div
                              className="text-xs truncate"
                              style={{ color: token.contentFaint }}
                            >
                              {member.detail}
                            </div>
                          )}
                        </div>

                        {/* Permission badge */}
                        <span
                          className="flex-shrink-0 px-2 py-0.5 rounded text-[10px] font-medium"
                          style={{
                            backgroundColor: permStyle.bg,
                            color: permStyle.text,
                            border: `1px solid ${permStyle.border}`,
                          }}
                        >
                          {isOwner ? 'Owner' : getPermissionLabel(member.permission)}
                        </span>

                        {/* Permission change dropdown */}
                        <select
                          value={member.permission}
                          disabled={isOwner || busy}
                          onChange={(e) =>
                            void handlePermissionChange(
                              member,
                              e.target.value as PermissionLevel,
                            )
                          }
                          aria-label={`Permission for ${member.userName}`}
                          className="flex-shrink-0 px-1.5 py-1 rounded text-xs border outline-none cursor-pointer"
                          style={{
                            backgroundColor: token.surfaceRaised,
                            borderColor: token.lineSubtle,
                            color: token.contentMuted,
                            maxWidth: 80,
                            cursor: isOwner || busy ? 'not-allowed' : 'pointer',
                          }}
                        >
                          <option value="viewer">Viewer</option>
                          <option value="editor">Editor</option>
                          <option value="admin">Admin</option>
                        </select>

                        {/* Remove button */}
                        <button
                          onClick={() => void handleRemoveMember(member)}
                          disabled={isOwner || busy}
                          className="flex-shrink-0 p-1 rounded transition-colors hover:bg-red-500/10 disabled:opacity-40"
                          style={{ color: token.contentFaint }}
                          title={
                            isOwner
                              ? 'Ownership cannot be removed here'
                              : `Remove ${member.userName}`
                          }
                          aria-label={`Remove ${member.userName}`}
                        >
                          <Trash2 className="w-3.5 h-3.5 hover:text-red-400" />
                        </button>
                      </div>
                    );
                  })}
                </div>
              )}

              {/* Empty roster — only once a read has actually succeeded. */}
              {rosterState === 'ready' && members.length === 0 && (
                <div
                  className="text-center py-4 text-xs"
                  style={{ color: token.contentFaint }}
                >
                  No members yet. Share this tree to collaborate.
                </div>
              )}
            </div>
          </div>
        </div>
      </div>
    </>
  );
}
