/**
 * Hermes Canopy — IterationPluginRenderer (SPEC-PL-04 §8.3)
 *
 * Bridges iteration card data to the plugin sandbox. When a card carries an
 * associated plugin, the card is mounted inside a PluginHost sandbox and its
 * progress/state transitions are surfaced as plugin notifications. Durable
 * `card_dismissed` frames arriving on the iteration SSE stream are forwarded
 * so the host can release the card.
 */

import { useEffect, useRef } from 'react';
import PluginHost from '../PluginHost.tsx';
import type { PluginEventPayload, PluginManifest, PluginPermission } from '../../lib/pluginTypes';
import type { IterationCardSubtypeData, IterationState } from '../../types/agent.ts';

export type IterationSseSource = Pick<EventSource, 'addEventListener' | 'removeEventListener'>;

export interface IterationPlugin {
  id: string;
  name: string;
  manifest: PluginManifest;
  sourceJS: string;
}

export interface IterationPluginRendererProps {
  cardId: string;
  data: IterationCardSubtypeData;
  /** Plugin associated with the card; when absent nothing is rendered. */
  plugin?: IterationPlugin;
  /** Iteration card SSE stream; `card_dismissed` frames are forwarded. */
  eventSource?: IterationSseSource;
  grantedPermissions?: readonly PluginPermission[];
  className?: string;
  /** Fires for plugin events and for card→plugin status notifications. */
  onEvent?: (event: PluginEventPayload) => void;
  /** Called with the card id when a durable card_dismissed frame arrives. */
  onDismissed?: (cardId: string) => void;
}

/** Map card lifecycle state to a short plugin-visible status string. */
export function iterationPluginStatus(data: IterationCardSubtypeData): string {
  const progress = data.progress;
  const counts = progress.total > 0 ? `${progress.current}/${progress.total}` : progress.status;
  return `${data.state === 'waiting_for_user' ? 'waiting for user' : data.state} · ${counts}`;
}

const NOTIFICATION_LEVELS: Record<IterationState, 'info' | 'success' | 'warning' | 'error'> = {
  running: 'info',
  waiting_for_user: 'warning',
  completed: 'success',
  failed: 'error',
  cancelled: 'warning',
  interrupted: 'warning',
};

export default function IterationPluginRenderer({
  cardId,
  data,
  plugin,
  eventSource,
  grantedPermissions,
  className,
  onEvent,
  onDismissed,
}: IterationPluginRendererProps) {
  const status = iterationPluginStatus(data);
  const lastNotified = useRef<string | null>(null);

  // Card progress/state → plugin notification, once per status transition.
  useEffect(() => {
    if (!plugin) return;
    if (lastNotified.current === status) return;
    lastNotified.current = status;
    onEvent?.({
      event: 'notification',
      data: {
        title: data.title,
        body: status,
        level: NOTIFICATION_LEVELS[data.state],
        persistent: false,
        ttl_seconds: 5,
      },
    });
  }, [data, plugin, onEvent, status]);

  // Durable card_dismissed frames on the iteration stream are forwarded to
  // the host (PL-04 §8.3). The stream itself is caller-owned: attach and
  // detach, never close.
  useEffect(() => {
    if (!eventSource || !plugin) return;
    const listener: EventListener = () => onDismissed?.(cardId);
    eventSource.addEventListener('card_dismissed', listener);
    return () => eventSource.removeEventListener('card_dismissed', listener);
  }, [cardId, eventSource, onDismissed, plugin]);

  if (!plugin) return null;
  return (
    <div className={className} data-testid={`iteration-plugin-renderer-${cardId}`} data-plugin-status={status}>
      <PluginHost
        plugin={plugin}
        instanceId={cardId}
        grantedPermissions={grantedPermissions}
        onEvent={onEvent}
      />
    </div>
  );
}
