/**
 * Composer selection tests for SPEC-PL-06 §4.1/§4.3.
 * Scenario 2: source-chip reorder invalidates preflight.
 * Scenario 12: underbudget preview disables Reply and shows server text.
 * Scenario 13: the parent can preserve the draft while asking for renewed preflight.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import {
  MultiReferenceComposer,
  type ComposerReferenceSource,
} from '../MessageComposer.tsx';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const SOURCES: ComposerReferenceSource[] = [
  { id: 'source-a', label: 'R1', colorKey: 'ref-0', preview: 'First source' },
  { id: 'source-b', label: 'R2', colorKey: 'ref-1', preview: 'Second source' },
];

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

function renderComposer(overrides: Partial<React.ComponentProps<typeof MultiReferenceComposer>> = {}) {
  const props: React.ComponentProps<typeof MultiReferenceComposer> = {
    sources: SOURCES,
    draft: 'keep this draft',
    onDraftChange: vi.fn(),
    onReorder: vi.fn(),
    onPreflight: vi.fn(),
    onSubmit: vi.fn(),
    onCancel: vi.fn(),
    preflightRequired: false,
    replyDisabled: false,
    submitting: false,
    error: null,
    ...overrides,
  };
  act(() => root.render(createElement(MultiReferenceComposer, props)));
  return props;
}

describe('MultiReferenceComposer', () => {
  it('reorders R# chips and notifies the parent to require a new preflight (scenario 2)', () => {
    const props = renderComposer();
    const moveUp = container.querySelector<HTMLButtonElement>('[aria-label="Move R2 up"]');
    expect(moveUp).not.toBeNull();

    act(() => moveUp!.click());

    expect(props.onReorder).toHaveBeenCalledWith(['source-b', 'source-a']);
  });

  it('disables Reply and surfaces server underbudget text (scenario 12)', () => {
    const props = renderComposer({
      replyDisabled: true,
      preflightRequired: true,
      error: 'REFERENCE_CONTEXT_BUDGET_EXCEEDED: all selected sources must fit',
    });

    const reply = container.querySelector<HTMLButtonElement>('[data-testid="multi-reference-reply"]');
    expect(reply?.disabled).toBe(true);
    expect(container.textContent).toContain('REFERENCE_CONTEXT_BUDGET_EXCEEDED');
    expect(container.textContent).toContain('Run preflight again');
    expect(props.onSubmit).not.toHaveBeenCalled();
  });

  it('keeps the draft visible while renewal is required after a stale token (scenario 13)', () => {
    renderComposer({
      preflightRequired: true,
      error: 'REFERENCE_SELECTION_STALE: token expired. Run preflight again.',
    });

    expect(container.querySelector<HTMLTextAreaElement>('textarea')?.value).toBe('keep this draft');
    expect(container.textContent).toContain('REFERENCE_SELECTION_STALE');
    expect(container.querySelector('[data-testid="multi-reference-preflight"]')).not.toBeNull();
  });
});
