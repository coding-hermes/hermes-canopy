/**
 * Hermes Canopy — Audit Trail
 *
 * Audit log table showing approval history with timestamps, actions,
 * and user info. Consumed by ApprovalPanel for detail drill-down.
 */

import { useMemo } from 'react';
import {
  Clock,
  User,
  CheckCircle,
  XCircle,
  AlertCircle,
  FileText,
  RefreshCw,
  Eye,
} from 'lucide-react';
import type { AuditEntry } from '../types/approval.ts';

// ─── Props ──────────────────────────────────────────────────────────────

export interface AuditTrailProps {
  entries: AuditEntry[];
  loading?: boolean;
  error?: string | null;
  /** Optional className for the root wrapper */
  className?: string;
}

// ─── Helpers ────────────────────────────────────────────────────────────

interface ActionStyle {
  icon: typeof CheckCircle;
  color: string;
  bg: string;
  label: string;
}

function getActionStyle(action: string): ActionStyle {
  const lower = action.toLowerCase();

  if (lower.includes('approve')) {
    return {
      icon: CheckCircle,
      color: 'text-green-400',
      bg: 'bg-green-500/10',
      label: 'Approved',
    };
  }
  if (lower.includes('deny') || lower.includes('reject')) {
    return {
      icon: XCircle,
      color: 'text-red-400',
      bg: 'bg-red-500/10',
      label: 'Denied',
    };
  }
  if (lower.includes('create') || lower.includes('submit')) {
    return {
      icon: FileText,
      color: 'text-blue-400',
      bg: 'bg-blue-500/10',
      label: 'Created',
    };
  }
  if (lower.includes('review')) {
    return {
      icon: Eye,
      color: 'text-purple-400',
      bg: 'bg-purple-500/10',
      label: 'Reviewed',
    };
  }
  if (lower.includes('update') || lower.includes('modif') || lower.includes('edit')) {
    return {
      icon: RefreshCw,
      color: 'text-amber-400',
      bg: 'bg-amber-500/10',
      label: 'Updated',
    };
  }

  return {
    icon: AlertCircle,
    color: 'text-gray-400',
    bg: 'bg-gray-700',
    label: action,
  };
}

function formatTimestamp(iso: string): string {
  try {
    const d = new Date(iso);
    return d.toLocaleString('en-US', {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    });
  } catch {
    return iso;
  }
}

// ─── Main component ─────────────────────────────────────────────────────

