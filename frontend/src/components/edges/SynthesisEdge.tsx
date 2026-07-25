/**
 * Hermes Canopy — SynthesisEdge
 *
 * Synthesis/merge edge: amber/gold dashed line, animated.
 * Connects parent branches to a synthesis node — visually distinct
 * to show multi-source merging.
 */

import { BaseEdge, getSmoothStepPath, type EdgeProps } from '@xyflow/react';

export function SynthesisEdge({
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
    borderRadius: 12,
  });

  return (
    <>
      <BaseEdge
        id={id}
        path={edgePath}
        style={{
          stroke: '#f59e0b',
          strokeWidth: 2.5,
          strokeDasharray: '8,4',
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
