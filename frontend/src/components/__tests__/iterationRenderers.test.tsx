import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { IterationSearchCard } from '../agent/IterationSearchCard.tsx';
import { IterationCodeExecCard } from '../agent/IterationCodeExecCard.tsx';
import { IterationFileReadCard } from '../agent/IterationFileReadCard.tsx';
import { IterationThinkingCard } from '../agent/IterationThinkingCard.tsx';
import { IterationToolCallCard } from '../agent/IterationToolCallCard.tsx';
import { iterationRenderers, resolveIterationRenderer } from '../../lib/iterationRenderers.ts';
import type {
  CardProgress,
  CodeExecSubtypeData,
  FileReadSubtypeData,
  IterationFeedbackInput,
  IterationRendererProps,
  SearchCardSubtypeData,
  ThinkingData,
  ToolCallSubtypeData,
} from '../../types/agent.ts';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function progress(type: CardProgress['type'], title: string): CardProgress {
  return { cardId: 'card-1', type, title, current: 0, total: 3, status: 'running', updatedAt: '2026-10-04T12:00:00Z' };
}

function searchData(): SearchCardSubtypeData {
  return {
    subtype: 'iteration_search',
    title: 'Find docs',
    state: 'running',
    progress: progress('search', 'Find docs'),
    urlsSearched: ['https://example.com/docs'],
    currentBatch: [
      { url: 'https://example.com/docs', snippet: 'The browser reconnects automatically.', snippetId: 'r1', status: 'retrieved' },
      { url: 'https://example.com/api', snippet: 'API reference.', snippetId: 'r2', status: 'searching' },
    ],
    focusUrls: null,
  };
}

function codeData(): CodeExecSubtypeData {
  return {
    subtype: 'iteration_code_exec',
    title: 'Run tests',
    state: 'running',
    progress: progress('code_exec', 'Run tests'),
    command: 'go test ./...',
    workdir: '/workspace/canopy',
    status: 'running',
    stdout: ['=== RUN   TestCard\n'],
    stderr: [],
    exitCode: null,
    startTime: '2026-10-04T12:00:00Z',
    endTime: null,
    cancelled: false,
  };
}

function fileData(): FileReadSubtypeData {
  return {
    subtype: 'iteration_file_read',
    title: 'Inspect service',
    state: 'completed',
    progress: progress('file_read', 'Inspect service'),
    path: 'internal/iteration/service.go',
    absolutePath: '/workspace/canopy/internal/iteration/service.go',
    size: 18240,
    mimeType: 'text/x-go',
    language: 'go',
    lineCount: 614,
    highlights: [{ startLine: 42, endLine: 47, label: 'cancel path', note: null, color: 'yellow' }],
    visibleLines: { start: 1, end: 160 },
  };
}

function thinkingData(): ThinkingData {
  return {
    subtype: 'iteration_thinking',
    title: 'Assess routing',
    state: 'running',
    progress: progress('thinking', 'Assess routing'),
    steps: [
      { id: 'step-1', title: 'Inspect contract', status: 'completed', content: 'The base card service persists events.', duration_ms: 240, error: null },
      { id: 'step-2', title: 'Decide routing', status: 'active', content: null, duration_ms: null, error: null },
    ],
    currentStepId: 'step-2',
  };
}

function toolData(): ToolCallSubtypeData {
  return {
    subtype: 'iteration_tool_call',
    title: 'Write migration',
    state: 'waiting_for_user',
    progress: progress('tool_call', 'Write migration'),
    toolName: 'write_file',
    params: { path: 'migrations/000002.sql' },
    result: null,
    status: 'pending_approval',
    startTime: null,
    endTime: null,
    durationMs: null,
    error: null,
    gated: true,
  };
}

let container: HTMLDivElement;
let root: Root;

function mount(node: React.ReactNode): HTMLDivElement {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  act(() => root.render(node));
  return container;
}

afterEach(async () => {
  if (root && container) {
    await act(async () => root.unmount());
    container.remove();
  }
});

