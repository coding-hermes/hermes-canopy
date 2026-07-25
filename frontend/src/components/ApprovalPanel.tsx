/**
 * Hermes Canopy — Approval Panel
 *
 * Main approval panel displaying pending items with approve/deny actions,
 * diff-view modal, and audit trail drill-down.
 * Routes to /approvals — wired in App.tsx.
 */

import { useState, useEffect, useCallback } from 'react';
import {
  Check,
  X,
  Eye,
  Clock,
  AlertCircle,
  RefreshCw,
  ChevronRight,
  Inbox,
} from 'lucide-react';
import type { ApprovalItem, TreeDiff, DiffField, AuditEntry } from '../types/approval.ts';
import { apiUrl } from '../types/approval.ts';
import ApprovalDiff from './ApprovalDiff.tsx';
import AuditTrail from './AuditTrail.tsx';

// ─── Props ──────────────────────────────────────────────────────────────

export interface ApprovalPanelProps {
  /** Optional className for the root wrapper */
  className?: string;
}

// ─── Helpers ────────────────────────────────────────────────────────────

/** Build tree-aware diff from API proposedChanges and previousState. */
function buildTreeDiff(item: ApprovalItem): TreeDiff {
  const fields: DiffField[] = [];
  const proposed = item.proposedChanges ?? {};
  const previous = item.previousState ?? {};

  // Collect all keys from both objects
  const allKeys = new Set([...Object.keys(proposed), ...Object.keys(previous)]);

  for (const key of allKeys) {
    const oldVal = previous[key];
    const newVal = proposed[key];

    const hasOld = key in previous;
    const hasNew = key in proposed;

    let kind: DiffField['kind'];
    if (!hasOld && hasNew) kind = 'added';
    else if (hasOld && !hasNew) kind = 'removed';
    else if (JSON.stringify(oldVal) !== JSON.stringify(newVal)) kind = 'modified';
    else kind = 'unchanged';

    fields.push({
      field: key,
      oldValue: oldVal,
      newValue: newVal,
      kind,
    });
  }

  // Parent change detection for move operations
  let parentChange: TreeDiff['parentChange'];
  if (item.changeType === 'move') {
    parentChange = {
      oldParentId: (previous.parentId as string) ?? null,
      newParentId: (proposed.parentId as string) ?? null,
      oldParentLabel: (previous.parentLabel as string) ?? undefined,
      newParentLabel: (proposed.parentLabel as string) ?? undefined,
    };
  }

  return {
    nodeId: item.nodeId,
    nodeLabel: item.title,
    changeType: item.changeType,
    fields,
    parentChange,
  };
}

function formatTimeAgo(iso: string): string {
  try {
    const ms = Date.now() - new Date(iso).getTime();
    const sec = Math.floor(ms / 1000);
    if (sec < 60) return `${sec}s ago`;
    const min = Math.floor(sec / 60);
    if (min < 60) return `${min}m ago`;
    const hr = Math.floor(min / 60);
    if (hr < 24) return `${hr}h ago`;
    const days = Math.floor(hr / 24);
    return `${days}d ago`;
  } catch {
    return iso;
  }
}

const STATUS_STYLES: Record<string, { bg: string; dot: string; text: string }> = {
  pending: {
    bg: 'bg-amber-500/10 border-amber-500/30',
    dot: 'bg-amber-400',
    text: 'text-amber-400',
  },
  approved: {
    bg: 'bg-green-500/10 border-green-500/30',
    dot: 'bg-green-400',
    text: 'text-green-400',
  },
  denied: {
    bg: 'bg-red-500/10 border-red-500/30',
    dot: 'bg-red-400',
    text: 'text-red-400',
  },
};

// ─── Action confirmation sub-component ──────────────────────────────────

