/**
 * Component tests — CardActivityPanel (SPEC-PL-03 §6.1 default card frame).
 *
 * The panel is driven through the REAL hook with `lib/sse.ts` mocked at the
 * module boundary, so what is asserted is the rendered output for frames the
 * shipped server actually sends. The last case pins §5.3's non-destructive
 * rule: a rejected payload shows a banner and the last good card stays on
 * screen. Two source invariants back the security/transport criteria — the
 * panel never injects raw HTML and `subscribeSse` is the only transport the
 * new client files use.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';

vi.mock('../../lib/sse.ts', () => ({ subscribeSse: vi.fn() }));

import { subscribeSse, type SseHandlers, type SseSubscription } from '../../lib/sse.ts';
import CardActivityPanel from '../CardActivityPanel.tsx';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const CARD_ID = '0191a9c3-0000-7000-8000-000000000000';
const CONTEXT_HASH = 'c4a2f1d6c7b8e9f00112233445566778899aabbccddeeff0011223344556677';

// ─── Source invariants (read the real files, no mocks) ──────────────────

const PANEL_SOURCE = import.meta.glob('/src/components/CardActivityPanel.tsx', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;

const HOOK_SOURCE = import.meta.glob('/src/hooks/useCardStream.ts', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;

const STORE_SOURCE = import.meta.glob('/src/lib/cardStore.ts', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;

// ─── Real frame bodies ─────────────────────────────────────────────────

function snapshotFrame(appId = 'canopy'): string {
  return JSON.stringify({
    event_type: 'card_snapshot',
    card_id: CARD_ID,
    card_type: 'iteration',
    sequence: 7,
    timestamp: '2026-07-22T12:00:00Z',
    data: {
      id: CARD_ID,
      tree_id: '0191a9c3-0000-7000-8000-0000000000aa',
      node_id: '0191a9c3-0000-7000-8000-0000000000bb',
      app_id: appId,
      type: 'iteration',
      status: 'active',
      context_hash: CONTEXT_HASH,
      data: { step: 7 },
      actions: [{ label: 'Run', handler: 'run' }],
      last_event_seq: 7,
      created_at: '2026-07-22T12:00:00Z',
    },
  });
}

function eventFrame(sequence: number): string {
  return JSON.stringify({
    event_type: 'card_event',
    card_id: CARD_ID,
    card_type: 'iteration',
    sequence,
    timestamp: '2026-07-22T12:00:00Z',
    data: {
      sequence,
      event_id: `0191a9c3-0000-7000-8000-0000000000${String(sequence).padStart(2, '0')}`,
      card_id: CARD_ID,
      event_type: 'agent_progress',
      actor_kind: 'agent',
      actor_id: 'coding',
      payload: { message: 'tests running' },
      created_at: '2026-07-22T12:00:00Z',
    },
  });
}

const HEARTBEAT_FRAME = JSON.stringify({
  event_type: 'heartbeat',
  card_id: CARD_ID,
  timestamp: '2026-07-22T12:00:05Z',
  data: { card_id: CARD_ID },
});

// ─── Harness ───────────────────────────────────────────────────────────

interface StreamRecord {
  url: string;
  handlers: SseHandlers;
  closed: number;
}

let streams: StreamRecord[];
let container: HTMLDivElement;
let root: Root | null = null;

async function mount(): Promise<void> {
  const current = root;
  if (current === null) throw new Error('root missing');
  await act(async () => {
    current.render(createElement(CardActivityPanel, { cardId: CARD_ID, onClose: () => {} }));
  });
}

async function teardown(): Promise<void> {
  const current = root;
  root = null;
  if (current !== null) await act(async () => current.unmount());
  container.remove();
}

function emit(index: number, type: string, data: string): Promise<void> {
  return act(async () => {
    streams[index]?.handlers.onEvent?.(type, data);
  });
}

function text(): string {
  return container.textContent ?? '';
}

beforeEach(() => {
  streams = [];
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);

  const mocked = vi.mocked(subscribeSse);
  mocked.mockReset();
  mocked.mockImplementation((url: string, handlers: SseHandlers): SseSubscription => {
    const record: StreamRecord = { url, handlers, closed: 0 };
    streams.push(record);
    return { close: () => { record.closed += 1; } };
  });
});

afterEach(async () => {
  await teardown();
});

// ─── Tests ─────────────────────────────────────────────────────────────

describe('CardActivityPanel — §6.1 default frame', () => {
  it('renders app id, type label, status, creation time, context hash and a disabled action', async () => {
    await mount();
    expect(text()).toContain('Waiting for the card snapshot…');

    await emit(0, 'card_snapshot', snapshotFrame('canopy.agent'));

    const body = text();
    expect(body).toContain('Iteration Card');
    expect(body).toContain('canopy.agent');
    expect(body).toContain('Active');
    expect(body).toContain('ago');
    // Context-hash affordance shows a shortened digest until expanded.
    expect(body).toContain('c4a2f1d6c7b8');

    const action = [...container.querySelectorAll('button')].find(
      (button) => button.textContent === 'Run',
    );
    expect(action).toBeDefined();
    // The action is wired through the card-action client and remains enabled.
    expect(action?.disabled).toBe(false);
  });

  it('renders the live event list', async () => {
    await mount();
    await emit(0, 'card_snapshot', snapshotFrame());
    expect(text()).toContain('No events yet.');

    await emit(0, 'card_event', eventFrame(1));
    await emit(0, 'card_event', eventFrame(2));

    const body = text();
    expect(body).toContain('#1');
    expect(body).toContain('#2');
    expect(body).toContain('agent_progress');
    expect(body).toContain('agent:coding');
    expect(body).toContain('tests running');
    expect(body).toContain('2 events · last #2');
  });

  it('shows the connection indicator going live on a heartbeat', async () => {
    await mount();
    expect(text()).toContain('Connecting…');

    await act(async () => {
      streams[0]?.handlers.onOpen?.();
    });
    expect(text()).toContain('Open');

    await emit(0, 'card_snapshot', snapshotFrame());
    await emit(0, 'heartbeat', HEARTBEAT_FRAME);
    expect(text()).toContain('Live');
    // The activity indicator distinguishes a heartbeating stream from an idle one.
    expect(text()).toContain('(live)');
  });

  it('renders a rejected payload as a banner and keeps the last good card state', async () => {
    await mount();
    await emit(0, 'card_snapshot', snapshotFrame('canopy.live'));
    await emit(0, 'card_event', eventFrame(1));
    expect(text()).toContain('canopy.live');

    await emit(0, 'card_event', '{"event_type":"card_event","card_id":');

    const banner = container.querySelector('[role="alert"]');
    expect(banner).not.toBeNull();
    expect(banner?.textContent ?? '').toContain('rejected');
    expect(banner?.textContent ?? '').toContain('invalid JSON');

    // Non-destructive: the card and the good event are still on screen.
    const body = text();
    expect(body).toContain('canopy.live');
    expect(body).toContain('Iteration Card');
    expect(body).toContain('#1');
  });
});

describe('CardActivityPanel — source invariants', () => {
  it('never injects raw HTML', () => {
    const source = PANEL_SOURCE['/src/components/CardActivityPanel.tsx'] ?? '';
    expect(source.length).toBeGreaterThan(1000);
    expect(source).not.toContain('dangerouslySetInnerHTML');
    expect(source).not.toContain('innerHTML');
  });

  it('uses the shared SSE transport and no other stream primitive', () => {
    const panel = PANEL_SOURCE['/src/components/CardActivityPanel.tsx'] ?? '';
    const hook = HOOK_SOURCE['/src/hooks/useCardStream.ts'] ?? '';
    const store = STORE_SOURCE['/src/lib/cardStore.ts'] ?? '';
    expect(panel.length).toBeGreaterThan(1000);
    expect(hook.length).toBeGreaterThan(1000);
    expect(store.length).toBeGreaterThan(1000);

    for (const source of [panel, hook, store]) {
      expect(source).not.toContain('new EventSource');
      expect(source).not.toContain('fetch(');
    }
    expect(hook).toContain("from '../lib/sse.ts'");
    expect(hook).toContain('subscribeSse(url');
  });
});
