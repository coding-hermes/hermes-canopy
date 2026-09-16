/**
 * Hermes Canopy — ReferenceEdge (SPEC-PL-06 §7.2)
 *
 * The convergence edge: a source message → a multi-reference reply. It is
 * the only connector whose colour is not a design token — §7.2 maps the
 * server-computed `color_key` (§5.2) to a fixed palette — and the only one
 * that carries an arrow at the target plus a small `R#` midpoint label.
 *
 * Geometry and colour stay in GlowConnector / lib/canvasGeometry: this
 * wrapper only picks the `kind`, matching ForkEdge / SynthesisEdge.
 */

import type { EdgeProps } from '@xyflow/react';
import { GlowConnector } from './GlowConnector.tsx';

export function ReferenceEdge(props: EdgeProps) {
  return <GlowConnector {...props} kind="reference" />;
}
