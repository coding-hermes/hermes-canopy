import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import IterationPluginRenderer, { iterationPluginStatus } from '../agent/IterationPluginRenderer.tsx';
import type { SearchCardSubtypeData } from '../../types/agent.ts';
import type { PluginManifest } from '../../lib/pluginTypes';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function searchData(): SearchCardSubtypeData {
  return {
    subtype: 'iteration_search',
    title: 'Find docs',
    state: 'running',
    progress: { cardId: 'card-1', type: 'search', title: 'Find docs', current: 0, total: 3, status: 'running', updatedAt: '2026-10-04T12:00:00Z' },
    urlsSearched: [],
    currentBatch: [],
    focusUrls: null,
  };
}

const manifest: PluginManifest = {
  id: 'iter-plugin',
  name: 'Iteration Plugin',
  version: '1.0.0',
  description: 'Iteration card bridge plugin',
  permissions: [],
  render_type: 'card',
  entry_point: 'main.js',
} as unknown as PluginManifest;

const plugin = { id: 'iter-plugin', name: 'Iteration Plugin', manifest, sourceJS: 'void 0;' };

function fakeEventSource() {
  const listeners = new Map<string, EventListener>();
  return {
    addEventListener: (type: string, listener: EventListener) => listeners.set(type, listener),
    removeEventListener: (type: string) => listeners.delete(type),
    emit(type: string) { listeners.get(type)?.(new Event(type)); },
    hasListener: (type: string) => listeners.has(type),
  };
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

describe('IterationPluginRenderer', () => {
  it('renders nothing without an associated plugin', () => {
    const view = render(<IterationPluginRenderer cardId="card-1" data={searchData()} />);
    expect(view.querySelector('[data-testid="iteration-plugin-renderer-card-1"]')).toBeNull();
  });

  it('mounts the PluginHost sandbox for a card with a plugin', () => {
    const view = render(<IterationPluginRenderer cardId="card-1" data={searchData()} plugin={plugin} />);
    const host = view.querySelector('[data-testid="iteration-plugin-renderer-card-1"]');
    expect(host).not.toBeNull();
    expect(host!.querySelector('iframe')).not.toBeNull();
    expect(host!.querySelector('iframe')?.getAttribute('sandbox')).toBe('allow-scripts');
  });

  it('notifies the plugin on mount and on status transitions', () => {
    const onEvent = vi.fn();
    render(<IterationPluginRenderer cardId="card-1" data={searchData()} plugin={plugin} onEvent={onEvent} />);
    // One notification for the initial status.
    const initialCalls = onEvent.mock.calls.length;
    expect(initialCalls).toBeGreaterThan(0);
    expect(onEvent.mock.calls[0][0]).toMatchObject({ event: 'notification', data: { title: 'Find docs' } });
    // Same status again → no duplicate notification.
    act(() => root!.render(<IterationPluginRenderer cardId="card-1" data={searchData()} plugin={plugin} onEvent={onEvent} />));
    expect(onEvent.mock.calls.length).toBe(initialCalls);
    // State change → new notification.
    const completed = { ...searchData(), state: 'completed' as const, progress: { ...searchData().progress, status: 'completed' as const, current: 3 } };
    act(() => root!.render(<IterationPluginRenderer cardId="card-1" data={completed} plugin={plugin} onEvent={onEvent} />));
    expect(onEvent.mock.calls.length).toBe(initialCalls + 1);
    const last = onEvent.mock.calls.at(-1)![0] as { data: { body: string; level: string } };
    expect(last.data.body).toContain('completed');
    expect(last.data.level).toBe('success');
  });

  it('forwards card_dismissed frames from the SSE stream and cleans up on unmount', () => {
    const onDismissed = vi.fn();
    const source = fakeEventSource();
    const view = render(
      <IterationPluginRenderer cardId="card-1" data={searchData()} plugin={plugin} eventSource={source as any} onDismissed={onDismissed} />,
    );
    expect(source.hasListener('card_dismissed')).toBe(true);
    act(() => source.emit('card_dismissed'));
    expect(onDismissed).toHaveBeenCalledWith('card-1');
    act(() => root!.unmount());
    expect(source.hasListener('card_dismissed')).toBe(false);
    root = null;
    container!.remove();
    container = null;
    void view;
  });

  it('does not subscribe to the stream when no plugin is associated', () => {
    const source = fakeEventSource();
    render(<IterationPluginRenderer cardId="card-1" data={searchData()} eventSource={source as any} />);
    expect(source.hasListener('card_dismissed')).toBe(false);
  });

  it('maps card state to a readable plugin status string', () => {
    expect(iterationPluginStatus(searchData())).toBe('running · 0/3');
    expect(iterationPluginStatus({ ...searchData(), state: 'waiting_for_user' })).toBe('waiting for user · 0/3');
  });
});
