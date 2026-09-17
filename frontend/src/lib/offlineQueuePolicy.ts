/**
 * Hermes Canopy — Offline-queue replay policy (DF-HERMES-CANOPY-16)
 *
 * The service worker (`frontend/sw.ts`) queues mutating requests while the
 * device is offline and replays them later. Each stored entry carries the
 * `Authorization` header captured at QUEUE time, so once the bearer token has
 * been rotated every later replay of that entry answers 401 (or 403). The
 * replay loop used to delete an entry only on a 2xx answer and keep it on
 * everything else, with no attempt counter and no non-ok deletion path: a
 * stale-token entry was therefore immortal. The queue grew without bound — a
 * slow IndexedDB leak — and the user-visible symptom was "nothing ever syncs".
 *
 * Policy
 * ------
 *   'ok'            → delete the entry; the attempt count is irrelevant.
 *   'network_error' → keep the entry and DO NOT consume the attempt budget.
 *   'auth'          → count the attempt; drop the entry after
 *                     MAX_AUTH_ATTEMPTS with one warning that names the auth
 *                     failure and the attempt count. A rejected token can
 *                     never heal itself by retrying, so retrying forever is
 *                     pure leak.
 *   'http_error'    → count the attempt; drop the entry after MAX_ATTEMPTS.
 *
 * WHY a network error must not consume the budget: the replay timer fires
 * every 60 s whether or not the device is online, and a thrown request is
 * indistinguishable from "nobody is here to receive it". If an offline device
 * burned the budget, it would exhaust all attempts within minutes while the
 * request never once reached the server, and the entry would be discarded at
 * exactly the moment it became deliverable again. Only an answer that came
 * BACK from the server is evidence about the request, so only a server answer
 * counts as an attempt.
 *
 * This module is deliberately pure — no HTTP call, no IndexedDB, no DOM or
 * service-worker globals — so it is unit-testable under jsdom. The
 * IndexedDB/network half of the loop stays in `frontend/sw.ts`.
 */

/** Attempts a rejected bearer token gets before the entry is dropped. */
export const MAX_AUTH_ATTEMPTS = 3;

/** Attempts any other non-2xx answer gets before the entry is dropped. */
export const MAX_ATTEMPTS = 5;

/** How a single replay attempt ended. */
export type ReplayOutcome = 'ok' | 'auth' | 'http_error' | 'network_error';

/**
 * Classify an attempt result.
 *
 * `null` means the request threw (no response at all) — i.e. still offline.
 */
export function classifyReplayResult(status: number | null): ReplayOutcome {
  if (status === null) return 'network_error';
  if (status >= 200 && status < 300) return 'ok';
  if (status === 401 || status === 403) return 'auth';
  return 'http_error';
}

/** What the caller should do with the entry after this attempt. */
export type ReplayDecision = {
  /** 'delete' removes the entry from the queue; 'keep' re-writes it. */
  action: 'delete' | 'keep';
  /**
   * The attempt count to PERSIST on the entry. Always a number so the counter
   * survives across replays (an absent field reads back as 0).
   */
  attempts: number;
  /** Non-null only when an entry is dropped after exhausting its budget. */
  warn: string | null;
};

/**
 * Decide the fate of a queued entry from its persisted attempt count and the
 * outcome of the attempt just made. `attempts` is the value returned by the
 * previous call (undefined for an entry queued before this policy existed).
 */
export function nextReplayDecision(
  attempts: number | undefined,
  outcome: ReplayOutcome,
): ReplayDecision {
  const prior = attempts ?? 0;

  switch (outcome) {
    case 'ok':
      // Delivered — the entry's work is done, whatever the counter said.
      return { action: 'delete', attempts: prior, warn: null };

    case 'network_error':
      // Still offline: the server never saw the request, so no attempt is
      // consumed and the entry stays queued indefinitely.
      return { action: 'keep', attempts: prior, warn: null };

    case 'auth': {
      const next = prior + 1;
      if (next >= MAX_AUTH_ATTEMPTS) {
        return {
          action: 'delete',
          attempts: next,
          warn:
            `[canopy-sw] dropping queued request after ${next} attempts: ` +
            'the stored bearer token was rejected (auth failure). ' +
            'Re-sign-in before the next offline session.',
        };
      }
      return { action: 'keep', attempts: next, warn: null };
    }

    case 'http_error': {
      const next = prior + 1;
      if (next >= MAX_ATTEMPTS) {
        return {
          action: 'delete',
          attempts: next,
          warn:
            `[canopy-sw] dropping queued request after ${next} attempts: ` +
            `the server kept answering with a non-2xx status (MAX_ATTEMPTS=${MAX_ATTEMPTS}).`,
        };
      }
      return { action: 'keep', attempts: next, warn: null };
    }
  }
}
