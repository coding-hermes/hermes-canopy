/**
 * Hermes Canopy — iteration subtype renderer registry (SPEC-PL-04 §5.3 / §8.2)
 *
 * Maps the closed `IterationSubtype` enum to its renderer component. IterationCard
 * and IterationSidePanel both dispatch through this registry so compact vs
 * expanded rendering stays identical across surfaces.
 */

import type { ComponentType } from 'react';
import type {
  IterationRendererProps,
  IterationSubtype,
} from '../types/agent.ts';
import { IterationSearchCard } from '../components/agent/IterationSearchCard.tsx';
import { IterationCodeExecCard } from '../components/agent/IterationCodeExecCard.tsx';
import { IterationFileReadCard } from '../components/agent/IterationFileReadCard.tsx';
import { IterationThinkingCard } from '../components/agent/IterationThinkingCard.tsx';
import { IterationToolCallCard } from '../components/agent/IterationToolCallCard.tsx';

export type IterationRenderer = ComponentType<IterationRendererProps>;

export const iterationRenderers: Record<IterationSubtype, IterationRenderer> = {
  iteration_search: IterationSearchCard as IterationRenderer,
  iteration_code_exec: IterationCodeExecCard as IterationRenderer,
  iteration_file_read: IterationFileReadCard as IterationRenderer,
  iteration_thinking: IterationThinkingCard as IterationRenderer,
  iteration_tool_call: IterationToolCallCard as IterationRenderer,
};

/** Resolve a renderer by subtype. Falls back to null for an unknown subtype. */
export function resolveIterationRenderer(subtype: string): IterationRenderer | null {
  return (iterationRenderers as Record<string, IterationRenderer>)[subtype] ?? null;
}
