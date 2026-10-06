import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it } from 'vitest';
import ProgressHeader, { ProgressHeaderView } from '../agent/ProgressHeader.tsx';
import { aggregateHeaderProgress, type ProgressAggregationRecord } from '../../lib/iterationProgress.ts';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function record(overrides: Partial<ProgressAggregationRecord> & { phase: string; title: string }): ProgressAggregationRecord {
  return {
    cardId: `card-${overrides.title}`,
    type: 'search',
    current: 1,
    total: 2,
    status: 'running',
    updatedAt: '2026-10-04T12:00:00Z',
    ...overrides,
  } as ProgressAggregationRecord;
}

let container: HTMLElement | null = null;
let root: Root | null = null;
function render(ui: React.ReactElement): HTMLElement {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  act(() => root!.render(ui));
  return container;
}
afterEach(() => {
  act(() => root?.unmount());
  container?.remove();
  container = null;
  root = null;
});

describe('ProgressHeader', () => {
  it('renders segment text from raw records via the aggregation pipeline', () => {
    const view = render(
      <ProgressHeader records={[record({ phase: 'Research', title: 'Find docs' })]} />,
    );
    const segment = view.querySelector('[data-testid="progress-segment"]');
    expect(segment?.textContent).toBe('Find docs: 1/2');
    expect(view.querySelector('[data-testid="iteration-header-status"]')?.textContent).toContain('Find docs: 1/2');
  });

  it('accepts a pre-computed HeaderAggregation without re-aggregating', () => {
    const aggregation = aggregateHeaderProgress([record({ phase: 'Research', title: 'Find docs' })]);
    const view = render(<ProgressHeader aggregation={aggregation} />);
    expect(view.querySelectorAll('[data-testid="progress-segment"]')).toHaveLength(1);
  });

  it('hides the overflow control at or below 3 segments', () => {
    const view = render(
      <ProgressHeader
        records={[
          record({ phase: 'A', title: 'one' }),
          record({ phase: 'B', title: 'two' }),
          record({ phase: 'C', title: 'three' }),
        ]}
      />,
    );
    expect(view.querySelectorAll('[data-testid="progress-segment"]')).toHaveLength(3);
    expect(view.querySelector('[data-testid="progress-header-overflow"]')).toBeNull();
  });

  it('shows overflow count and expands the groups section on toggle', () => {
    const view = render(
      <ProgressHeader
        records={[
          record({ phase: 'A', title: 'one' }),
          record({ phase: 'B', title: 'two' }),
          record({ phase: 'C', title: 'three' }),
          record({ phase: 'D', title: 'four' }),
          record({ phase: 'E', title: 'five' }),
        ]}
      />,
    );
    const toggle = view.querySelector('[data-testid="progress-header-overflow"]') as HTMLButtonElement;
    expect(toggle?.textContent).toContain('+2 more');
    // Groups section not rendered before expansion.
    expect(view.querySelector('[data-testid="progress-header-groups"]')).toBeNull();
    act(() => toggle.click());
    const groups = view.querySelectorAll('[data-testid="progress-header-group"]');
    expect(groups).toHaveLength(5);
    expect(groups[4]?.textContent).toContain('E');
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
  });

  it('renders the groups section directly when given expanded via ProgressHeaderView', () => {
    const aggregation = aggregateHeaderProgress([
      record({ phase: 'Research', title: 'Find docs' }),
      record({ phase: 'Verify', title: 'Check results' }),
      record({ phase: 'Synthesize', title: 'Merge findings' }),
      record({ phase: 'Report', title: 'Write summary' }),
    ]);
    expect(aggregation.overflow).toBe(1);
    const view = render(<ProgressHeaderView aggregation={aggregation} groupsExpanded />);
    expect(view.querySelectorAll('[data-testid="progress-header-group"]')).toHaveLength(4);
  });
});
