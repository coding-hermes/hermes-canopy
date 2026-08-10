/**
 * ProposalCard component tests (TM-02, spec §11.2 scenarios 1-12).
 *
 * Tests the card's rendering, state transitions, actions, keyboard,
 * validation, and stale-card reconciliation. Uses the same createRoot +
 * act harness as AppHeader.test.tsx.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { ProposalCard } from '../ProposalCard';
import type { ProposalCard as ProposalCardVM } from '../../stores/topicProposalStore';
import {
  confidenceBand,
  CONFIDENCE_BAND_LABELS,
} from '../../types/topic-detection';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

// ─── Mock the store + API ──────────────────────────────────────────────

const storeMocks = vi.hoisted(() => ({
  setCardStatus: vi.fn(),
  removeCard: vi.fn(),
}));

vi.mock('../../stores/topicProposalStore', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../stores/topicProposalStore')>();
  return {
    ...actual,
    setCardStatus: storeMocks.setCardStatus,
    removeCard: storeMocks.removeCard,
  };
});

vi.mock('../../lib/topicDetectionApi', () => ({
  confirmProposal: vi.fn(),
  dismissProposal: vi.fn(),
}));

vi.mock('../../lib/activeTree', () => ({
  notifyTopicsChanged: vi.fn(),
}));

import { confirmProposal, dismissProposal } from '../../lib/topicDetectionApi';

// ─── Fixtures ──────────────────────────────────────────────────────────

function makeProposal(
  overrides: Partial<ProposalCardVM['proposal']> = {},
): ProposalCardVM['proposal'] {
  return {
    proposalId: 'prop-001',
    treeId: 'tree-001',
    rootNodeId: 'node-001',
    title: 'Database schema',
    description: 'Conversation shifted to database design',
    detectionType: 'implicit',
    confidence: 0.82,
    subjectKey: 'database',
    status: 'pending',
    expiresAt: '2025-12-31T23:59:59Z',
    ...overrides,
  };
}

function makeCard(
  overrides: Partial<ProposalCardVM> = {},
): ProposalCardVM {
  return {
    proposal: makeProposal(),
    status: 'pending',
    ...overrides,
  };
}

// ─── Harness ───────────────────────────────────────────────────────────

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  vi.clearAllMocks();
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

function mount(card: ProposalCardVM, treeId = 'tree-001') {
  act(() => {
    root.render(createElement(ProposalCard, { card, treeId }));
  });
}

async function settle(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

function q(selector: string): HTMLElement | null {
  return container.querySelector(selector);
}

// ─── Tests ─────────────────────────────────────────────────────────────

describe('ProposalCard — spec §11.2', () => {
  // Scenario 1: Render topic_proposed
  it('1. renders inline proposal card at pending state', () => {
    mount(makeCard());
    const card = q('[data-testid="proposal-card"]');
    expect(card).not.toBeNull();
    expect(card?.getAttribute('data-status')).toBe('pending');
    expect(card?.textContent).toContain('Database schema');
    expect(card?.textContent).toContain('create?');
  });

  // Scenario 1: Shows signal type + confidence band
  it('displays detection type label and confidence band (not raw probability)', () => {
    mount(makeCard({ proposal: makeProposal({ confidence: 0.9 }) }));
    const card = q('[data-testid="proposal-card"]');
    expect(card?.textContent).toContain('Conversation shift');
    expect(card?.textContent).toContain(
      CONFIDENCE_BAND_LABELS[confidenceBand(0.9)],
    );
    // Raw probability is NOT shown
    expect(card?.textContent).not.toContain('0.9');
    expect(card?.textContent).not.toContain('90%');
  });

  // Scenario 2: Accept action
  it('2. accept calls confirmProposal and transitions to created', async () => {
    vi.mocked(confirmProposal).mockResolvedValue({
      topic: { id: 'topic-1', title: 'Database schema', slug: 'database-schema' },
    });
    mount(makeCard());

    act(() => {
      q('[data-testid="proposal-accept"]')?.click();
    });
    await settle();

    expect(confirmProposal).toHaveBeenCalledWith('prop-001');
    expect(storeMocks.setCardStatus).toHaveBeenCalledWith(
      'prop-001',
      'created',
      expect.objectContaining({
        createdTopic: { id: 'topic-1', title: 'Database schema', slug: 'database-schema' },
      }),
    );
  });

  // Scenario 3: Rename action
  it('3. rename shows inline input, validates, and submits override', async () => {
    vi.mocked(confirmProposal).mockResolvedValue({
      topic: { id: 'topic-2', title: 'Custom Name', slug: 'custom-name' },
    });
    mount(makeCard());

    act(() => {
      q('[data-testid="proposal-rename"]')?.click();
    });

    const input = q('[data-testid="proposal-rename-input"]') as HTMLInputElement;
    expect(input).not.toBeNull();
    expect(input.value).toBe('Database schema'); // pre-filled with proposal title

    // React 19: use native setter to trigger onChange properly
    const nativeInputValueSetter = Object.getOwnPropertyDescriptor(
      window.HTMLInputElement.prototype,
      'value',
    )!.set!;
    act(() => {
      nativeInputValueSetter.call(input, 'Custom Name');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });

    act(() => {
      q('[data-testid="proposal-rename-submit"]')?.click();
    });
    await settle();

    expect(confirmProposal).toHaveBeenCalledWith('prop-001', 'Custom Name');
  });

  // Scenario 11: Long title validation
  it('11. shows error at >200 chars and disables submit', () => {
    mount(makeCard());

    act(() => {
      q('[data-testid="proposal-rename"]')?.click();
    });

    const input = q('[data-testid="proposal-rename-input"]') as HTMLInputElement;
    const longTitle = 'a'.repeat(201);
    const nativeInputValueSetter = Object.getOwnPropertyDescriptor(
      window.HTMLInputElement.prototype,
      'value',
    )!.set!;
    act(() => {
      nativeInputValueSetter.call(input, longTitle);
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });

    const submitBtn = q('[data-testid="proposal-rename-submit"]') as HTMLButtonElement;
    expect(submitBtn.disabled).toBe(true);
    expect(container.textContent).toContain('200');
  });

  it('11b. rejects empty title', () => {
    mount(makeCard());

    act(() => {
      q('[data-testid="proposal-rename"]')?.click();
    });

    const input = q('[data-testid="proposal-rename-input"]') as HTMLInputElement;
    const nativeInputValueSetter = Object.getOwnPropertyDescriptor(
      window.HTMLInputElement.prototype,
      'value',
    )!.set!;
    act(() => {
      nativeInputValueSetter.call(input, '   ');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });

    const submitBtn = q('[data-testid="proposal-rename-submit"]') as HTMLButtonElement;
    expect(submitBtn.disabled).toBe(true);
  });

  // Scenario 4: Reject action
  it('4. reject calls dismissProposal and removes card', async () => {
    vi.mocked(dismissProposal).mockResolvedValue(undefined);
    mount(makeCard());

    act(() => {
      q('[data-testid="proposal-reject"]')?.click();
    });
    await settle();

    expect(dismissProposal).toHaveBeenCalledWith('prop-001');
    expect(storeMocks.removeCard).toHaveBeenCalledWith('prop-001');
  });

  // Scenario 5: Dismiss (same as reject server-side, hides without focus loss)
  it('5. escape key dismisses when card is focused', async () => {
    vi.mocked(dismissProposal).mockResolvedValue(undefined);
    mount(makeCard());

    const card = q('[data-testid="proposal-card"]') as HTMLElement;
    card.focus();
    expect(document.activeElement).toBe(card);

    act(() => {
      card.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }),
      );
    });
    await settle();

    expect(dismissProposal).toHaveBeenCalledWith('prop-001');
  });

  // Scenario 10: Keyboard controls
  it('10. Enter accepts when card is focused', async () => {
    vi.mocked(confirmProposal).mockResolvedValue({
      topic: { id: 't1', title: 'T', slug: 't' },
    });
    mount(makeCard());

    const card = q('[data-testid="proposal-card"]') as HTMLElement;
    card.focus();

    act(() => {
      card.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }),
      );
    });
    await settle();

    expect(confirmProposal).toHaveBeenCalledWith('prop-001');
  });

  it('10b. keyboard does not fire when rename input is open', () => {
    mount(makeCard());

    act(() => {
      q('[data-testid="proposal-rename"]')?.click();
    });

    const card = q('[data-testid="proposal-card"]') as HTMLElement;
    const before = vi.mocked(confirmProposal).mock.calls.length;

    act(() => {
      card.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }),
      );
    });

    expect(vi.mocked(confirmProposal).mock.calls.length).toBe(before);
  });

  // Scenario 6: Ignore timeout / expired reconciliation
  it('6. stale card is removed on TOPIC_PROPOSAL_EXPIRED error', async () => {
    vi.mocked(confirmProposal).mockRejectedValue(
      new Error('Topic proposal has expired'),
    );
    mount(makeCard());

    act(() => {
      q('[data-testid="proposal-accept"]')?.click();
    });
    await settle();

    expect(storeMocks.removeCard).toHaveBeenCalledWith('prop-001');
  });

  it('6b. stale card is removed on ALREADY_RESOLVED error', async () => {
    vi.mocked(dismissProposal).mockRejectedValue(
      new Error('Topic proposal is already resolved'),
    );
    mount(makeCard());

    act(() => {
      q('[data-testid="proposal-reject"]')?.click();
    });
    await settle();

    expect(storeMocks.removeCard).toHaveBeenCalledWith('prop-001');
  });

  // Scenario 12: Concurrent resolution
  it('12. stale card resolves to server state (not found = removed)', async () => {
    vi.mocked(confirmProposal).mockRejectedValue(
      new Error('Topic proposal not found'),
    );
    mount(makeCard());

    act(() => {
      q('[data-testid="proposal-accept"]')?.click();
    });
    await settle();

    expect(storeMocks.removeCard).toHaveBeenCalledWith('prop-001');
  });

  // Error state: non-stale error keeps the card
  it('non-stale error shows inline error and keeps card', async () => {
    vi.mocked(confirmProposal).mockRejectedValue(
      new Error('Topic proposal title must be 1-200 characters'),
    );
    mount(makeCard());

    act(() => {
      q('[data-testid="proposal-accept"]')?.click();
    });
    await settle();

    expect(storeMocks.setCardStatus).toHaveBeenCalledWith(
      'prop-001',
      'error',
      expect.objectContaining({
        error: 'Topic proposal title must be 1-200 characters',
      }),
    );
    // Card is NOT removed on non-stale error
    expect(storeMocks.removeCard).not.toHaveBeenCalled();
  });

  // Created state renders confirmation
  it('created state shows "Topic created" and topic link', () => {
    mount(
      makeCard({
        status: 'created',
        createdTopic: { id: 'topic-99', title: 'Schema', slug: 'schema' },
      }),
    );
    const created = q('[data-testid="proposal-card-created"]');
    expect(created).not.toBeNull();
    expect(created?.textContent).toContain('Topic created');
    expect(created?.textContent).toContain('#schema');
    expect(created?.querySelector('a')?.getAttribute('href')).toContain(
      'topic=topic-99',
    );
  });

  // Confirming state disables buttons
  it('confirming state disables all action buttons', () => {
    mount(makeCard({ status: 'confirming' }));
    const accept = q('[data-testid="proposal-accept"]') as HTMLButtonElement;
    const rename = q('[data-testid="proposal-rename"]') as HTMLButtonElement;
    const reject = q('[data-testid="proposal-reject"]') as HTMLButtonElement;
    expect(accept.disabled).toBe(true);
    expect(rename.disabled).toBe(true);
    expect(reject.disabled).toBe(true);
  });

  // Error state renders inline error
  it('error state renders inline error message', () => {
    mount(
      makeCard({
        status: 'error',
        error: 'Something went wrong',
      }),
    );
    const err = q('[data-testid="proposal-card-error"]');
    expect(err).not.toBeNull();
    expect(err?.textContent).toContain('Something went wrong');
  });

  // Rename cancel restores original state
  it('rename cancel hides the input', () => {
    mount(makeCard());

    act(() => {
      q('[data-testid="proposal-rename"]')?.click();
    });
    expect(q('[data-testid="proposal-rename-input"]')).not.toBeNull();

    act(() => {
      q('[data-testid="proposal-rename-cancel"]')?.click();
    });
    expect(q('[data-testid="proposal-rename-input"]')).toBeNull();
    // Actions are back
    expect(q('[data-testid="proposal-accept"]')).not.toBeNull();
  });

  // Rename Enter key submits
  it('rename input Enter key submits the override', async () => {
    vi.mocked(confirmProposal).mockResolvedValue({
      topic: { id: 't1', title: 'T', slug: 't' },
    });
    mount(makeCard());

    act(() => {
      q('[data-testid="proposal-rename"]')?.click();
    });

    const input = q('[data-testid="proposal-rename-input"]') as HTMLInputElement;
    const nativeInputValueSetter = Object.getOwnPropertyDescriptor(
      window.HTMLInputElement.prototype,
      'value',
    )!.set!;
    act(() => {
      nativeInputValueSetter.call(input, 'New Title');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });

    act(() => {
      input.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }),
      );
    });
    await settle();

    expect(confirmProposal).toHaveBeenCalledWith('prop-001', 'New Title');
  });

  // Rename Escape cancels
  it('rename input Escape cancels editing', () => {
    mount(makeCard());

    act(() => {
      q('[data-testid="proposal-rename"]')?.click();
    });

    const input = q('[data-testid="proposal-rename-input"]') as HTMLInputElement;
    act(() => {
      input.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }),
      );
    });

    expect(q('[data-testid="proposal-rename-input"]')).toBeNull();
  });

  // Card never steals focus on mount (spec §6)
  it('does not steal focus on mount', () => {
    const previouslyFocused = document.createElement('input');
    document.body.appendChild(previouslyFocused);
    previouslyFocused.focus();

    mount(makeCard());

    // The card should not have stolen focus
    expect(document.activeElement).toBe(previouslyFocused);
    document.body.removeChild(previouslyFocused);
  });

  // Renders for all detection types
  it('renders explicit detection type label', () => {
    mount(makeCard({ proposal: makeProposal({ detectionType: 'explicit', confidence: 1.0 }) }));
    expect(container.textContent).toContain('Direct request');
    expect(container.textContent).toContain('Confident');
  });

  it('renders structural detection type label', () => {
    mount(makeCard({ proposal: makeProposal({ detectionType: 'structural', confidence: 0.9 }) }));
    expect(container.textContent).toContain('Branch boundary');
  });
});

// ─── Store idempotency (scenario 8: SSE reconnect dedupe) ──────────────

describe('topicProposalStore — SSE reconnect dedupe (§11.2 scenario 8)', () => {
  beforeEach(() => {
    vi.resetModules();
  });

  it('adding the same proposalId twice does not duplicate', async () => {
    const store = await import('../../stores/topicProposalStore');
    const event = {
      proposalId: 'prop-dedup',
      treeId: 'tree-1',
      rootNodeId: 'node-1',
      title: 'Test',
      description: 'Desc',
      detectionType: 'implicit' as const,
      confidence: 0.8,
      subjectKey: 'test',
      status: 'pending',
      expiresAt: '',
    };

    // Clear any prior state
    store.clearAll();

    let callCount = 0;
    const unsub = store.subscribe(() => {
      callCount++;
    });

    store.addProposal(event);
    const cardsAfterFirst = store.getCards().length;

    store.addProposal(event); // replay on reconnect — should be deduped
    const cardsAfterSecond = store.getCards().length;

    unsub();

    expect(cardsAfterFirst).toBe(1);
    expect(cardsAfterSecond).toBe(1); // no duplicate
  });
});
