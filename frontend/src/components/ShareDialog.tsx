/**
 * Hermes Canopy — Share Dialog
 *
 * Modal dialog for sharing a tree with other users.
 *   - Email input + permission dropdown → send invite
 *   - List of existing members with permission change + remove
 *
 * WIRE-004 / BUG-024: POSTs to the real /api/v1/trees/:id/share endpoint.
 */

import { useState, useCallback } from 'react';
import { X, Mail, Shield, Trash2, Loader2 } from 'lucide-react';
import { token } from '../theme.ts';
import type { PermissionLevel, ShareInvitePayload } from '../types/multiUser.ts';
import {
  getPermissionLabel,
  getPermissionStyle,
  getUserInitials,
  getColorForUser,
} from '../types/multiUser.ts';

// ─── Mock member type ──────────────────────────────────────────────────

interface Member {
  userId: string;
  userName: string;
  email: string;
  permission: PermissionLevel;
  avatarColor: string;
}

// ─── Props ─────────────────────────────────────────────────────────────

export interface ShareDialogProps {
  /** Whether the dialog is open */
  open: boolean;
  /** Called to close the dialog */
  onClose: () => void;
  /** Tree ID for API calls */
  treeId: string;
  /** Current members with permissions */
  members: Member[];
  /** Called when a member's permission changes */
  onPermissionChange: (userId: string, permission: PermissionLevel) => void;
  /** Called when a member is removed */
  onRemoveMember: (userId: string) => void;
  /** Called when an invite is sent (Placeholder) */
  onInvite: (payload: ShareInvitePayload) => void;
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

// ─── Component ─────────────────────────────────────────────────────────

export default function ShareDialog({
  open,
  onClose,
  treeId,
  members,
  onPermissionChange,
  onRemoveMember,
  onInvite,
}: ShareDialogProps) {
  const [email, setEmail] = useState('');
  const [permission, setPermission] = useState<PermissionLevel>('editor');
  const [message, setMessage] = useState('');
  const [sending, setSending] = useState(false);
  const [status, setStatus] = useState<'idle' | 'success' | 'error'>('idle');
  const [statusText, setStatusText] = useState('');

  // ── Reset on close ────────────────────────────────────────────────
  const handleClose = useCallback(() => {
    setEmail('');
    setPermission('editor');
    setMessage('');
    setStatus('idle');
    setStatusText('');
    onClose();
  }, [onClose]);

  // ── Send invite (real POST to /api/v1/trees/:id/share) ────────────
  const handleSendInvite = useCallback(async () => {
    if (!email.trim()) return;

    setSending(true);
    setStatus('idle');
    setStatusText('');

    const payload: ShareInvitePayload = {
      email: email.trim(),
      permission,
      message: message.trim() || undefined,
    };

    try {
      const response = await fetch(
        `/api/v1/trees/${encodeURIComponent(treeId)}/share`,
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload),
          credentials: 'include',
        },
      );

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

      setStatus('success');
      setStatusText(`Invitation sent to ${payload.email}`);
      onInvite(payload);
      setEmail('');
      setMessage('');
    } catch (err) {
      // Network error / API down.
      const detail =
        err instanceof Error ? err.message : 'Network error — try again';
      setStatus('error');
      setStatusText(detail);
    } finally {
      setSending(false);
    }
  }, [email, permission, message, treeId, onInvite]);

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
                Invite by email
              </h3>

              {/* Email input */}
              <div className="relative">
                <Mail
                  className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4"
                  style={{ color: token.contentFaint }}
                  aria-hidden="true"
                />
                <input
                  type="email"
                  id="share-invite-email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder="colleague@example.com"
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
                />
              </div>

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
                disabled={!email.trim() || sending}
                className="w-full py-2 rounded-lg text-sm font-medium transition-all flex items-center justify-center gap-2"
                style={{
                  backgroundColor: email.trim()
                    ? 'var(--color-accent-2-600)'
                    : token.lineSubtle,
                  color: email.trim() ? '#ffffff' : token.contentFaint,
                  cursor: email.trim() ? 'pointer' : 'not-allowed',
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

            {/* ── Members list ─────────────────────────────────────── */}
            {members.length > 0 && (
              <div className="space-y-3">
                <h3 className="text-sm font-medium" style={{ color: token.contentMuted }}>
                  Members ({members.length})
                </h3>

                <div className="space-y-1.5 max-h-48 overflow-y-auto">
                  {members.map((member) => {
                    const initials = getUserInitials(member.userName);
                    const permStyle = getPermissionStyle(member.permission);
                    const color =
                      member.avatarColor || getColorForUser(member.userId);

                    return (
                      <div
                        key={member.userId}
                        className="flex items-center gap-3 px-3 py-2 rounded-lg"
                        style={{ backgroundColor: token.surfaceInput }}
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

                        {/* Name + email */}
                        <div className="flex-1 min-w-0">
                          <div
                            className="text-sm font-medium truncate"
                            style={{ color: token.contentPrimary }}
                          >
                            {member.userName}
                          </div>
                          <div
                            className="text-xs truncate"
                            style={{ color: token.contentFaint }}
                          >
                            {member.email}
                          </div>
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
                          {getPermissionLabel(member.permission)}
                        </span>

                        {/* Permission change dropdown */}
                        <select
                          value={member.permission}
                          onChange={(e) =>
                            onPermissionChange(
                              member.userId,
                              e.target.value as PermissionLevel,
                            )
                          }
                          className="flex-shrink-0 px-1.5 py-1 rounded text-xs border outline-none cursor-pointer"
                          style={{
                            backgroundColor: token.surfaceRaised,
                            borderColor: token.lineSubtle,
                            color: token.contentMuted,
                            maxWidth: 80,
                          }}
                        >
                          <option value="viewer">Viewer</option>
                          <option value="editor">Editor</option>
                          <option value="admin">Admin</option>
                        </select>

                        {/* Remove button */}
                        <button
                          onClick={() => onRemoveMember(member.userId)}
                          className="flex-shrink-0 p-1 rounded transition-colors hover:bg-red-500/10"
                          style={{ color: token.contentFaint }}
                          title={`Remove ${member.userName}`}
                          aria-label={`Remove ${member.userName}`}
                        >
                          <Trash2 className="w-3.5 h-3.5 hover:text-red-400" />
                        </button>
                      </div>
                    );
                  })}
                </div>
              </div>
            )}

            {/* Empty members state */}
            {members.length === 0 && (
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
    </>
  );
}
