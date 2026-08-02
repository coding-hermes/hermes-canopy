/**
 * Hermes Canopy — GhostNode (UI-04, Phase 11 Mockup Parity)
 *
 * The dashed placeholder slots at the frontier of the tree
 * (docs/mockups/mockup-1.png, right edge). Each one hangs off a leaf and
 * says "a reply could go here" — activating it opens the composer for
 * that parent rather than silently writing an empty node.
 *
 * Rendered as a real React Flow node so it participates in layout,
 * panning and zooming like everything else, but it carries no graph data
 * and is never persisted.
 */

import { memo } from 'react';
import { Handle, Position, type NodeProps, type Node } from '@xyflow/react';
import { Plus } from 'lucide-react';
import type { GhostNodeData } from '../../types/tree.ts';
import { palette, alpha } from '../../theme.ts';

type GhostNodeType = Node<GhostNodeData, 'ghostNode'>;

function GhostNodeComponent({ data }: NodeProps<GhostNodeType>) {
  const d = data as unknown as GhostNodeData;
  const interactive = typeof d.onCreate === 'function';
  const label = d.label ?? (interactive ? 'Add reply' : 'No replies yet');

  /*
   * Both states clear WCAG AA on the ghost fill (accent @0.04 over
   * surface-base). The inert label was measured at 3.40:1 at 0.5 alpha —
   * a real failure, not a stylistic dimming — so it sits at 0.7 (5.51:1)
   * while the interactive one keeps 0.75 (6.16:1). Hierarchy is carried
   * by the border and the icon, not by illegible text.
   */
  const textAlpha = interactive ? 0.75 : 0.7;
  const borderAlpha = interactive ? 0.34 : 0.2;

  return (
    <button
      type="button"
      className="nodrag group flex h-[52px] w-[150px] items-center justify-center gap-1.5 rounded-lg text-[11px] font-medium transition-colors duration-150"
      style={{
        border: `1.5px dashed ${alpha(palette.accent, borderAlpha)}`,
        backgroundColor: alpha(palette.accent, 0.04),
        color: alpha(palette.accent, textAlpha),
        cursor: interactive ? 'pointer' : 'default',
      }}
      disabled={!interactive}
      onClick={(e) => {
        e.stopPropagation();
        d.onCreate?.(d.parentId);
      }}
      onMouseEnter={(e) => {
        if (!interactive) return;
        e.currentTarget.style.backgroundColor = alpha(palette.accent, 0.1);
        e.currentTarget.style.borderColor = alpha(palette.accent, 0.6);
      }}
      onMouseLeave={(e) => {
        if (!interactive) return;
        e.currentTarget.style.backgroundColor = alpha(palette.accent, 0.04);
        e.currentTarget.style.borderColor = alpha(palette.accent, borderAlpha);
      }}
      aria-label={label}
      title={label}
      data-testid="ghost-node"
      data-interactive={interactive ? 'true' : 'false'}
    >
      <Handle
        type="target"
        position={Position.Left}
        className="!h-1.5 !w-1.5 !border-0"
        style={{ backgroundColor: alpha(palette.accent, 0.5) }}
        isConnectable={false}
        aria-hidden="true"
      />
      {interactive && <Plus className="h-3 w-3" aria-hidden="true" />}
      {label}
    </button>
  );
}

export const GhostNode = memo(GhostNodeComponent);
