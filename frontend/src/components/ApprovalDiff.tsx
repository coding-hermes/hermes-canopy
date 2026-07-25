/**
 * Hermes Canopy — Approval Diff View
 *
 * Tree-aware diff visualization for node changes.
 * Shows field-level diffs with add/remove/modify highlighting,
 * and parent-change information for move operations.
 */

import { useMemo, useState } from 'react';
import {
  GitBranch,
  Plus,
  Minus,
  Edit3,
  ArrowRight,
  ChevronDown,
  ChevronRight,
  FileText,
  Type,
  Hash,
  Calendar,
} from 'lucide-react';
import type { TreeDiff, DiffField, ChangeType } from '../types/approval.ts';

// ─── Props ──────────────────────────────────────────────────────────────

export interface ApprovalDiffProps {
  diff: TreeDiff;
  /** Optional className for the root wrapper */
  className?: string;
}

// ─── Helpers ────────────────────────────────────────────────────────────

const CHANGE_ICONS: Record<ChangeType, typeof GitBranch> = {
  create: Plus,
  update: Edit3,
  delete: Minus,
  move: ArrowRight,
};

const CHANGE_COLORS: Record<ChangeType, string> = {
  create: 'text-green-400',
  update: 'text-amber-400',
  delete: 'text-red-400',
  move: 'text-purple-400',
};

const CHANGE_BG: Record<ChangeType, string> = {
  create: 'bg-green-500/10 border-green-500/30',
  update: 'bg-amber-500/10 border-amber-500/30',
  delete: 'bg-red-500/10 border-red-500/30',
  move: 'bg-purple-500/10 border-purple-500/30',
};

function getFieldIcon(field: string): typeof FileText {
  if (field === 'content' || field === 'description' || field === 'title')
    return Type;
  if (field === 'parentId' || field === 'treeId') return GitBranch;
  if (field.includes('At') || field.includes('Date')) return Calendar;
  return Hash;
}

function formatDiffValue(value: unknown): string {
  if (value === null || value === undefined) return '—';
  if (typeof value === 'string') {
    if (value.length > 120) return value.slice(0, 120) + '…';
    return value;
  }
  if (typeof value === 'object') {
    return JSON.stringify(value).slice(0, 120);
  }
  return String(value);
}

// ─── Sub-components ─────────────────────────────────────────────────────

