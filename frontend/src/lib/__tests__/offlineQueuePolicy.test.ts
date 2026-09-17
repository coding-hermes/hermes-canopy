/**
 * Unit tests — offline-queue replay policy (DF-HERMES-CANOPY-16)
 *
 * The defect: the service worker kept a queued entry on every non-2xx answer
 * and on every thrown request, with no attempt counter, so an entry whose
 * Authorization header was captured before a token rotation replayed 401
 * forever and never left IndexedDB.
 *
 * SCOPE OF THESE TESTS: `src/lib/offlineQueuePolicy.ts` is pure, so what is
 * pinned here is the DECISION TABLE and the counter arithmetic. jsdom has no
 * IndexedDB and no service-worker globals, so the loop in `frontend/sw.ts`
 * (read entry → replay → delete/put back) is NOT covered here; the harness
 * below models the persistence contract by feeding each decision's `attempts`
 * back into the next call, exactly as `sw.ts` writes it back with `put`.
 */

import { describe, it, expect } from 'vitest';
import {
  MAX_ATTEMPTS,
  MAX_AUTH_ATTEMPTS,
  classifyReplayResult,
  nextReplayDecision,
} from '../offlineQueuePolicy';
import type { ReplayDecision } from '../offlineQueuePolicy';

/**
 * Replay one entry until it is deleted (or `maxSteps` is reached), persisting
 * the returned attempt count between replays exactly as the service worker
 * does. `stored` starts undefined to model a legacy entry queued before the
 * `attempts` field existed.
 */
function replayUntilGone(
  status: number | null,
  maxSteps = 20,
): { steps: ReplayDecision[]; warns: string[]; persisted: Array<number | undefined> } {
  let stored: number | undefined;
  const steps: ReplayDecision[] = [];
  const warns: string[] = [];
  const persisted: Array<number | undefined> = [];

  for (let i = 0; i < maxSteps; i++) {
    const decision = nextReplayDecision(stored, classifyReplayResult(status));
    steps.push(decision);
    if (decision.warn !== null) warns.push(decision.warn);
    if (decision.action === 'delete') break;
    stored = decision.attempts;
    persisted.push(stored);
  }

  return { steps, warns, persisted };
}

describe('classifyReplayResult', () => {
  it('maps 2xx to ok', () => {
    expect(classifyReplayResult(200)).toBe('ok');
    expect(classifyReplayResult(204)).toBe('ok');
  });

  it('maps 401 and 403 to auth', () => {
    expect(classifyReplayResult(401)).toBe('auth');
    expect(classifyReplayResult(403)).toBe('auth');
  });

  it('maps any other status to http_error', () => {
    expect(classifyReplayResult(404)).toBe('http_error');
    expect(classifyReplayResult(500)).toBe('http_error');
  });

  it('maps a thrown request (null) to network_error', () => {
    expect(classifyReplayResult(null)).toBe('network_error');
  });
});

describe('nextReplayDecision — attempt budget', () => {
  it('deletes a 200 with no warning', () => {
    const decision = nextReplayDecision(2, classifyReplayResult(200));
    expect(decision).toEqual({ action: 'delete', attempts: 2, warn: null });
  });

  it('bounds a stale-token entry at MAX_AUTH_ATTEMPTS and warns exactly once', () => {
    const { steps, warns, persisted } = replayUntilGone(401);

    // Kept while the budget lasts, with the counter written back each time.
    expect(persisted).toEqual([1, 2]);
    expect(steps[0]).toEqual({ action: 'keep', attempts: 1, warn: null });
    expect(steps[1]).toEqual({ action: 'keep', attempts: 2, warn: null });

    // Third attempt: dropped, because a rejected token can never heal itself.
    expect(steps).toHaveLength(MAX_AUTH_ATTEMPTS);
    expect(steps[2].action).toBe('delete');
    expect(steps[2].attempts).toBe(MAX_AUTH_ATTEMPTS);

    // Exactly one warning, naming the auth failure and the attempt count.
    expect(warns).toHaveLength(1);
    expect(warns[0]).toBe(steps[2].warn);
    expect(warns[0]).toMatch(/auth failure/i);
    expect(warns[0]).toContain(String(MAX_AUTH_ATTEMPTS));
  });

  it('treats 403 the same as 401', () => {
    const { steps } = replayUntilGone(403);
    expect(steps).toHaveLength(MAX_AUTH_ATTEMPTS);
    expect(steps[steps.length - 1].action).toBe('delete');
  });

  it('never deletes and never consumes the budget on a network error', () => {
    const { steps, warns, persisted } = replayUntilGone(null, 10);

    // 10 offline replays: still queued, budget untouched, nothing warned.
    expect(steps).toHaveLength(10);
    expect(steps.every((s) => s.action === 'keep')).toBe(true);
    expect(steps.every((s) => s.attempts === 0)).toBe(true);
    // Whatever the worker persists back, it stays at 0 — the counter never
    // advances, so the entry is never dropped by the offline loop.
    expect(persisted).toEqual(Array(10).fill(0));
    expect(warns).toEqual([]);
  });

  it('does not burn the budget when an offline stretch precedes a 401', () => {
    // 5 network errors, then auth failures — the budget starts at 0, not 5.
    let attempts: number | undefined;
    for (let i = 0; i < 5; i++) {
      const offline = nextReplayDecision(attempts, classifyReplayResult(null));
      expect(offline.action).toBe('keep');
      attempts = offline.attempts;
    }
    expect(attempts).toBe(0);

    const firstAuth = nextReplayDecision(attempts, classifyReplayResult(401));
    expect(firstAuth).toEqual({ action: 'keep', attempts: 1, warn: null });
  });

  it('bounds any other non-2xx answer at MAX_ATTEMPTS', () => {
    const { steps, warns, persisted } = replayUntilGone(500);

    expect(steps).toHaveLength(MAX_ATTEMPTS);
    expect(persisted).toEqual([1, 2, 3, 4]);
    expect(steps[steps.length - 1].action).toBe('delete');
    expect(steps[steps.length - 1].attempts).toBe(MAX_ATTEMPTS);
    expect(warns).toHaveLength(1);
    expect(warns[0]).toContain(String(MAX_ATTEMPTS));
  });

  it('treats an undefined (legacy) attempts value as 0', () => {
    expect(nextReplayDecision(undefined, 'http_error')).toEqual(
      nextReplayDecision(0, 'http_error'),
    );
    expect(nextReplayDecision(undefined, 'auth')).toEqual(
      nextReplayDecision(0, 'auth'),
    );
    expect(nextReplayDecision(undefined, 'network_error')).toEqual({
      action: 'keep',
      attempts: 0,
      warn: null,
    });
    expect(nextReplayDecision(undefined, 'ok')).toEqual({
      action: 'delete',
      attempts: 0,
      warn: null,
    });
  });

  it('persists the attempt count across replays (same entry, sequential replays)', () => {
    let stored: number | undefined;
    const kept: number[] = [];
    for (let i = 0; i < MAX_ATTEMPTS - 1; i++) {
      const decision = nextReplayDecision(stored, classifyReplayResult(404));
      expect(decision.action).toBe('keep');
      stored = decision.attempts;
      kept.push(stored);
    }
    expect(kept).toEqual([1, 2, 3, 4]);
    // The 5th failure uses the persisted counter, not a fresh one.
    const last = nextReplayDecision(stored, classifyReplayResult(404));
    expect(last.action).toBe('delete');
    expect(last.attempts).toBe(MAX_ATTEMPTS);
  });
});
