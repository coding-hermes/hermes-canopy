/**
 * Hermes Canopy — IterationCard
 *
 * Generic card for agent iterations with status badge, progress bar,
 * and expand/collapse details. Supports subtypes:
 *   search, code_exec, file_read, tool_call
 *
 * Each subtype has distinct visual treatment (icon, colors).
 */

import { memo, useState } from 'react';
import {
  Search,
  Terminal,
  FileText,
  Wrench,
  ChevronDown,
  ChevronRight,
  Loader2,
  CheckCircle2,
  XCircle,
  AlertTriangle,
  Clock,
  PauseCircle,
} from 'lucide-react';
import type { IterationCardSubtypeData } from '../../types/agent.ts';

// ─── Props ─────────────────────────────────────────────────────────────

export interface IterationCardProps {
  data: IterationCardSubtypeData;
  className?: string;
}

// ─── Subtype config ────────────────────────────────────────────────────

interface SubtypeConfig {
  icon: React.ReactNode;
  label: string;
  accent: string; // Tailwind color class for border/badge
  bg: string;
}

const SUBTYPE_CONFIG: Record<string, SubtypeConfig> = {
  iteration_search: {
    icon: <Search className="w-4 h-4" />,
    label: 'Search',
    accent: 'border-blue-500/50',
    bg: 'bg-blue-500/10',
  },
  iteration_code_exec: {
    icon: <Terminal className="w-4 h-4" />,
    label: 'Code Exec',
    accent: 'border-green-500/50',
    bg: 'bg-green-500/10',
  },
  iteration_file_read: {
    icon: <FileText className="w-4 h-4" />,
    label: 'File Read',
    accent: 'border-amber-500/50',
    bg: 'bg-amber-500/10',
  },
  iteration_tool_call: {
    icon: <Wrench className="w-4 h-4" />,
    label: 'Tool Call',
    accent: 'border-purple-500/50',
    bg: 'bg-purple-500/10',
  },
};

// ─── Status config ─────────────────────────────────────────────────────

function getStatusBadge(state: string): {
  icon: React.ReactNode;
  label: string;
  className: string;
} {
  switch (state) {
    case 'running':
      return {
        icon: <Loader2 className="w-3 h-3 animate-spin" />,
        label: 'Running',
        className: 'bg-purple-500/20 text-purple-300',
      };
    case 'waiting_for_user':
      return {
        icon: <PauseCircle className="w-3 h-3" />,
        label: 'Waiting',
        className: 'bg-amber-500/20 text-amber-300',
      };
    case 'completed':
      return {
        icon: <CheckCircle2 className="w-3 h-3" />,
        label: 'Done',
        className: 'bg-green-500/20 text-green-300',
      };
    case 'failed':
      return {
        icon: <XCircle className="w-3 h-3" />,
        label: 'Failed',
        className: 'bg-red-500/20 text-red-300',
      };
    case 'cancelled':
      return {
        icon: <AlertTriangle className="w-3 h-3" />,
        label: 'Cancelled',
        className: 'bg-gray-500/20 text-gray-400',
      };
    case 'interrupted':
      return {
        icon: <AlertTriangle className="w-3 h-3" />,
        label: 'Interrupted',
        className: 'bg-orange-500/20 text-orange-300',
      };
    default:
      return {
        icon: <Clock className="w-3 h-3" />,
        label: state,
        className: 'bg-gray-500/20 text-gray-400',
      };
  }
}

// ─── Subtype detail renderers ──────────────────────────────────────────

function SearchDetail({ data }: { data: IterationCardSubtypeData }) {
  if (data.subtype !== 'iteration_search') return null;
  return (
    <div className="space-y-1.5">
      {data.urlsSearched && data.urlsSearched.length > 0 && (
        <div>
          <p className="text-xs text-gray-500 mb-1">
            URLs searched: {data.urlsSearched.length}
          </p>
          <div className="space-y-0.5 max-h-24 overflow-y-auto">
            {data.urlsSearched.slice(0, 5).map((url, i) => (
              <p key={i} className="text-xs text-gray-400 truncate font-mono">
                {url}
              </p>
            ))}
            {data.urlsSearched.length > 5 && (
              <p className="text-xs text-gray-500 italic">
                +{data.urlsSearched.length - 5} more
              </p>
            )}
          </div>
        </div>
      )}
      {data.currentBatch && data.currentBatch.length > 0 && (
        <p className="text-xs text-gray-400">
          Results: {data.currentBatch.filter((r) => r.status === 'retrieved').length}/
          {data.currentBatch.length} retrieved
        </p>
      )}
    </div>
  );
}

function CodeExecDetail({ data }: { data: IterationCardSubtypeData }) {
  if (data.subtype !== 'iteration_code_exec') return null;
  return (
    <div className="space-y-1">
      {data.command && (
        <p className="text-xs text-gray-400 font-mono truncate">
          <span className="text-gray-500">$</span> {data.command}
        </p>
      )}
      {data.workdir && (
        <p className="text-xs text-gray-500 truncate font-mono">
          in {data.workdir}
        </p>
      )}
      {data.exitCode != null && (
        <p className={`text-xs font-mono ${data.exitCode === 0 ? 'text-green-400' : 'text-red-400'}`}>
          exit: {data.exitCode}
        </p>
      )}
      {data.stdout && data.stdout.length > 0 && (
        <p className="text-xs text-gray-500">
          {data.stdout.reduce((sum, s) => sum + s.length, 0)} chars output
        </p>
      )}
    </div>
  );
}

