/**
 * Unit tests — topic icon mapping (UI-02 topics rail)
 *
 * The rail derives a topic's glyph from its title, so the mapping is the
 * one piece of rail logic that is pure and worth pinning down: a topic
 * must keep the same icon across renders, reloads, and surfaces.
 */

import { describe, it, expect } from 'vitest';
import { Hash } from 'lucide-react';
import { topicIcon, orderTopics, DEFAULT_TOPIC_ICON } from '../topicIcons';

describe('topicIcon', () => {
  it('falls back to the hash glyph for an unmatched title', () => {
    expect(topicIcon('Off Topic')).toBe(Hash);
    expect(topicIcon('')).toBe(DEFAULT_TOPIC_ICON);
    expect(topicIcon('zzzz qqqq')).toBe(DEFAULT_TOPIC_ICON);
  });

  it('maps the mockup topic titles to distinct glyphs', () => {
    // docs/mockups/mockup-1.png shows a different icon per pill.
    const mockupTitles = [
      'Strategy',
      'Product Ideas',
      'Market Research',
      'User Feedback',
      'Roadmap',
      'Risks & Constraints',
      'Launch Plan',
      'Design Exploration',
    ];
    const icons = mockupTitles.map(topicIcon);

    // None of the named topics should fall through to the default.
    for (const [i, icon] of icons.entries()) {
      expect(icon, `${mockupTitles[i]} should have a semantic icon`).not.toBe(
        DEFAULT_TOPIC_ICON,
      );
    }

    // And each should be visually distinguishable from the others.
    expect(new Set(icons).size).toBe(mockupTitles.length);
  });

  it('is case-insensitive and matches on substrings', () => {
    expect(topicIcon('STRATEGY')).toBe(topicIcon('strategy'));
    expect(topicIcon('Q3 Strategy Review')).toBe(topicIcon('Strategy'));
  });

  it('is deterministic — the same title always yields the same icon', () => {
    for (const title of ['Strategy', 'Roadmap', 'anything else']) {
      expect(topicIcon(title)).toBe(topicIcon(title));
    }
  });

  it('resolves earlier keywords first when a title matches several', () => {
    // "Launch Plan" matches both /launch/ (Rocket) and /plan/ (Calendar);
    // the rocket rule is listed first, so it wins.
    expect(topicIcon('Launch Plan')).toBe(topicIcon('Launch'));
  });
});

describe('orderTopics', () => {
  const mk = (title: string, node_count?: number) => ({ title, node_count });

  it('sorts busiest subgraph first', () => {
    const ordered = orderTopics([mk('Small', 3), mk('Big', 14), mk('Mid', 8)]);
    expect(ordered.map((t) => t.title)).toEqual(['Big', 'Mid', 'Small']);
  });

  it('breaks ties alphabetically for a stable pill order', () => {
    const ordered = orderTopics([mk('Zulu', 5), mk('Alpha', 5), mk('Mike', 5)]);
    expect(ordered.map((t) => t.title)).toEqual(['Alpha', 'Mike', 'Zulu']);
  });

  it('treats a missing node_count as zero instead of NaN-sorting', () => {
    const ordered = orderTopics([mk('NoCount'), mk('Has', 1)]);
    expect(ordered.map((t) => t.title)).toEqual(['Has', 'NoCount']);
  });

  it('does not mutate its input', () => {
    const input = [mk('A', 1), mk('B', 9)];
    const copy = [...input];
    orderTopics(input);
    expect(input).toEqual(copy);
  });

  it('reproduces the mockup ordering from real seeded counts', () => {
    // Counts as seeded against the live API for UI-02 verification.
    const ordered = orderTopics([
      mk('Off Topic', 3), mk('Design Exploration', 11), mk('Launch Plan', 5),
      mk('Risks & Constraints', 6), mk('Roadmap', 7), mk('User Feedback', 9),
      mk('Market Research', 14), mk('Product Ideas', 8), mk('Strategy', 12),
    ]);
    expect(ordered.map((t) => t.title)).toEqual([
      'Market Research', 'Strategy', 'Design Exploration', 'User Feedback',
      'Product Ideas', 'Roadmap', 'Risks & Constraints', 'Launch Plan',
      'Off Topic',
    ]);
  });

  it('returns an empty array unchanged', () => {
    expect(orderTopics([])).toEqual([]);
  });
});
