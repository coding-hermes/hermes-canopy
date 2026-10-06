/**
 * Hermes Canopy — ProgressHeader (SPEC-PL-04 §8.3)
 *
 * Renders the header aggregation produced by `iterationProgress.ts`. Consumes
 * either raw card progress records (aggregated internally) or a pre-computed
 * `HeaderAggregation`, so callers that already hold the aggregation (e.g.
 * IterationSidePanel) can skip a second pass.
 */

import { useState } from 'react';
import { ChevronDown, ChevronRight } from 'lucide-react';
import {
  aggregateHeaderProgress,
  formatHeaderSegment,
  formatHeaderStatus,
  type HeaderAggregation,
  type HeaderSegment,
  type ProgressAggregationRecord,
} from '../../lib/iterationProgress.ts';

export interface ProgressHeaderProps {
  /** Raw card progress records; aggregated when `aggregation` is absent. */
  records?: readonly ProgressAggregationRecord[];
  /** Pre-computed aggregation (wins over `records`). */
  aggregation?: HeaderAggregation;
  /** Initial expanded state of the overflow groups section. */
  defaultGroupsExpanded?: boolean;
}

function segmentTone(segment: HeaderSegment): string {
  switch (segment.status) {
    case 'pending_approval':
    case 'waiting_for_user': return 'bg-amber-500/20 text-amber-300';
    case 'running': return 'bg-purple-500/20 text-purple-300';
    case 'failed':
    case 'cancelled': return 'bg-red-500/20 text-red-300';
    default: return 'bg-green-500/20 text-green-300';
  }
}

/** Named export for direct testing; the default export wraps it. */
export function ProgressHeaderView({
  aggregation,
  groupsExpanded,
  onToggleGroups,
}: {
  aggregation: HeaderAggregation;
  groupsExpanded: boolean;
  onToggleGroups?: () => void;
}) {
  const status = formatHeaderStatus(aggregation) || 'No active progress';
  const hasOverflow = aggregation.overflow > 0;
  return (
    <div data-testid="progress-header">
      <p className="mt-1 text-xs text-content-secondary" aria-live="polite" data-testid="iteration-header-status">{status}</p>
      <ul role="list" className="mt-2 flex flex-wrap gap-1.5" aria-label="Progress segments">
        {aggregation.segments.map((segment) => (
          <li
            key={`${segment.group}-${segment.title}`}
            className={`inline-flex items-center gap-1 rounded-full px-1.5 py-0.5 text-xs ${segmentTone(segment)}`}
            data-testid="progress-segment"
            data-segment-group={segment.group}
          >
            {formatHeaderSegment(segment)}
          </li>
        ))}
      </ul>
      {hasOverflow && (
        <button
          type="button"
          onClick={onToggleGroups}
          aria-expanded={groupsExpanded}
          aria-controls="progress-header-groups"
          className="mt-2 inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs text-content-muted hover:bg-surface-hover hover:text-content-primary"
          data-testid="progress-header-overflow"
        >
          {groupsExpanded ? <ChevronDown className="h-3 w-3" aria-hidden="true" /> : <ChevronRight className="h-3 w-3" aria-hidden="true" />}
          {groupsExpanded ? 'Show fewer' : `+${aggregation.overflow} more`}
        </button>
      )}
      {hasOverflow && groupsExpanded && (
        <div id="progress-header-groups" className="mt-2 space-y-1" data-testid="progress-header-groups">
          {aggregation.groups.map((group) => (
            <p key={group.name} className="text-xs text-content-muted" data-testid="progress-header-group">
              <span className="font-medium text-content-secondary">{group.name}</span>
              {' · '}
              {group.indeterminate || group.total <= 0 ? 'in progress' : `${group.current}/${group.total}`}
              {' · '}
              {group.records.length} card{group.records.length === 1 ? '' : 's'}
            </p>
          ))}
        </div>
      )}
      {hasOverflow && <p className="sr-only">{aggregation.overflow} additional progress segments are available in the card list.</p>}
    </div>
  );
}

export default function ProgressHeader({ records, aggregation, defaultGroupsExpanded = false }: ProgressHeaderProps) {
  const [groupsExpanded, setGroupsExpanded] = useState(defaultGroupsExpanded);
  const computed = aggregation ?? aggregateHeaderProgress(records ?? []);
  return (
    <ProgressHeaderView
      aggregation={computed}
      groupsExpanded={groupsExpanded}
      onToggleGroups={() => setGroupsExpanded((value) => !value)}
    />
  );
}
