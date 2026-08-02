/**
 * Hermes Canopy — GlowConnector (UI-04, Phase 11 Mockup Parity)
 *
 * The shared body of every connector on the branching canvas. The mockup
 * (docs/mockups/mockup-1.png) draws each link as a soft left→right bezier
 * that *glows*: a wide, low-opacity stroke of the accent colour sits
 * underneath a crisp core stroke, and a small joint dot marks the point
 * where a parent fans out to its children.
 *
 * Because every sibling edge leaves the same source handle, their joint
 * dots land on the same coordinate and read as the single fan-out dot in
 * the mockup — no separate "branch point" bookkeeping required.
 *
 * ReplyEdge / ForkEdge / SynthesisEdge are thin wrappers that pick a
 * `kind`; all geometry and colour lives in lib/canvasGeometry so it can be
 * unit-tested without a renderer.
 */

import { memo } from 'react';
import type { EdgeProps } from '@xyflow/react';
import {
  connectorPath,
  connectorStyle,
  jointDotPosition,
  type ConnectorKind,
} from '../../lib/canvasGeometry';

// ─── Props ─────────────────────────────────────────────────────────────

export interface GlowConnectorProps extends EdgeProps {
  /** Which visual language this connector speaks. */
  kind: ConnectorKind;
}

/** Edge `data` the canvas attaches for state-dependent styling. */
interface ConnectorData {
  /** True when this edge feeds a branch the user has collapsed. */
  dimmed?: boolean;
  /** Suppress the fan-out dot (single-child links don't fan out). */
  hideJoint?: boolean;
}

// ─── Component ─────────────────────────────────────────────────────────

function GlowConnectorComponent({
  id,
  kind,
  sourceX,
  sourceY,
  targetX,
  targetY,
  selected,
  data,
}: GlowConnectorProps) {
  const points = { sourceX, sourceY, targetX, targetY };
  const { path } = connectorPath(points);

  const edgeData = (data ?? {}) as ConnectorData;
  const style = connectorStyle(kind, {
    selected: selected === true,
    dimmed: edgeData.dimmed === true,
  });

  const joint = jointDotPosition(points);
  const showJoint = edgeData.hideJoint !== true;

  return (
    <g className="canopy-connector" data-kind={kind}>
      {/* Glow halo — wide, soft, painted first so the core sits on top */}
      <path
        id={`${id}-glow`}
        d={path}
        fill="none"
        stroke={style.glow}
        strokeWidth={style.glowWidth}
        strokeLinecap="round"
        pointerEvents="none"
        aria-hidden="true"
      />

      {/* Core stroke */}
      <path
        id={id}
        className="react-flow__edge-path"
        d={path}
        fill="none"
        stroke={style.stroke}
        strokeWidth={style.strokeWidth}
        strokeLinecap="round"
        {...(style.dash ? { strokeDasharray: style.dash } : {})}
        pointerEvents="none"
      />

      {/* Joint dot at the fan-out point */}
      {showJoint && (
        <circle
          cx={joint.x}
          cy={joint.y}
          r={selected ? 3.6 : 3}
          fill={style.dot}
          pointerEvents="none"
          aria-hidden="true"
        />
      )}

      {/* Invisible fat path — hit target for pointer + a11y tooling */}
      <path
        d={path}
        fill="none"
        stroke="transparent"
        strokeWidth={20}
        role="presentation"
        aria-hidden="true"
      />
    </g>
  );
}

export const GlowConnector = memo(GlowConnectorComponent);