describe('iteration renderer registry', () => {
  it('maps every subtype to a component', () => {
    expect(resolveIterationRenderer('iteration_search')).toBe(iterationRenderers.iteration_search);
    expect(resolveIterationRenderer('iteration_code_exec')).toBe(iterationRenderers.iteration_code_exec);
    expect(resolveIterationRenderer('iteration_file_read')).toBe(iterationRenderers.iteration_file_read);
    expect(resolveIterationRenderer('iteration_thinking')).toBe(iterationRenderers.iteration_thinking);
    expect(resolveIterationRenderer('iteration_tool_call')).toBe(iterationRenderers.iteration_tool_call);
    expect(resolveIterationRenderer('unknown')).toBeNull();
  });
});

describe('IterationSearchCard', () => {
  const props = (data: SearchCardSubtypeData, over: Partial<IterationRendererProps<SearchCardSubtypeData>> = {}): IterationRendererProps<SearchCardSubtypeData> => ({
    cardId: 'card-1', data, compact: false, ...over,
  });

  it('renders compact summary and expanded results with per-result controls', () => {
    const compact = mount(createElement(IterationSearchCard, props(searchData(), { compact: true })));
    expect(compact.textContent).toContain('1/3');
    expect(compact.textContent).toContain('https://example.com/api');

    mount(createElement(IterationSearchCard, props(searchData(), { compact: false, submitFeedback: vi.fn() })));
    expect(container.textContent).toContain('The browser reconnects automatically.');
    expect(container.textContent).toContain('URLs searched');
    expect(container.querySelector('[data-testid="search-approve-r1"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="search-reject-r1"]')).not.toBeNull();
  });

  it('renders server strings as text, never as HTML', () => {
    const data = searchData();
    data.currentBatch[0]!.snippet = '<img src=x onerror=alert(1)>';
    mount(createElement(IterationSearchCard, props(data)));
    expect(container.querySelector('img')).toBeNull();
    expect(container.textContent).toContain('<img src=x onerror=alert(1)>');
  });

  it('owns a polite live region', () => {
    mount(createElement(IterationSearchCard, props(searchData())));
    const live = container.querySelector('[data-testid="iteration-live-card-1"]');
    expect(live?.getAttribute('role')).toBe('status');
    expect(live?.getAttribute('aria-live')).toBe('polite');
  });
});

describe('IterationCodeExecCard', () => {
  it('shows the command compactly and separates stdout/stderr when expanded', () => {
    const compact = mount(createElement(IterationCodeExecCard, { cardId: 'card-1', data: codeData(), compact: true }));
    expect(compact.textContent).toContain('go test ./...');

    mount(createElement(IterationCodeExecCard, { cardId: 'card-1', data: codeData(), compact: false }));
    expect(container.textContent).toContain('=== RUN   TestCard');
    expect(container.querySelector('[data-testid="iteration-code-stdout-card-1"]')).not.toBeNull();
    expect(container.textContent).toContain('/workspace/canopy');
  });

  it('offers cancel only while running, and only when a cancel callback is provided', () => {
    const cancel = vi.fn();
    mount(createElement(IterationCodeExecCard, { cardId: 'card-1', data: codeData(), compact: false, cancel }));
    const button = container.querySelector('[aria-label="Cancel Run tests"]') as HTMLButtonElement;
    expect(button).not.toBeNull();
    act(() => button.click());
    expect(cancel).toHaveBeenCalledWith('Cancelled by user');

    mount(createElement(IterationCodeExecCard, { cardId: 'card-1', data: { ...codeData(), status: 'completed', exitCode: 0, endTime: '2026-10-04T12:01:00Z' }, compact: false, cancel }));
    expect(container.querySelector('[aria-label="Cancel Run tests"]')).toBeNull();
    expect(container.textContent).toContain('exit 0');

    mount(createElement(IterationCodeExecCard, { cardId: 'card-1', data: codeData(), compact: false }));
    expect(container.querySelector('[aria-label="Cancel Run tests"]')).toBeNull();
  });
});