function ConfirmDialog({
  action,
  onConfirm,
  onCancel,
}: {
  action: 'approve' | 'deny';
  onConfirm: (comment: string) => void;
  onCancel: () => void;
}) {
  const [comment, setComment] = useState('');

  const isApprove = action === 'approve';
  const accentColor = isApprove
    ? 'bg-green-600 hover:bg-green-700'
    : 'bg-red-600 hover:bg-red-700';

  return (
    <div className="fixed inset-0 z-[60] flex items-center justify-center">
      <div
        className="absolute inset-0 bg-black/60"
        onClick={onCancel}
      />
      <div className="relative bg-gray-900 border border-gray-700 rounded-xl shadow-2xl w-full max-w-md mx-4">
        <div className="px-5 py-4 border-b border-gray-800">
          <h3 className="text-sm font-medium text-gray-200">
            {isApprove ? 'Approve' : 'Deny'} Change
          </h3>
        </div>
        <div className="px-5 py-4 space-y-3">
          <label htmlFor="confirm-comment" className="block text-xs text-gray-400">
            Comment (optional)
          </label>
          <textarea
            id="confirm-comment"
            value={comment}
            onChange={(e) => setComment(e.target.value)}
            rows={3}
            className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 placeholder-gray-600 resize-none focus:outline-none focus:ring-2 focus:ring-purple-500/50 focus:border-purple-500"
            placeholder={isApprove ? 'Looks good to me!' : 'Reason for denial...'}
          />
        </div>
        <div className="px-5 py-3 border-t border-gray-800 flex items-center justify-end gap-2">
          <button
            onClick={onCancel}
            className="px-3 py-1.5 text-xs font-medium text-gray-400 hover:text-gray-200 rounded-lg hover:bg-gray-800 transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={() => onConfirm(comment)}
            className={`px-4 py-1.5 text-xs font-semibold text-white rounded-lg transition-colors ${accentColor}`}
          >
            {isApprove ? 'Approve' : 'Deny'}
          </button>
        </div>
      </div>
    </div>
  );
}

// ─── Approval card sub-component ────────────────────────────────────────

function ApprovalCard({
  item,
  onSelect,
  onApprove,
  onDeny,
  acting,
}: {
  item: ApprovalItem;
  onSelect: () => void;
  onApprove: () => void;
  onDeny: () => void;
  acting: boolean;
}) {
  const style = STATUS_STYLES[item.status] ?? STATUS_STYLES.pending;

  return (
    <div
      className={`rounded-lg border p-4 transition-colors hover:border-gray-600 cursor-pointer group ${style.bg}`}
      onClick={onSelect}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 mb-1">
            <div className={`w-2 h-2 rounded-full flex-shrink-0 ${style.dot}`} />
            <h3 className="text-sm font-medium text-gray-200 truncate">
              {item.title}
            </h3>
          </div>
          {item.description && (
            <p className="text-xs text-gray-500 line-clamp-2 mb-2">
              {item.description}
            </p>
          )}
          <div className="flex items-center gap-3 text-[11px] text-gray-600">
            <span className="font-mono">{item.nodeId.slice(0, 8)}</span>
            <span>by {item.authorId}</span>
            <span className="flex items-center gap-1">
              <Clock className="w-3 h-3" />
              {formatTimeAgo(item.createdAt)}
            </span>
          </div>
        </div>

        <div className="flex items-center gap-1 flex-shrink-0">
          {item.status === 'pending' && (
            <>
              <button
                onClick={(e) => {
                  e.stopPropagation();
                  onApprove();
                }}
                disabled={acting}
                className="p-1.5 rounded-md text-green-400 hover:bg-green-500/20 disabled:opacity-40 transition-colors"
                title="Approve"
                aria-label={`Approve: ${item.title}`}
              >
                <Check className="w-4 h-4" />
              </button>
              <button
                onClick={(e) => {
                  e.stopPropagation();
                  onDeny();
                }}
                disabled={acting}
                className="p-1.5 rounded-md text-red-400 hover:bg-red-500/20 disabled:opacity-40 transition-colors"
                title="Deny"
                aria-label={`Deny: ${item.title}`}
              >
                <X className="w-4 h-4" />
              </button>
            </>
          )}
          <ChevronRight className="w-4 h-4 text-gray-600 group-hover:text-gray-400 transition-colors" />
        </div>
      </div>

      {item.reviewedBy && (
        <div className={`mt-2 pt-2 border-t border-gray-700/50`}>
          <span className={`text-[11px] ${style.text}`}>
            {item.status === 'approved'
              ? `Approved by ${item.reviewedBy}`
              : item.status === 'denied'
                ? `Denied by ${item.reviewedBy}`
                : `Reviewed by ${item.reviewedBy}`}
          </span>
        </div>
      )}
    </div>
  );
}

