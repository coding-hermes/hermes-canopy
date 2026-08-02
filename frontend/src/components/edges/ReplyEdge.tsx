/**
 * Hermes Canopy — ReplyEdge
 *
 * The default conversation link: a glowing cyan bezier (UI-04). Geometry
 * and colour come from GlowConnector / lib/canvasGeometry.
 */

import type { EdgeProps } from '@xyflow/react';
import { GlowConnector } from './GlowConnector.tsx';

export function ReplyEdge(props: EdgeProps) {
  return <GlowConnector {...props} kind="reply" />;
}
