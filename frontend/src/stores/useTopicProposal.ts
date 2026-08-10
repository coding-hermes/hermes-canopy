/**
 * Hermes Canopy — React hooks for the topic proposal store (TM-02)
 *
 * Bridges the imperative store (topicProposalStore.ts) to React via
 * useSyncExternalStore. Components subscribe and re-render on any store
 * change, selecting only the slice they need.
 */

import { useSyncExternalStore, useCallback } from 'react';
import {
  subscribe,
  getVersion,
  getCardsForNode,
  getCards,
  type ProposalCard,
} from './topicProposalStore';

/**
 * Subscribe to all proposal cards (e.g., for a global listener).
 * Re-renders when the store version changes.
 */
export function useAllProposalCards(): ProposalCard[] {
  return useSyncExternalStore(
    subscribe,
    useCallback(() => {
      getVersion(); // touch version to register dependency
      return getCards();
    }, []),
  );
}

/**
 * Subscribe to proposal cards attached to a specific node.
 * The card appears under the node whose id === rootNodeId (spec §6).
 */
export function useProposalCardsForNode(rootNodeId: string): ProposalCard[] {
  return useSyncExternalStore(
    subscribe,
    useCallback(() => {
      getVersion();
      return getCardsForNode(rootNodeId);
    }, [rootNodeId]),
  );
}
