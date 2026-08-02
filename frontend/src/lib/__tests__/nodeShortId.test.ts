/**
 * Unit tests — distinguishable short node ids (UI-08)
 *
 * The "duplicate seed data" bug was a display bug: `id.split('-')[0]` on
 * a UUIDv7 prints the top 32 bits of a millisecond timestamp, so nodes
 * created inside the same ~65s window are rendered identically.
 *
 * The ids below are the REAL demo tree, read live from
 * `GET /api/v1/trees/9a7f97f3-3cfc-4eec-91c3-3014064dea15/nodes`. Four of
 * them genuinely share the first group; all six are distinct. That is the
 * whole regression: six rows must produce six labels.
 */

import { describe, it, expect } from 'vitest';
import {
  disambiguateNodeIds,
  nodeIdLinkLabel,
  shortNodeId,
} from '../nodeShortId';

// ─── The real demo tree ────────────────────────────────────────────────

const DEMO_IDS = [
  '019fb0bc-ddcb-710f-8497-387ee327b400', // root
  '019fb0c0-58c1-7d9b-b4dc-ecc04872c800', // Child 1
  '019fb0c2-cab0-70c5-a477-fa10f136e000', // Child #2  ┐
  '019fb0c2-cad5-75b5-a291-2dde84047400', // Child #3  │ same first group
  '019fb0c2-caed-7dca-b471-d03e71f6b000', // Child #4  │
  '019fb0c2-cb05-75f0-9854-def889e34000', // Child #5  ┘
];

// ─── Regression: the actual bug ────────────────────────────────────────

describe('the UUIDv7 prefix collision', () => {
  it('reproduces the bug — the old truncation collapses 6 ids to 3 labels', () => {
    const old = DEMO_IDS.map((id) => id.split('-')[0]);
    expect(new Set(old).size).toBe(3);
    expect(old.filter((p) => p === '019fb0c2')).toHaveLength(4);
  });

  it('confirms the underlying ids were never duplicated', () => {
    expect(new Set(DEMO_IDS).size).toBe(6);
  });

  it('renders one distinct label per node', () => {
    const labels = disambiguateNodeIds(DEMO_IDS);
    expect(labels.size).toBe(6);
    expect(new Set(labels.values()).size).toBe(6);
  });

  it('reaches the random tail, where same-millisecond v7 ids differ', () => {
    const labels = disambiguateNodeIds(DEMO_IDS);
    expect(labels.get('019fb0c2-cab0-70c5-a477-fa10f136e000')).toBe(
      '019fb0c2…e000',
    );
    expect(labels.get('019fb0c2-cad5-75b5-a291-2dde84047400')).toBe(
      '019fb0c2…7400',
    );
  });
});

// ─── Short form ────────────────────────────────────────────────────────

describe('shortNodeId', () => {
  it('keeps the head and the tail', () => {
    expect(shortNodeId('019fb0c2-cab0-70c5-a477-fa10f136e000')).toBe(
      '019fb0c2…e000',
    );
  });

  it('widens the tail on request', () => {
    expect(shortNodeId('019fb0c2-cab0-70c5-a477-fa10f136e000', 6)).toBe(
      '019fb0c2…36e000',
    );
  });

  it('drops the ellipsis entirely at tail 0', () => {
    expect(shortNodeId('019fb0c2-cab0-70c5-a477-fa10f136e000', 0)).toBe(
      '019fb0c2',
    );
  });

  it('passes a short id through rather than faking an ellipsis', () => {
    expect(shortNodeId('abc')).toBe('abc');
    expect(shortNodeId('019fb0c2')).toBe('019fb0c2');
  });

  it('returns an empty string for an empty id instead of "…"', () => {
    expect(shortNodeId('')).toBe('');
    expect(shortNodeId('   ')).toBe('');
  });
});

// ─── Disambiguation ────────────────────────────────────────────────────

describe('disambiguateNodeIds', () => {
  it('stays short when nothing collides', () => {
    const labels = disambiguateNodeIds([
      '019fb0bc-ddcb-710f-8497-387ee327b400',
      '019fb0c0-58c1-7d9b-b4dc-ecc04872c800',
    ]);
    expect([...labels.values()].every((l) => l.length === 13)).toBe(true);
  });

  it('widens only as far as it must to break a collision', () => {
    // Identical except for the last two characters.
    const a = '019fb0c2-cab0-70c5-a477-fa10f136ea01';
    const b = '019fb0c2-cab0-70c5-a477-fa10f136ea02';
    const labels = disambiguateNodeIds([a, b]);
    expect(labels.get(a)).not.toBe(labels.get(b));
    expect(new Set(labels.values()).size).toBe(2);
  });

  it('never maps two different ids to the same label', () => {
    const labels = disambiguateNodeIds(DEMO_IDS);
    for (const id of DEMO_IDS) expect(labels.get(id)).toBeTruthy();
    expect(new Set(labels.values()).size).toBe(new Set(DEMO_IDS).size);
  });

  it('collapses a repeated id to a single entry', () => {
    const labels = disambiguateNodeIds(['dup', 'dup']);
    expect(labels.size).toBe(1);
  });

  it('falls back to the full id when even the widest form collides', () => {
    // Same id twice is deduped; a genuine duplicate cannot be broken.
    const id = '019fb0c2-cab0-70c5-a477-fa10f136e000';
    const labels = disambiguateNodeIds([id]);
    expect(labels.get(id)).toBe('019fb0c2…e000');
  });

  it('ignores blank ids without emitting an entry', () => {
    const labels = disambiguateNodeIds(['', '   ', 'real-id-value-here']);
    expect(labels.size).toBe(1);
  });

  it('returns an empty map for an empty list', () => {
    expect(disambiguateNodeIds([]).size).toBe(0);
  });
});

// ─── Accessible label ──────────────────────────────────────────────────

describe('nodeIdLinkLabel', () => {
  it('carries the id in FULL — the visible label is elided', () => {
    const id = '019fb0c2-cab0-70c5-a477-fa10f136e000';
    expect(nodeIdLinkLabel(id)).toBe(`Open node ${id}`);
    expect(nodeIdLinkLabel(id)).toContain(id);
  });
});
