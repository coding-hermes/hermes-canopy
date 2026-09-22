import type { ComponentType } from 'react';
import {
  CardFrame,
  CompactCard,
  ExpandedCard,
  IterationCard,
  type CardFrameProps,
} from '../components/CardFrame.tsx';

export type CardRenderer = ComponentType<CardFrameProps>;

/** The safe renderer used whenever an app adapter has no exact registration. */
export const DEFAULT_CARD_RENDERER: CardRenderer = CardFrame;

const BUILTIN_RENDERERS: Readonly<Record<string, CardRenderer>> = {
  'canopy:compact': CompactCard,
  'canopy:expanded': ExpandedCard,
  'canopy:iteration': IterationCard,
  'canopy.task:compact': CompactCard,
  'canopy.file:compact': CompactCard,
  'canopy.code:compact': CompactCard,
};

function rendererKey(appId: string, cardType: string): string {
  return `${appId}:${cardType}`;
}

/** Resolve by the complete `(appId, cardType)` pair, never by type alone. */
export function resolveCardRenderer(appId: string, cardType: string): CardRenderer {
  return BUILTIN_RENDERERS[rendererKey(appId, cardType)] ?? DEFAULT_CARD_RENDERER;
}