function FileReadDetail({ data }: { data: IterationCardSubtypeData }) {
  if (data.subtype !== 'iteration_file_read') return null;
  return (
    <div className="space-y-1">
      <p className="text-xs text-gray-400 font-mono truncate">{data.path}</p>
      <div className="flex items-center gap-3 text-xs text-gray-500">
        <span>{data.lineCount} lines</span>
        <span>{(data.size / 1024).toFixed(1)} KB</span>
        {data.language && <span className="text-purple-400">{data.language}</span>}
      </div>
      {data.highlights && data.highlights.length > 0 && (
        <p className="text-xs text-gray-500">
          {data.highlights.length} highlight{data.highlights.length !== 1 ? 's' : ''}
        </p>
      )}
    </div>
  );
}

function ToolCallDetail({ data }: { data: IterationCardSubtypeData }) {
  if (data.subtype !== 'iteration_tool_call') return null;
  return (
    <div className="space-y-1">
      <p className="text-xs text-gray-400">
        <span className="text-purple-400 font-mono">{data.toolName}</span>
        {data.gated && (
          <span className="ml-2 text-xs px-1 py-0.5 rounded bg-amber-500/20 text-amber-300">
            gated
          </span>
        )}
      </p>
      {data.status && (
        <p className="text-xs text-gray-500 capitalize">{data.status.replace(/_/g, ' ')}</p>
      )}
      {data.error && (
        <p className="text-xs text-red-400 line-clamp-2">{data.error}</p>
      )}
    </div>
  );
}

function renderSubtypeDetail(data: IterationCardSubtypeData): React.ReactNode {
  switch (data.subtype) {
    case 'iteration_search':
      return <SearchDetail data={data} />;
    case 'iteration_code_exec':
      return <CodeExecDetail data={data} />;
    case 'iteration_file_read':
      return <FileReadDetail data={data} />;
    case 'iteration_tool_call':
      return <ToolCallDetail data={data} />;
    default:
      return null;
  }
}

// ─── Component ─────────────────────────────────────────────────────────

function IterationCardComponent({ data, className = '' }: IterationCardProps) {
  const [expanded, setExpanded] = useState(!data._collapsed);
  const config = SUBTYPE_CONFIG[data.subtype] ?? SUBTYPE_CONFIG.iteration_search;
  const status = getStatusBadge(data.state);
  const hasProgress = data.progress && data.progress.total > 0;

  return (
    <div
      className={`rounded-lg border bg-gray-800/90 border-gray-700 shadow-sm min-w-[200px] max-w-[320px] ${config.accent} ${className}`}
    >
      {/* Header */}
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className="w-full flex items-center gap-2 px-3 py-2 rounded-t-lg hover:bg-gray-750
                   transition-colors text-left"
      >
        {/* Subtype icon */}
        <span className={`flex-shrink-0 p-1 rounded ${config.bg} text-gray-300`}>
          {config.icon}
        </span>

        {/* Title + subtype label */}
        <div className="flex-1 min-w-0">
          <p className="text-sm font-medium text-gray-200 truncate">
            {data.title || config.label}
          </p>
          <p className="text-xs text-gray-500">{config.label}</p>
        </div>

        {/* Status badge */}
        <span
          className={`flex items-center gap-1 text-xs px-1.5 py-0.5 rounded-full flex-shrink-0 ${status.className}`}
        >
          {status.icon}
          {status.label}
        </span>

        {expanded ? (
          <ChevronDown className="w-4 h-4 text-gray-500 flex-shrink-0" />
        ) : (
          <ChevronRight className="w-4 h-4 text-gray-500 flex-shrink-0" />
        )}
      </button>

      {/* Expanded detail */}
      {expanded && (
        <div className="border-t border-gray-700/60 px-3 py-2">
          {renderSubtypeDetail(data)}
        </div>
      )}

      {/* Progress bar */}
      {hasProgress && (
        <div className="border-t border-gray-700/60 px-3 py-1.5">
          <div className="flex items-center gap-2">
            <div className="flex-1 h-1.5 bg-gray-700 rounded-full overflow-hidden">
              <div
                className="h-full rounded-full transition-all duration-300"
                style={{
                  width: `${(data.progress!.current / data.progress!.total) * 100}%`,
                  backgroundColor:
                    data.progress!.status === 'completed'
                      ? '#22c55e'
                      : data.progress!.status === 'failed'
                        ? '#ef4444'
                        : '#7c3aed',
                }}
              />
            </div>
            <span className="text-xs text-gray-500 flex-shrink-0">
              {data.progress!.current}/{data.progress!.total}
            </span>
          </div>
        </div>
      )}
    </div>
  );
}

export const IterationCard = memo(IterationCardComponent);
