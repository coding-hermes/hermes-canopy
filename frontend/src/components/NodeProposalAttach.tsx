/**
 * Hermes Canopy — Node Proposal Attach Point (TM-02)
 *
 * Renders pending proposal cards under a node whose id matches a
 * proposal's rootNodeId (spec §6: "inline, non-blocking card attached
 * to the triggering node").
 *
 * Used inside NodeCard and the React Flow node components so proposals
 * appear in both the thread view and the canvas.
 */

import { useProposalCardsForNode } from '../stores/useTopicProposal';
import { ProposalCard } from './ProposalCard';

export interface NodeProposalAttachProps {
  nodeId: string;
  treeId: string;
}

/**
 * Renders all pending/confirming/error proposal cards for a node.
 * Cards in 'created' or 'rejected' states are handled (created shows
 * briefly then auto-removes; rejected is already removed from the store).
 */
export function NodeProposalAttach({ nodeId, treeId }: NodeProposalAttachProps) {
  const cards = useProposalCardsForNode(nodeId);

  if (cards.length === 0) return null;

  return (
    <div className="space-y-2" data-testid="node-proposal-attach">
      {cards.map((card) => (
        <ProposalCard key={card.proposal.proposalId} card={card} treeId={treeId} />
      ))}
    </div>
  );
}

export default NodeProposalAttach;
