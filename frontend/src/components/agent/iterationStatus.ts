/**
 * Hermes Canopy — iteration status vocabulary.
 *
 * Non-component helpers shared by the five §8.2 subtype renderers: subtype
 * labels and the human-readable status label / Tailwind tone for a card state.
 * They live in a .ts module rather than beside the badge components because a
 * .tsx file that also exports plain functions breaks React Fast Refresh
 * (only-export-components); nothing here interprets server strings as HTML.
 */

import type { IterationSubtype } from '../../types/agent.ts';

export const ITERATION_LABELS: Record<IterationSubtype, string> = {
  iteration_search: 'Search',
  iteration_code_exec: 'Code Exec',
  iteration_file_read: 'File Read',
  iteration_thinking: 'Thinking',
  iteration_tool_call: 'Tool Call',
};

/** Human-readable label for a card state or subtype status value. */
export function statusLabel(value: string): string {
  switch (value) {
    case 'running': return 'Running';
    case 'active': return 'Active';
    case 'waiting_for_user': return 'Waiting';
    case 'pending_approval': return 'Pending approval';
    case 'completed': return 'Done';
    case 'retrieved': return 'Retrieved';
    case 'approved': return 'Approved';
    case 'failed': return 'Failed';
    case 'error': return 'Error';
    case 'cancelled': return 'Cancelled';
    case 'denied': return 'Denied';
    case 'rejected': return 'Rejected';
    case 'interrupted': return 'Interrupted';
    case 'queued': return 'Queued';
    case 'searching': return 'Searching';
    case 'pending': return 'Pending';
    default: return value.replace(/_/g, ' ');
  }
}

/** Tailwind tone classes for a card state or subtype status value. */
export function statusTone(value: string): string {
  switch (value) {
    case 'running':
    case 'active':
      return 'bg-purple-500/20 text-purple-300';
    case 'waiting_for_user':
    case 'pending_approval':
    case 'queued':
    case 'searching':
    case 'pending':
      return 'bg-amber-500/20 text-amber-300';
    case 'completed':
    case 'retrieved':
    case 'approved':
      return 'bg-green-500/20 text-green-300';
    case 'failed':
    case 'error':
      return 'bg-red-500/20 text-red-300';
    case 'cancelled':
    case 'denied':
    case 'rejected':
      return 'bg-gray-500/20 text-gray-400';
    case 'interrupted':
      return 'bg-orange-500/20 text-orange-300';
    default:
      return 'bg-gray-500/20 text-gray-400';
  }
}
