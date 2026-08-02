/**
 * Hermes Canopy — ForkEdge
 *
 * Branch/fork link: glowing magenta bezier (UI-04). Indicates a new branch
 * created from a conversation node.
 */

import type { EdgeProps } from '@xyflow/react';
import { GlowConnector } from './GlowConnector.tsx';

export function ForkEdge(props: EdgeProps) {
  return <GlowConnector {...props} kind="fork" />;
}
