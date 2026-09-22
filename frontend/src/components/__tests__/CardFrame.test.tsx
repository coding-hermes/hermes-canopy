import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import CardFrame from '../CardFrame.tsx';
import { cardFromSummaryWire, type Card, type CardEvent } from '../../types/card.ts';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const CONTEXT_HASH = 'c4a2f1d6c7b8e9f00112233445566778899aabbccddeeff0011223344556677';

function fixtureCard(data: Record<string, unknown> = {}): Card {
  const parsed = cardFromSummaryWire({
    id: '0191a9c3-0000-7000-8000-000000000000',
    tree_id: '0191a9c3-0000-7000-8000-0000000000aa',
    node_id: '0191a9c3-0000-7000-8000-0000000000bb',
    app_id: 'canopy',
    type: 'iteration',
    status: 'active',
    context_hash: CONTEXT_HASH,
    data: { title: 'Implement card sync', ...data },
    actions: [{ label: 'Run', handler: 'run' }],
    last_event_seq: 7,
    created_at: '2026-07-22T12:00:00Z',
  });
  if (!parsed.ok) throw new Error(parsed.issues.join('; '));
  return parsed.value;
}

const event: CardEvent = {
  sequence: 1,
  eventId: '0191a9c3-0000-7000-8000-000000000001',
  cardId: '0191a9c3-0000-7000-8000-000000000000',
  eventType: 'agent_progress',
  actorKind: 'agent',
  actorId: 'coding',
  payload: { message: 'working' },
  createdAt: '2026-07-22T12:00:00Z',
};

let container: HTMLDivElement;
let root: Root;

async function mount(
  card: Card,
  invokeAction: (handler: string, payload: Record<string, unknown>) => Promise<unknown>,
): Promise<void> {
  await act(async () => {
    root.render(createElement(CardFrame, {
      card,
      events: [event],
      isLive: true,
      invokeAction,
    }));
  });
}

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
});

describe('CardFrame — SPEC-PL-03 §6.1', () => {
  it('renders every mandatory frame element from a validated Card', async () => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    await mount(fixtureCard(), vi.fn().mockResolvedValue({ accepted: true }));

    const body = container.textContent ?? '';
    expect(body).toContain('canopy');
    expect(body).toContain('Iteration Card');
    expect(body).toContain('Active');
    expect(body).toContain('created');
    expect(body).toContain('c4a2f1d6c7b8');
    expect(body).toContain('1 event');
    expect(body).toContain('(live)');
    expect(body).toContain('Run');
    expect(container.querySelector('button[title="' + CONTEXT_HASH + '"]')).not.toBeNull();
  });

  it('invokes a declared action and shows a text-only success outcome', async () => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    const invokeAction = vi.fn().mockResolvedValue({ accepted: true });
    await mount(fixtureCard(), invokeAction);

    const button = [...container.querySelectorAll('button')].find((item) => item.textContent === 'Run');
    expect(button).toBeDefined();
    await act(async () => button?.click());

    expect(invokeAction).toHaveBeenCalledWith('run', {});
    expect(container.textContent).toContain('Action completed');
  });

  it('shows an action failure inline without dumping a result object', async () => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    const invokeAction = vi.fn().mockRejectedValue(new Error('handler is not declared'));
    await mount(fixtureCard(), invokeAction);

    const button = [...container.querySelectorAll('button')].find((item) => item.textContent === 'Run');
    await act(async () => button?.click());

    expect(container.querySelector('[role="alert"]')?.textContent).toContain('handler is not declared');
  });

  it('renders data containing markup as text rather than HTML', async () => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    await mount(fixtureCard({ summary: '<b>html</b>' }), vi.fn().mockResolvedValue(undefined));

    expect(container.textContent).toContain('<b>html</b>');
    expect(container.querySelector('b')).toBeNull();
  });
});
