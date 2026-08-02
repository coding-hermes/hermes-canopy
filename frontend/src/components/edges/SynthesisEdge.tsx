/**
 * Hermes Canopy — SynthesisEdge
 *
 * Synthesis/merge link: glowing amber dashed bezier (UI-04). Connects
 * parent branches into a synthesis node — dashed so multi-source merging
 * stays visually distinct from a plain reply.
 */

import type { EdgeProps } from '@xyflow/react';
import { GlowConnector } from './GlowConnector.tsx';

export function SynthesisEdge(props: EdgeProps) {
  return <GlowConnector {...props} kind="synthesis" />;
}
