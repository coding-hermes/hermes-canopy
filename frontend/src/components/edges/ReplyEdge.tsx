/**
 * Hermes Canopy — ReplyEdge
 *
 * Standard reply edge: solid gray line with arrow.
 * Subtle and unobtrusive — the default edge for conversation threads.
 */

import { BaseEdge, getSmoothStepPath, type EdgeProps } from '@xyflow/react';

export function ReplyEdge({
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
    borderRadius: 8,
  });

  return (
    <>
      <BaseEdge
        id={id}
        path={edgePath}
        style={{
          stroke: '#9ca3af',
          strokeWidth: 1.5,
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
