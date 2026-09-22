import { describe, expect, it } from 'vitest';
import { CompactCard, ExpandedCard, IterationCard } from '../../components/CardFrame.tsx';
import { DEFAULT_CARD_RENDERER, resolveCardRenderer } from '../cardRenderers.ts';

describe('card renderer registry', () => {
  it('resolves each registered canopy card type to its built-in component', () => {
    expect(resolveCardRenderer('canopy', 'compact')).toBe(CompactCard);
    expect(resolveCardRenderer('canopy', 'expanded')).toBe(ExpandedCard);
    expect(resolveCardRenderer('canopy', 'iteration')).toBe(IterationCard);
  });

  it('resolves the built-in compact app ids explicitly', () => {
    expect(resolveCardRenderer('canopy.task', 'compact')).toBe(CompactCard);
    expect(resolveCardRenderer('canopy.file', 'compact')).toBe(CompactCard);
    expect(resolveCardRenderer('canopy.code', 'compact')).toBe(CompactCard);
  });

  it('falls back for an unknown app and card-type pair', () => {
    expect(resolveCardRenderer('unknown-app', 'compact')).toBe(DEFAULT_CARD_RENDERER);
    expect(resolveCardRenderer('canopy', 'unknown-type')).toBe(DEFAULT_CARD_RENDERER);
    expect(resolveCardRenderer('canopy.task', 'expanded')).toBe(DEFAULT_CARD_RENDERER);
  });
});