describe('IterationFileReadCard', () => {
  it('renders compact file identity and expanded metadata, annotations, and controls', () => {
    const compact = mount(createElement(IterationFileReadCard, { cardId: 'card-1', data: fileData(), compact: true }));
    expect(compact.textContent).toContain('internal/iteration/service.go');
    expect(compact.textContent).toContain('lines 1-160');

    const submitFeedback = vi.fn();
    mount(createElement(IterationFileReadCard, { cardId: 'card-1', data: fileData(), compact: false, submitFeedback }));
    expect(container.textContent).toContain('cancel path');
    expect(container.textContent).toContain('614');
    expect(container.querySelector('[data-testid="iteration-file-add-card-1"]')).not.toBeNull();
  });

  it('submits a highlight with the selected range, label, note, and color', () => {
    const submitFeedback = vi.fn();
    mount(createElement(IterationFileReadCard, { cardId: 'card-1', data: fileData(), compact: false, submitFeedback }));
    const add = container.querySelector('[data-testid="iteration-file-add-card-1"]') as HTMLButtonElement;
    act(() => add.click());
    const input = submitFeedback.mock.calls[0]?.[0] as IterationFeedbackInput;
    expect(input.feedbackType).toBe('highlight');
    expect(input.target).toMatchObject({ region: { startLine: 1, endLine: 160 } });
  });
});

describe('IterationThinkingCard', () => {
  it('renders the active step compactly and collapsible steps when expanded', () => {
    const compact = mount(createElement(IterationThinkingCard, { cardId: 'card-1', data: thinkingData(), compact: true }));
    expect(compact.textContent).toContain('Decide routing');

    mount(createElement(IterationThinkingCard, { cardId: 'card-1', data: thinkingData(), compact: false }));
    const toggle = container.querySelector('[aria-label="Expand step Decide routing for Assess routing"]') as HTMLButtonElement;
    expect(toggle).not.toBeNull();
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    act(() => toggle.click());
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
  });
});

describe('IterationToolCallCard', () => {
  it('shows the tool name compactly and formatted JSON params when expanded', () => {
    const compact = mount(createElement(IterationToolCallCard, { cardId: 'card-1', data: toolData(), compact: true }));
    expect(compact.textContent).toContain('write_file');
    expect(compact.textContent).toContain('Pending approval');

    mount(createElement(IterationToolCallCard, { cardId: 'card-1', data: toolData(), compact: false }));
    expect(container.textContent).toContain('migrations/000002.sql');
  });

  it('offers approve/deny only when gated and pending approval', () => {
    const submitFeedback = vi.fn();
    mount(createElement(IterationToolCallCard, { cardId: 'card-1', data: toolData(), compact: false, submitFeedback }));
    expect(container.querySelector('[data-testid="iteration-tool-approve-card-1"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="iteration-tool-deny-card-1"]')).not.toBeNull();

    mount(createElement(IterationToolCallCard, { cardId: 'card-1', data: { ...toolData(), status: 'completed', gated: true }, compact: false, submitFeedback }));
    expect(container.querySelector('[data-testid="iteration-tool-approve-card-1"]')).toBeNull();
    expect(container.querySelector('[data-testid="iteration-tool-deny-card-1"]')).toBeNull();

    mount(createElement(IterationToolCallCard, { cardId: 'card-1', data: { ...toolData(), gated: false }, compact: false, submitFeedback }));
    expect(container.querySelector('[data-testid="iteration-tool-approve-card-1"]')).toBeNull();
  });

  it('caps untrusted result JSON with the safe formatter', () => {
    mount(createElement(IterationToolCallCard, { cardId: 'card-1', data: { ...toolData(), status: 'completed', result: { blob: 'z'.repeat(200_000) } }, compact: false }));
    expect(container.textContent).toContain('[truncated]');
    expect(container.querySelector('b')).toBeNull();
  });
});
