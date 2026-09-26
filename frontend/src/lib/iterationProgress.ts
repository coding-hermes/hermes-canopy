/**
 * Pure progress projection for SPEC-PL-04 §8.3.
 *
 * The renderer consumes this module rather than deriving percentages locally.
 * That keeps the cardinal rules (especially indeterminate code execution and
 * tool approval) identical in the header, compact cards, and tests.
 */

import type { CardProgress, ProgressStatus, ProgressType } from '../types/agent.ts';

export interface ProgressInput {
  cardId: string;
  parentCardId?: string;
  type: ProgressType;
  title: string;
  current?: number;
  total?: number;
  status: ProgressStatus;
  phase?: string;
  updatedAt: string;
}

export interface ProgressAggregationRecord extends CardProgress {
  /** Base-card lifecycle, used to exclude dismissed/archived history. */
  cardStatus?: 'active' | 'dismissed' | 'archived';
  /** A tool can be waiting even though its normalized progress status is running. */
  pendingApproval?: boolean;
  waitingForUser?: boolean;
  /** Optional agent-declared phase ordering. */
  phaseOrdinal?: number;
}

export interface ProgressGroup {
  name: string;
  records: ProgressAggregationRecord[];
  current: number;
  total: number;
  indeterminate: boolean;
  earliestUpdatedAt: string;
}

export interface HeaderSegment {
  group: string;
  title: string;
  current: number;
  total: number;
  status: ProgressStatus | 'waiting_for_user' | 'pending_approval';
  indeterminate: boolean;
}

export interface HeaderAggregation {
  groups: ProgressGroup[];
  segments: HeaderSegment[];
  overflow: number;
}

const TERMINAL_EXCLUDED = new Set(['dismissed', 'archived']);

function numeric(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : 0;
}

/** Apply the five subtype cardinal rules without inventing a percentage. */
export function normalizeProgress(input: ProgressInput): CardProgress {
  const current = numeric(input.current);
  const total = numeric(input.total);
  let normalizedCurrent = current;
  let normalizedTotal = total;

  if (input.type === 'code_exec') {
    normalizedCurrent = input.status === 'completed' ? 1 : 0;
    normalizedTotal = input.status === 'completed' ? 1 : 0;
  } else if (input.type === 'tool_call') {
    normalizedCurrent = input.status === 'completed' || input.status === 'failed' || input.status === 'cancelled' ? 1 : 0;
    normalizedTotal = 1;
  }

  return {
    cardId: input.cardId,
    ...(input.parentCardId ? { parentCardId: input.parentCardId } : {}),
    type: input.type,
    title: input.title,
    current: normalizedCurrent,
    total: normalizedTotal,
    status: input.status,
    ...(input.phase ? { phase: input.phase } : {}),
    updatedAt: input.updatedAt,
  };
}

function recordPriority(record: ProgressAggregationRecord): number {
  if (record.waitingForUser || record.pendingApproval || record.status === 'running' && record.type === 'tool_call' && record.total === 1 && record.current === 0) return 0;
  if (record.status === 'running') return 1;
  if (record.status === 'failed' || record.status === 'cancelled') return 2;
  return 3;
}

function titleForGroup(group: ProgressGroup): string {
  const first = group.records[0];
  if (!first) return group.name;
  if (group.records.length === 1) {
    return first.type === 'tool_call' && !first.title.startsWith('Tool:') ? `Tool: ${first.title}` : first.title;
  }
  return first.type === 'code_exec' ? 'Code' : first.type === 'file_read' ? 'File' : first.type === 'tool_call' ? 'Tool' : first.type === 'thinking' ? 'Thinking' : 'Search';
}

/**
 * Implement the header algorithm from §8.3. `phaseOrdinals` is the declared
 * agent order; without it groups use their earliest committed update time.
 */
export function aggregateHeaderProgress(
  records: readonly ProgressAggregationRecord[],
  phaseOrdinals: Readonly<Record<string, number>> = {},
): HeaderAggregation {
  const groupsByName = new Map<string, ProgressGroup>();
  for (const record of records) {
    if (record.cardStatus && TERMINAL_EXCLUDED.has(record.cardStatus)) continue;
    const name = record.phase?.trim() || 'Ungrouped';
    const existing = groupsByName.get(name);
    if (existing) {
      existing.records.push(record);
      if (record.total > 0) {
        existing.current += numeric(record.current);
        existing.total += numeric(record.total);
      }
      if (record.updatedAt < existing.earliestUpdatedAt) existing.earliestUpdatedAt = record.updatedAt;
      existing.indeterminate ||= record.total <= 0;
    } else {
      groupsByName.set(name, {
        name,
        records: [record],
        current: record.total > 0 ? numeric(record.current) : 0,
        total: record.total > 0 ? numeric(record.total) : 0,
        indeterminate: record.total <= 0,
        earliestUpdatedAt: record.updatedAt,
      });
    }
  }

  const groups = [...groupsByName.values()].sort((a, b) => {
    const declaredOrdinals: Record<string, number> = { ...phaseOrdinals };
    for (const record of [...a.records, ...b.records]) {
      if (record.phase && record.phaseOrdinal !== undefined && declaredOrdinals[record.phase] === undefined) declaredOrdinals[record.phase] = record.phaseOrdinal;
    }
    const aOrdinal = declaredOrdinals[a.name];
    const bOrdinal = declaredOrdinals[b.name];
    if (aOrdinal !== undefined || bOrdinal !== undefined) {
      if (aOrdinal === undefined) return 1;
      if (bOrdinal === undefined) return -1;
      if (aOrdinal !== bOrdinal) return aOrdinal - bOrdinal;
    }
    return a.earliestUpdatedAt.localeCompare(b.earliestUpdatedAt);
  });

  const candidates = groups
    .flatMap((group) => {
      const recordsInGroup = [...group.records].sort((a, b) => recordPriority(a) - recordPriority(b));
      const lead = recordsInGroup[0];
      if (!lead) return [];
      const waiting = recordsInGroup.some((record) => record.waitingForUser || record.pendingApproval || record.status === 'running' && record.type === 'tool_call' && record.current === 0);
      const status = waiting ? (recordsInGroup.some((record) => record.pendingApproval) ? 'pending_approval' : 'waiting_for_user') : lead.status;
      return [{
        group: group.name,
        title: titleForGroup(group),
        current: group.current,
        total: group.total,
        status,
        indeterminate: group.indeterminate,
      } satisfies HeaderSegment];
    })
    .sort((a, b) => {
      const aPriority = a.status === 'waiting_for_user' || a.status === 'pending_approval' ? 0 : a.status === 'running' ? 1 : a.status === 'failed' || a.status === 'cancelled' ? 2 : 3;
      const bPriority = b.status === 'waiting_for_user' || b.status === 'pending_approval' ? 0 : b.status === 'running' ? 1 : b.status === 'failed' || b.status === 'cancelled' ? 2 : 3;
      return aPriority - bPriority;
    });

  return { groups, segments: candidates.slice(0, 3), overflow: Math.max(0, candidates.length - 3) };
}

export function formatHeaderSegment(segment: HeaderSegment): string {
  if (segment.indeterminate || segment.total <= 0) return `${segment.title}`;
  return `${segment.title}: ${segment.current}/${segment.total}`;
}

export function formatHeaderStatus(aggregation: HeaderAggregation): string {
  return aggregation.segments.map(formatHeaderSegment).join(' | ');
}