export default function AuditTrail({
  entries,
  loading = false,
  error = null,
  className = '',
}: AuditTrailProps) {
  const sorted = useMemo(
    () =>
      [...entries].sort(
        (a, b) =>
          new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime(),
      ),
    [entries],
  );

  // ── Loading state ──────────────────────────────────────────────────
  if (loading) {
    return (
      <div className={`rounded-lg border border-gray-800 bg-gray-900 ${className}`}>
        <div className="px-4 py-3 border-b border-gray-800 flex items-center gap-2">
          <Clock className="w-4 h-4 text-gray-500" />
          <h3 className="text-sm font-medium text-gray-300">Audit Trail</h3>
        </div>
        <div className="p-6 space-y-4">
          {[1, 2, 3].map((i) => (
            <div key={i} className="flex items-center gap-3 animate-pulse">
              <div className="w-8 h-8 rounded-full bg-gray-800" />
              <div className="flex-1 space-y-1.5">
                <div className="h-3 bg-gray-800 rounded w-24" />
                <div className="h-2.5 bg-gray-800 rounded w-40" />
              </div>
              <div className="h-2.5 bg-gray-800 rounded w-16" />
            </div>
          ))}
        </div>
      </div>
    );
  }

  // ── Error state ────────────────────────────────────────────────────
  if (error) {
    return (
      <div className={`rounded-lg border border-red-500/30 bg-red-500/5 ${className}`}>
        <div className="px-4 py-3 border-b border-red-500/20 flex items-center gap-2">
          <Clock className="w-4 h-4 text-red-400" />
          <h3 className="text-sm font-medium text-red-300">Audit Trail</h3>
        </div>
        <div className="p-4 text-center">
          <AlertCircle className="w-5 h-5 text-red-400 mx-auto mb-2" />
          <p className="text-sm text-red-400">{error}</p>
        </div>
      </div>
    );
  }

  // ── Empty state ────────────────────────────────────────────────────
  if (sorted.length === 0) {
    return (
      <div className={`rounded-lg border border-gray-800 bg-gray-900 ${className}`}>
        <div className="px-4 py-3 border-b border-gray-800 flex items-center gap-2">
          <Clock className="w-4 h-4 text-gray-500" />
          <h3 className="text-sm font-medium text-gray-300">Audit Trail</h3>
        </div>
        <div className="p-6 text-center">
          <FileText className="w-5 h-5 text-gray-600 mx-auto mb-2" />
          <p className="text-sm text-gray-500">No audit entries recorded.</p>
        </div>
      </div>
    );
  }

  // ── Filled state ───────────────────────────────────────────────────
  return (
    <div className={`rounded-lg border border-gray-800 bg-gray-900 ${className}`}>
      <div className="px-4 py-3 border-b border-gray-800 flex items-center gap-2">
        <Clock className="w-4 h-4 text-gray-500" />
        <h3 className="text-sm font-medium text-gray-300">Audit Trail</h3>
        <span className="ml-auto text-[11px] text-gray-600">
          {sorted.length} entr{sorted.length !== 1 ? 'ies' : 'y'}
        </span>
      </div>

      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-gray-800 text-left">
              <th className="px-4 py-2.5 text-[11px] font-semibold text-gray-500 uppercase tracking-wider w-12">
                #
              </th>
              <th className="px-4 py-2.5 text-[11px] font-semibold text-gray-500 uppercase tracking-wider">
                Action
              </th>
              <th className="px-4 py-2.5 text-[11px] font-semibold text-gray-500 uppercase tracking-wider">
                Actor
              </th>
              <th className="px-4 py-2.5 text-[11px] font-semibold text-gray-500 uppercase tracking-wider">
                Details
              </th>
              <th className="px-4 py-2.5 text-[11px] font-semibold text-gray-500 uppercase tracking-wider text-right">
                Timestamp
              </th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-800/50">
            {sorted.map((entry, idx) => {
              const style = getActionStyle(entry.action);
              const ActionIcon = style.icon;
              const detailsStr = entry.details
                ? Object.entries(entry.details)
                    .filter(([, v]) => v !== null && v !== undefined && v !== '')
                    .map(([k, v]) => `${k}: ${typeof v === 'string' ? (v.length > 40 ? v.slice(0, 40) + '…' : v) : String(v)}`)
                    .join(' · ')
                : '—';

              return (
                <tr
                  key={entry.id}
                  className="hover:bg-gray-800/30 transition-colors"
                >
                  <td className="px-4 py-3 text-xs text-gray-600 font-mono">
                    {sorted.length - idx}
                  </td>
                  <td className="px-4 py-3">
                    <div className="flex items-center gap-2">
                      <span className={`p-1 rounded ${style.bg}`}>
                        <ActionIcon className={`w-3 h-3 ${style.color}`} />
                      </span>
                      <span className="text-xs text-gray-300 font-medium">
                        {style.label}
                      </span>
                    </div>
                  </td>
                  <td className="px-4 py-3">
                    <div className="flex items-center gap-1.5">
                      <User className="w-3 h-3 text-gray-500" />
                      <span className="text-xs text-gray-400 font-mono truncate max-w-[120px]">
                        {entry.actorId || 'system'}
                      </span>
                    </div>
                  </td>
                  <td className="px-4 py-3">
                    <span
                      className="text-xs text-gray-500 truncate max-w-[200px] inline-block"
                      title={
                        entry.details
                          ? JSON.stringify(entry.details, null, 2)
                          : undefined
                      }
                    >
                      {detailsStr}
                    </span>
                  </td>
                  <td className="px-4 py-3 text-right text-xs text-gray-500 font-mono whitespace-nowrap">
                    {formatTimestamp(entry.timestamp)}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}
