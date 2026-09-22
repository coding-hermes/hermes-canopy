/** Card action transport for SPEC-PL-03 §6.1. */

import { apiPost } from './api.ts';

export type CardActionPayload = Record<string, unknown>;

/** Invoke one handler declared by a card and preserve API failures for the UI. */
export function invokeCardAction(
  cardId: string,
  handler: string,
  payload: CardActionPayload,
): Promise<unknown> {
  return apiPost(`/cards/${encodeURIComponent(cardId)}/actions`, { handler, payload });
}
