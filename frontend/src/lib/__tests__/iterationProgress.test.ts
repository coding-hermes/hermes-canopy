import { describe, expect, it } from 'vitest';
import {
  aggregateHeaderProgress,
  formatHeaderStatus,
  normalizeProgress,
  type ProgressAggregationRecord,
} from '../iterationProgress.ts';

function record(over: Partial<ProgressAggregationRecord> = {}): ProgressAggregationRecord {
  return {
    cardId: over.cardId ?? 'card-1',
    type: over.type ?? 'search',
    title: over.title ?? 'Search',
    current: over.current ?? 0,
    total: over.total ?? 0,
    status: over.status ?? 'running',
    updatedAt: over.updatedAt ?? '2026-09-26T12:00:00.000Z',
    ...over,
  };
}

describe('iteration progress normalization — SPEC-PL-04 §8.3', () => {
  it('applies code execution and tool-call cardinal semantics', () => {
    expect(normalizeProgress({ cardId: 'code', type: 'code_exec', title: 'npm test', current: 4, total: 10, status: 'running', updatedAt: '2026-09-26T00:00:00Z' })).toMatchObject({ current: 0, total: 0 });
    expect(normalizeProgress({ cardId: 'code', type: 'code_exec', title: 'npm test', status: 'completed', updatedAt: '2026-09-26T00:00:00Z' })).toMatchObject({ current: 1, total: 1 });
    expect(normalizeProgress({ cardId: 'tool', type: 'tool_call', title: 'approve file_write', status: 'running', updatedAt: '2026-09-26T00:00:00Z' })).toMatchObject({ current: 0, total: 1 });
    expect(normalizeProgress({ cardId: 'tool', type: 'tool_call', title: 'file_write', status: 'failed', updatedAt: '2026-09-26T00:00:00Z' })).toMatchObject({ current: 1, total: 1 });
  });

  it('keeps file ranges and wire identity fields intact', () => {
    expect(normalizeProgress({ cardId: 'file', parentCardId: 'parent', type: 'file_read', title: 'main.go', current: 20, total: 100, status: 'running', phase: 'read', updatedAt: '2026-09-26T00:00:00Z' })).toEqual({
      cardId: 'file', parentCardId: 'parent', type: 'file_read', title: 'main.go', current: 20, total: 100, status: 'running', phase: 'read', updatedAt: '2026-09-26T00:00:00Z',
    });
  });
});

describe('header aggregation — exact grouping and relevance rules', () => {
  it('groups phase and ungrouped records, sums determinate values, and marks mixed groups indeterminate', () => {
    const result = aggregateHeaderProgress([
      record({ cardId: 'a', phase: 'research', current: 2, total: 5 }),
      record({ cardId: 'b', phase: 'research', current: 0, total: 0, title: 'Code output' }),
      record({ cardId: 'c', current: 3, total: 4, updatedAt: '2026-09-26T12:01:00.000Z' }),
    ]);
    expect(result.groups.map((group) => group.name)).toEqual(['research', 'Ungrouped']);
    expect(result.groups[0]).toMatchObject({ current: 2, total: 5, indeterminate: true });
    expect(formatHeaderStatus(result)).toContain('Search');
    expect(formatHeaderStatus(result)).not.toContain('2/5');
  });

  it('uses declared phase ordinal over timestamps and excludes dismissed history', () => {
    const result = aggregateHeaderProgress([
      record({ cardId: 'late', phase: 'phase-2', updatedAt: '2026-09-26T12:02:00.000Z', current: 1, total: 1 }),
      record({ cardId: 'early', phase: 'phase-1', updatedAt: '2026-09-26T12:03:00.000Z', current: 1, total: 2 }),
      record({ cardId: 'old', phase: 'phase-0', cardStatus: 'dismissed', current: 99, total: 100 }),
    ], { 'phase-1': 1, 'phase-2': 2 });
    expect(result.groups.map((group) => group.name)).toEqual(['phase-1', 'phase-2']);
    expect(result.groups.some((group) => group.name === 'phase-0')).toBe(false);
  });

  it('renders waiting approval before running, then failed/cancelled, with accessible overflow', () => {
    const result = aggregateHeaderProgress([
      record({ cardId: 'running', title: 'Running', phase: 'run', current: 1, total: 2 }),
      record({ cardId: 'approval', type: 'tool_call', title: 'approve file_write', phase: 'approval', pendingApproval: true }),
      record({ cardId: 'failed', title: 'Failed', phase: 'failed', status: 'failed' }),
      record({ cardId: 'cancelled', title: 'Cancelled', phase: 'cancelled', status: 'cancelled' }),
    ]);
    expect(result.segments[0]?.status).toBe('pending_approval');
    expect(result.segments[1]?.status).toBe('running');
    expect(result.segments[2]?.status).toBe('failed');
    expect(result.overflow).toBe(1);
  });

  it('reproduces the spec header example', () => {
    const result = aggregateHeaderProgress([
      record({ cardId: 'thinking', type: 'thinking', title: 'Thinking', phase: 'thinking', current: 3, total: 5 }),
      record({ cardId: 'search', type: 'search', title: 'Search', phase: 'search', current: 4, total: 4, updatedAt: '2026-09-26T12:01:00.000Z' }),
      record({ cardId: 'tool', type: 'tool_call', title: 'approve file_write', phase: 'tool', status: 'completed', updatedAt: '2026-09-26T12:02:00.000Z' }),
    ]);
    expect(formatHeaderStatus(result)).toBe('Thinking: 3/5 | Search: 4/4 | Tool: approve file_write');
  });
});