function DiffFieldRow({ field, compact }: { field: DiffField; compact: boolean }) {
  const FieldIcon = getFieldIcon(field.field);

  const kindStyle: Record<string, string> = {
    added: 'bg-green-500/5 border-l-2 border-green-500',
    removed: 'bg-red-500/5 border-l-2 border-red-500',
    modified: 'bg-amber-500/5 border-l-2 border-amber-500',
    unchanged: 'bg-transparent',
  };

  return (
    <div className={`flex flex-col gap-1 px-3 py-2 ${kindStyle[field.kind] ?? ''}`}>
      <div className="flex items-center gap-2 text-xs">
        <FieldIcon className="w-3 h-3 text-gray-500" />
        <span className="font-mono font-medium text-gray-300">{field.field}</span>
        <span
          className={`px-1.5 py-0.5 rounded text-[10px] font-semibold uppercase tracking-wider ${
            field.kind === 'added'
              ? 'bg-green-500/20 text-green-400'
              : field.kind === 'removed'
                ? 'bg-red-500/20 text-red-400'
                : field.kind === 'modified'
                  ? 'bg-amber-500/20 text-amber-400'
                  : 'bg-gray-700 text-gray-400'
          }`}
        >
          {field.kind}
        </span>
      </div>

      {field.kind === 'unchanged' ? (
        <span className="text-gray-500 text-xs font-mono ml-5">
          {formatDiffValue(field.oldValue)}
        </span>
      ) : (
        <div
          className={
            compact
              ? 'flex items-center gap-2 ml-5'
              : 'grid grid-cols-1 gap-1 ml-5'
          }
        >
          {(field.kind === 'removed' || field.kind === 'modified') && (
            <div className="flex items-start gap-1.5">
              <Minus className="w-3 h-3 mt-0.5 text-red-400 flex-shrink-0" />
              <span className="text-red-400/80 text-xs font-mono line-through break-all">
                {formatDiffValue(field.oldValue)}
              </span>
            </div>
          )}
          {(field.kind === 'added' || field.kind === 'modified') && (
            <div className="flex items-start gap-1.5">
              <Plus className="w-3 h-3 mt-0.5 text-green-400 flex-shrink-0" />
              <span className="text-green-400/80 text-xs font-mono break-all">
                {formatDiffValue(field.newValue)}
              </span>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// ─── Main component ─────────────────────────────────────────────────────

export default function ApprovalDiff({
  diff,
  className = '',
}: ApprovalDiffProps) {
  const [expanded, setExpanded] = useState(true);
  const [showUnchanged, setShowUnchanged] = useState(false);

  const visibleFields = useMemo(() => {
    if (showUnchanged) return diff.fields;
    return diff.fields.filter((f) => f.kind !== 'unchanged');
  }, [diff.fields, showUnchanged]);

  const ChangeIcon = CHANGE_ICONS[diff.changeType];

  return (
    <div
      className={`rounded-lg border bg-gray-900 border-gray-800 overflow-hidden ${className}`}
    >
      {/* ── Header ─────────────────────────────────────────────────── */}
      <button
        onClick={() => setExpanded((v) => !v)}
        className="w-full flex items-center gap-3 px-4 py-3 hover:bg-gray-800/50 transition-colors"
      >
        {expanded ? (
          <ChevronDown className="w-4 h-4 text-gray-500 flex-shrink-0" />
        ) : (
          <ChevronRight className="w-4 h-4 text-gray-500 flex-shrink-0" />
        )}
        <div
          className={`p-1.5 rounded-md flex-shrink-0 ${CHANGE_BG[diff.changeType]}`}
        >
          <ChangeIcon className={`w-4 h-4 ${CHANGE_COLORS[diff.changeType]}`} />
        </div>
        <div className="flex-1 text-left min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-medium text-sm text-gray-200 truncate">
              {diff.nodeLabel}
            </span>
            <span
              className={`text-[10px] font-semibold uppercase tracking-wider ${CHANGE_COLORS[diff.changeType]}`}
            >
              {diff.changeType}
            </span>
          </div>
          <div className="text-xs text-gray-500 font-mono mt-0.5">
            {diff.nodeId}
          </div>
        </div>
        <span className="text-[11px] text-gray-500">
          {visibleFields.length} field{visibleFields.length !== 1 ? 's' : ''} changed
        </span>
      </button>

      {/* ── Body ───────────────────────────────────────────────────── */}
      {expanded && (
        <div className="border-t border-gray-800">
          {/* Parent change indicator for moves */}
          {diff.parentChange && (
            <div className="px-4 py-3 bg-purple-500/5 border-b border-purple-500/10">
              <div className="flex items-center gap-2 text-xs text-purple-300">
                <GitBranch className="w-3.5 h-3.5" />
                <span className="font-medium">Moved:</span>
                <span className="text-gray-500 line-through">
                  {diff.parentChange.oldParentLabel ?? diff.parentChange.oldParentId ?? 'root'}
                </span>
                <ArrowRight className="w-3 h-3 text-purple-400" />
                <span className="text-purple-300">
                  {diff.parentChange.newParentLabel ?? diff.parentChange.newParentId ?? 'root'}
                </span>
              </div>
            </div>
          )}

          {/* Field diffs */}
          {visibleFields.length === 0 ? (
            <div className="px-4 py-6 text-center text-gray-500 text-sm">
              No field changes to display.
            </div>
          ) : (
            <div className="divide-y divide-gray-800/50 max-h-96 overflow-y-auto">
              {visibleFields.map((field) => (
                <DiffFieldRow
                  key={field.field}
                  field={field}
                  compact={visibleFields.length > 4}
                />
              ))}
            </div>
          )}

          {/* Footer with show-unchanged toggle */}
          <div className="flex items-center justify-between px-4 py-2 border-t border-gray-800 bg-gray-900/50">
            <label className="flex items-center gap-2 text-[11px] text-gray-500 cursor-pointer select-none">
              <input
                type="checkbox"
                checked={showUnchanged}
                onChange={(e) => setShowUnchanged(e.target.checked)}
                className="w-3 h-3 rounded border-gray-600 bg-gray-800 accent-purple-500"
              />
              Show unchanged fields
            </label>
            <span className="text-[11px] text-gray-600">
              {diff.fields.length} total
            </span>
          </div>
        </div>
      )}
    </div>
  );
}
