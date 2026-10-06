/**
 * Hermes Canopy — IterationCard
 *
 * Generic card frame for agent iterations. It owns the header (subtype icon,
 * title, status badge, expand toggle) and dispatches the subtype-specific
 * compact/expanded content to the §8.2 renderer registry.
 */

import { createElement, memo, useState } from 'react';
import {
  Search,
  Terminal,
  FileText,
  Wrench,
  Brain,
  ChevronDown,
  ChevronRight,
  Loader2,
  CheckCircle2,
  XCircle,
  AlertTriangle,
  Clock,
  PauseCircle,
} from 'lucide-react';
import type { IterationCardSubtypeData, IterationSubtype } from '../../types/agent.ts';
import { resolveIterationRenderer } from '../../lib/iterationRenderers.ts';
import { statusLabel, statusTone } from './iterationShared.tsx';
import IterationPluginRenderer, { type IterationPlugin, type IterationPluginRendererProps, type IterationSseSource } from './IterationPluginRenderer.tsx';

export interface IterationCardProps {
  data: IterationCardSubtypeData;
  className?: string;
  /** Plugin associated with the card; when set, content renders in the sandbox. */
  plugin?: IterationPlugin;
  /** Card SSE stream; durable card_dismissed frames are forwarded to the plugin. */
  eventSource?: IterationSseSource;
  grantedPermissions?: IterationPluginRendererProps['grantedPermissions'];
  /** Fires for plugin events and card→plugin status notifications. */
  onPluginEvent?: IterationPluginRendererProps['onEvent'];
  /** Called when a durable card_dismissed frame arrives on the stream. */
  onDismissed?: IterationPluginRendererProps['onDismissed'];
}

interface SubtypeConfig {
  icon: React.ReactNode;
  label: string;
  accent: string;
  bg: string;
}

const SUBTYPE_CONFIG: Record<IterationSubtype, SubtypeConfig> = {
  iteration_search: { icon: <Search className="h-4 w-4" />, label: 'Search', accent: 'border-blue-500/50', bg: 'bg-blue-500/10' },
  iteration_code_exec: { icon: <Terminal className="h-4 w-4" />, label: 'Code Exec', accent: 'border-green-500/50', bg: 'bg-green-500/10' },
  iteration_file_read: { icon: <FileText className="h-4 w-4" />, label: 'File Read', accent: 'border-amber-500/50', bg: 'bg-amber-500/10' },
  iteration_thinking: { icon: <Brain className="h-4 w-4" />, label: 'Thinking', accent: 'border-purple-500/50', bg: 'bg-purple-500/10' },
  iteration_tool_call: { icon: <Wrench className="h-4 w-4" />, label: 'Tool Call', accent: 'border-purple-500/50', bg: 'bg-purple-500/10' },
};

function statusIcon(state: string): React.ReactNode {
  switch (state) {
    case 'running': return <Loader2 className="h-3 w-3 animate-spin" />;
    case 'waiting_for_user': return <PauseCircle className="h-3 w-3" />;
    case 'completed': return <CheckCircle2 className="h-3 w-3" />;
    case 'failed': return <XCircle className="h-3 w-3" />;
    case 'cancelled': return <AlertTriangle className="h-3 w-3" />;
    case 'interrupted': return <AlertTriangle className="h-3 w-3" />;
    default: return <Clock className="h-3 w-3" />;
  }
}

function IterationCardComponent({ data, className = '', plugin, eventSource, grantedPermissions, onPluginEvent, onDismissed }: IterationCardProps) {
  const [expanded, setExpanded] = useState(!data._collapsed);
  const config = SUBTYPE_CONFIG[data.subtype] ?? SUBTYPE_CONFIG.iteration_search;
  const renderer = resolveIterationRenderer(data.subtype);
  const cardId = data.progress?.cardId ?? data.title;
  const pluginAssociated = Boolean(plugin);

  return (
    <div className={`rounded-lg border bg-gray-800/90 border-gray-700 shadow-sm min-w-[200px] max-w-[320px] ${config.accent} ${className}`}>
      <button
        type="button"
        onClick={() => setExpanded((value) => !value)}
        aria-expanded={expanded}
        aria-controls={`iteration-body-${cardId}`}
        className="flex w-full items-center gap-2 rounded-t-lg px-3 py-2 text-left transition-colors hover:bg-gray-750"
      >
        <span className={`flex-shrink-0 rounded p-1 text-gray-300 ${config.bg}`}>{config.icon}</span>
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-medium text-gray-200">{data.title || config.label}</p>
          <p className="text-xs text-gray-500">{config.label}</p>
        </div>
        <span className={`flex shrink-0 items-center gap-1 rounded-full px-1.5 py-0.5 text-xs ${statusTone(data.state)}`}>
          {statusIcon(data.state)}
          {statusLabel(data.state)}
        </span>
        {expanded ? <ChevronDown className="h-4 w-4 shrink-0 text-gray-500" /> : <ChevronRight className="h-4 w-4 shrink-0 text-gray-500" />}
      </button>

      {expanded && pluginAssociated && (
        <div id={`iteration-body-${cardId}`} className="border-t border-gray-700/60 px-3 py-2">
          <IterationPluginRenderer
            cardId={cardId}
            data={data}
            plugin={plugin}
            eventSource={eventSource}
            grantedPermissions={grantedPermissions}
            onEvent={onPluginEvent}
            onDismissed={onDismissed}
          />
        </div>
      )}

      {expanded && !pluginAssociated && renderer && (
        <div id={`iteration-body-${cardId}`} className="border-t border-gray-700/60 px-3 py-2">
          {createElement(renderer, { cardId, data, compact: false, events: [], submitFeedback: undefined, cancel: undefined })}
        </div>
      )}
    </div>
  );
}

export const IterationCard = memo(IterationCardComponent);
