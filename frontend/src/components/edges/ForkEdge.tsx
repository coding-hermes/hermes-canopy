/**
 * Hermes Canopy — ForkEdge
 *
 * Branch/fork edge: purple, slightly thicker.
 * Indicates a new branch created from a conversation node.
 */

import { BaseEdge, getSmoothStepPath, type EdgeProps } from '@xyflow/react';

export function ForkEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  markerEnd,
}: EdgeProps) {
  const [edgePath] = getSmoothStepPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
    borderRadius: 10,
  });

  return (
    <>
      <BaseEdge
        id={id}
        path={edgePath}
        style={{
          stroke: '#8b5cf6',
          strokeWidth: 2,
        }}
        markerEnd={markerEnd}
      />
      {/* Invisible wider path for keyboard/screen reader access */}
      <path
        d={edgePath}
        fill="none"
        stroke="transparent"
        strokeWidth={20}
        role="presentation"
        aria-hidden="true"
      />
    </>
  );
}