// ─── Main component ─────────────────────────────────────────────────────

export default function ApprovalPanel({ className = '' }: ApprovalPanelProps) {
  const [items, setItems] = useState<ApprovalItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [acting, setActing] = useState<string | null>(null); // id of item being acted on

  // Detail modal state
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState<string | null>(null);
  const [auditEntries, setAuditEntries] = useState<AuditEntry[]>([]);
  const [auditLoading, setAuditLoading] = useState(false);
  const [auditError, setAuditError] = useState<string | null>(null);

  // Confirm dialog state
  const [confirmAction, setConfirmAction] = useState<{
    id: string;
    action: 'approve' | 'deny';
  } | null>(null);

  // Filter state
  const [statusFilter, setStatusFilter] = useState<'all' | 'pending' | 'approved' | 'denied'>(
    'pending',
  );

  // ─── Fetch approvals ────────────────────────────────────────────────
  const fetchApprovals = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await fetch(apiUrl('/approvals'));
      if (!res.ok) throw new Error(`HTTP ${res.status}: ${res.statusText}`);
      const data = (await res.json()) as ApprovalItem[];
      setItems(Array.isArray(data) ? data : []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load approvals');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void fetchApprovals();
  }, [fetchApprovals]);

  // ─── Fetch audit for selected item ──────────────────────────────────
  useEffect(() => {
    if (!selectedId) {
      setAuditEntries([]);
      return;
    }

    let cancelled = false;

    const fetchAudit = async () => {
      setAuditLoading(true);
      setAuditError(null);
      try {
        const res = await fetch(apiUrl(`/approvals/${selectedId}/audit`));
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const data = (await res.json()) as AuditEntry[];
        if (!cancelled) setAuditEntries(Array.isArray(data) ? data : []);
      } catch (err) {
        if (!cancelled)
          setAuditError(err instanceof Error ? err.message : 'Failed to load audit');
      } finally {
        if (!cancelled) setAuditLoading(false);
      }
    };

    void fetchAudit();
    return () => {
      cancelled = true;
    };
  }, [selectedId]);

  // ─── Detail fetch ───────────────────────────────────────────────────
  useEffect(() => {
    if (!selectedId) {
      setDetailError(null);
      return;
    }

    let cancelled = false;

    const fetchDetail = async () => {
      setDetailLoading(true);
      setDetailError(null);
      try {
        const res = await fetch(apiUrl(`/approvals/${selectedId}`));
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const data = (await res.json()) as ApprovalItem;
        if (!cancelled) {
          // Refresh the single item in the list
          setItems((prev) => prev.map((it) => (it.id === data.id ? data : it)));
        }
      } catch (err) {
        if (!cancelled)
          setDetailError(err instanceof Error ? err.message : 'Failed to load details');
      } finally {
        if (!cancelled) setDetailLoading(false);
      }
    };

    void fetchDetail();
    return () => {
      cancelled = true;
    };
  }, [selectedId]);

  // ─── Approve / Deny ─────────────────────────────────────────────────
  const handleAction = useCallback(
    async (id: string, action: 'approve' | 'deny', comment: string) => {
      setConfirmAction(null);
      setActing(id);

      try {
        const res = await fetch(apiUrl(`/approvals/${id}/${action}`), {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ comment: comment || undefined }),
        });

        if (!res.ok) throw new Error(`HTTP ${res.status}`);

        const updated = (await res.json()) as ApprovalItem;
        setItems((prev) => prev.map((it) => (it.id === updated.id ? updated : it)));
      } catch (err) {
        setError(err instanceof Error ? err.message : `Failed to ${action}`);
      } finally {
        setActing(null);
      }
    },
    [],
  );

  // ─── Filtered items ─────────────────────────────────────────────────
  const filteredItems =
    statusFilter === 'all'
      ? items
      : items.filter((it) => it.status === statusFilter);

  // ─── Selected item ──────────────────────────────────────────────────
  const selectedItem = items.find((it) => it.id === selectedId);

  // ─── Render ─────────────────────────────────────────────────────────

  return (
    <div className={`p-6 ${className}`}>
      {/* ── Header ──────────────────────────────────────────────────── */}
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-bold text-gray-100">Approvals</h1>
          <p className="text-sm text-gray-500 mt-1">
            Review and manage pending change requests
          </p>
        </div>
        <button
          onClick={fetchApprovals}
          disabled={loading}
          className="flex items-center gap-2 px-3 py-2 rounded-lg text-xs font-medium bg-gray-800 hover:bg-gray-700 text-gray-300 transition-colors disabled:opacity-50"
        >
          <RefreshCw className={`w-3.5 h-3.5 ${loading ? 'animate-spin' : ''}`} />
          Refresh
        </button>
      </div>

      {/* ── Filter tabs ────────────────────────────────────────────── */}
      <div className="flex items-center gap-1 mb-4 p-1 rounded-lg bg-gray-800/50 w-fit">
        {(['all', 'pending', 'approved', 'denied'] as const).map((f) => (
          <button
            key={f}
            onClick={() => setStatusFilter(f)}
            className={`px-3 py-1.5 rounded-md text-xs font-medium capitalize transition-colors ${
              statusFilter === f
                ? 'bg-purple-600 text-white'
                : 'text-gray-400 hover:text-gray-200'
            }`}
          >
            {f}
          </button>
        ))}
      </div>

      {/* ── Error banner ───────────────────────────────────────────── */}
      {error && (
        <div className="flex items-center gap-2 mb-4 p-3 rounded-lg bg-red-500/10 border border-red-500/30 text-red-400 text-sm">
          <AlertCircle className="w-4 h-4 flex-shrink-0" />
          <span>{error}</span>
          <button
            onClick={fetchApprovals}
            className="ml-auto text-xs underline hover:text-red-300"
          >
            Retry
          </button>
        </div>
      )}

      {/* ── Loading state ──────────────────────────────────────────── */}
      {loading && (
        <div className="space-y-3">
          {[1, 2, 3].map((i) => (
            <div
              key={i}
              className="rounded-lg border border-gray-800 p-4 animate-pulse"
            >
              <div className="flex items-start gap-3">
                <div className="w-2 h-2 rounded-full bg-gray-700 mt-1.5" />
                <div className="flex-1 space-y-2">
                  <div className="h-4 bg-gray-800 rounded w-48" />
                  <div className="h-3 bg-gray-800 rounded w-72" />
                  <div className="h-3 bg-gray-800 rounded w-32" />
                </div>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* ── Empty state ────────────────────────────────────────────── */}
      {!loading && !error && filteredItems.length === 0 && (
        <div className="rounded-lg border border-gray-800 bg-gray-900 p-12 text-center">
          <Inbox className="w-10 h-10 text-gray-700 mx-auto mb-3" />
          <h3 className="text-sm font-medium text-gray-400 mb-1">
            No {statusFilter !== 'all' ? statusFilter : ''} approvals
          </h3>
          <p className="text-xs text-gray-600">
            {statusFilter === 'pending'
              ? 'All change requests have been reviewed.'
              : 'No approval items match this filter.'}
          </p>
        </div>
      )}

      {/* ── Approval cards ─────────────────────────────────────────── */}
      {!loading && !error && filteredItems.length > 0 && (
        <div className="space-y-3">
          {filteredItems.map((item) => (
            <ApprovalCard
              key={item.id}
              item={item}
              onSelect={() => setSelectedId(item.id)}
              onApprove={() =>
                setConfirmAction({ id: item.id, action: 'approve' })
              }
              onDeny={() =>
                setConfirmAction({ id: item.id, action: 'deny' })
              }
              acting={acting === item.id}
            />
          ))}
        </div>
      )}

      {/* ── Detail modal ───────────────────────────────────────────── */}
      {selectedId && (
        <div className="fixed inset-0 z-50 flex items-start justify-center pt-[10vh]">
          <div
            className="absolute inset-0 bg-black/60"
            onClick={() => setSelectedId(null)}
          />
          <div className="relative bg-gray-950 border border-gray-800 rounded-xl shadow-2xl w-full max-w-2xl mx-4 max-h-[80vh] flex flex-col">
            {/* Modal header */}
            <div className="flex items-center justify-between px-5 py-4 border-b border-gray-800 flex-shrink-0">
              <div className="flex items-center gap-2 min-w-0">
                <Eye className="w-4 h-4 text-purple-400 flex-shrink-0" />
                <h2 className="text-sm font-medium text-gray-200 truncate">
                  {selectedItem?.title ?? 'Loading…'}
                </h2>
              </div>
              <button
                onClick={() => setSelectedId(null)}
                className="p-1.5 rounded-md text-gray-500 hover:text-gray-300 hover:bg-gray-800 transition-colors"
              >
                <X className="w-4 h-4" />
              </button>
            </div>

            {/* Modal body */}
            <div className="flex-1 overflow-y-auto px-5 py-4 space-y-4">
              {/* Detail loading/error */}
              {detailLoading && (
                <div className="space-y-2 animate-pulse">
                  <div className="h-4 bg-gray-800 rounded w-32" />
                  <div className="h-3 bg-gray-800 rounded w-48" />
                </div>
              )}
              {detailError && (
                <div className="p-3 rounded-lg bg-red-500/10 border border-red-500/30">
                  <p className="text-sm text-red-400">{detailError}</p>
                </div>
              )}

              {/* Diff view */}
              {selectedItem && (
                <>
                  <ApprovalDiff diff={buildTreeDiff(selectedItem)} />

                  {/* Quick actions inside modal for pending items */}
                  {selectedItem.status === 'pending' && (
                    <div className="flex items-center gap-2 pt-2">
                      <button
                        onClick={() =>
                          setConfirmAction({
                            id: selectedItem.id,
                            action: 'approve',
                          })
                        }
                        disabled={acting === selectedItem.id}
                        className="flex items-center gap-2 px-4 py-2 rounded-lg text-xs font-semibold bg-green-600 hover:bg-green-700 text-white transition-colors disabled:opacity-50"
                      >
                        <Check className="w-4 h-4" />
                        Approve
                      </button>
                      <button
                        onClick={() =>
                          setConfirmAction({
                            id: selectedItem.id,
                            action: 'deny',
                          })
                        }
                        disabled={acting === selectedItem.id}
                        className="flex items-center gap-2 px-4 py-2 rounded-lg text-xs font-semibold bg-red-600 hover:bg-red-700 text-white transition-colors disabled:opacity-50"
                      >
                        <X className="w-4 h-4" />
                        Deny
                      </button>
                    </div>
                  )}

                  {/* Audit Trail */}
                  <AuditTrail
                    entries={auditEntries}
                    loading={auditLoading}
                    error={auditError}
                  />
                </>
              )}
            </div>
          </div>
        </div>
      )}

      {/* ── Confirm dialog ──────────────────────────────────────────── */}
      {confirmAction && (
        <ConfirmDialog
          action={confirmAction.action}
          onConfirm={(comment) =>
            void handleAction(confirmAction.id, confirmAction.action, comment)
          }
          onCancel={() => setConfirmAction(null)}
        />
      )}

      {/* ARIA live region for approval status changes */}
      <div
        className="sr-only"
        role="status"
        aria-live="polite"
        aria-atomic="true"
      >
        {acting ? `Processing ${confirmAction?.action}...` : ''}
        {!acting && items.length > 0
          ? `${items.filter((i) => i.status === 'pending').length} pending approvals`
          : ''}
      </div>
    </div>
  );
}
