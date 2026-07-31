/**
 * Hermes Canopy — Share Dialog
 *
 * Modal dialog for sharing a tree with other users.
 *   - Email input + permission dropdown → send invite
 *   - List of existing members with permission change + remove
 *
 * Backend share endpoint (POST /api/v1/trees/:id/share) is planned but not
 * yet implemented.  The dialog simulates success locally so the UI is
 * fully functional for demos and UX reviews.
 *
 * TODO(BUG-024): Wire up real POST /api/v1/trees/:id/share when backend ships.
 */

import { useState, useCallback } from 'react';
import { X, Mail, Shield, Trash2, Loader2 } from 'lucide-react';
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

  // ── Send invite (coming soon — share API not yet implemented) ──────
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

    // Backend share endpoint is planned but not yet implemented.
    // Simulate a brief network-like delay so the UI feels responsive.
    console.log(
      `[ShareDialog] Share invite (coming soon): ${payload.email} / ${payload.permission} / tree=${treeId}`,
    );

    await new Promise((resolve) => setTimeout(resolve, 400));

    setStatus('success');
    setStatusText(`Invitation sent to ${payload.email}`);
    onInvite(payload);
    setEmail('');
    setMessage('');
    setSending(false);
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
            backgroundColor: '#0f0f23',
            borderColor: '#2d2d4a',
          }}
          onClick={(e) => e.stopPropagation()}
        >
          {/* Header */}
          <div
            className="flex items-center justify-between px-5 py-4 border-b"
            style={{ borderColor: '#2d2d4a' }}
          >
            <div className="flex items-center gap-2">
              <Shield className="w-4 h-4" style={{ color: '#7c3aed' }} />
              <h2 className="text-base font-semibold" style={{ color: '#e2e8f0' }}>
                Share Tree
              </h2>
            </div>
            <button
              onClick={handleClose}
              className="p-1 rounded-md transition-colors hover:bg-white/5"
              style={{ color: '#94a3b8' }}
              aria-label="Close share dialog"
            >
              <X className="w-4 h-4" />
            </button>
          </div>

          {/* Body */}
          <div className="px-5 py-4 space-y-4">
            {/* ── Invite form ──────────────────────────────────────── */}
            <div className="space-y-3">
              <h3 className="text-sm font-medium" style={{ color: '#94a3b8' }}>
                Invite by email
              </h3>

              {/* Email input */}
              <div className="relative">
                <Mail
                  className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4"
                  style={{ color: '#4a4a6a' }}
                  aria-hidden="true"
                />
                <input
                  type="email"
                  id="share-invite-email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder="colleague@example.com"
                  className="w-full pl-10 pr-3 py-2 rounded-lg border text-sm outline-none transition-colors focus:ring-1 focus:ring-[#7c3aed]"
                  style={{
                    backgroundColor: '#1a1a2e',
                    borderColor: '#2d2d4a',
                    color: '#e2e8f0',
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
                className="w-full px-3 py-2 rounded-lg border text-sm outline-none transition-colors focus:ring-1 focus:ring-[#7c3aed] cursor-pointer"
                style={{
                  backgroundColor: '#1a1a2e',
                  borderColor: '#2d2d4a',
                  color: '#e2e8f0',
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
                className="w-full px-3 py-2 rounded-lg border text-sm outline-none transition-colors focus:ring-1 focus:ring-[#7c3aed]"
                style={{
                  backgroundColor: '#1a1a2e',
                  borderColor: '#2d2d4a',
                  color: '#e2e8f0',
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
                      status === 'success' ? '#22c55e' : '#ef4444',
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
                    ? '#7c3aed'
                    : '#2d2d4a',
                  color: email.trim() ? '#ffffff' : '#4a4a6a',
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
                <h3 className="text-sm font-medium" style={{ color: '#94a3b8' }}>
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
                        style={{ backgroundColor: '#1a1a2e' }}
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
                            style={{ color: '#e2e8f0' }}
                          >
                            {member.userName}
                          </div>
                          <div
                            className="text-xs truncate"
                            style={{ color: '#4a4a6a' }}
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
                            backgroundColor: '#0f0f23',
                            borderColor: '#2d2d4a',
                            color: '#94a3b8',
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
                          style={{ color: '#4a4a6a' }}
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
                style={{ color: '#4a4a6a' }}
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
