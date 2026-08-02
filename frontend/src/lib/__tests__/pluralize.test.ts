/**
 * Unit tests — countable phrasing (UI-08)
 *
 * "(1 nodes)" shipped to a screenshot. These pin the boundary that
 * actually failed (exactly 1) plus the two inputs a real payload
 * supplies that a fixture never does: 0 and a missing count.
 */

import { describe, it, expect } from 'vitest';
import { countLabel, filteredCountLabel, pluralize } from '../pluralize';

describe('pluralize', () => {
  it('singularises exactly one', () => {
    expect(pluralize(1, 'node')).toBe('node');
  });

  it('pluralises zero — "0 nodes", never "0 node"', () => {
    expect(pluralize(0, 'node')).toBe('nodes');
  });

  it('pluralises everything above one', () => {
    expect(pluralize(2, 'node')).toBe('nodes');
    expect(pluralize(6, 'node')).toBe('nodes');
    expect(pluralize(100, 'node')).toBe('nodes');
  });

  it('takes an explicit plural for irregular nouns', () => {
    expect(pluralize(1, 'entry', 'entries')).toBe('entry');
    expect(pluralize(3, 'entry', 'entries')).toBe('entries');
  });

  it('treats -1 as singular so a signed delta still reads correctly', () => {
    expect(pluralize(-1, 'node')).toBe('node');
  });
});

describe('countLabel', () => {
  it('fixes the exact string from the screenshot', () => {
    // Was: "(1 nodes)" in the tree selector.
    expect(countLabel(1, 'node')).toBe('1 node');
  });

  it('renders the demo tree count', () => {
    expect(countLabel(6, 'node')).toBe('6 nodes');
  });

  it('renders zero', () => {
    expect(countLabel(0, 'node')).toBe('0 nodes');
  });

  it('renders 0 rather than "NaN nodes" for a missing count', () => {
    expect(countLabel(NaN, 'node')).toBe('0 nodes');
    expect(countLabel(Infinity, 'node')).toBe('0 nodes');
  });

  it('carries an irregular plural through', () => {
    expect(countLabel(2, 'entry', 'entries')).toBe('2 entries');
  });
});

describe('filteredCountLabel', () => {
  it('agrees with the TOTAL, not the shown subset', () => {
    // "1 of 6 nodes" — plural, because six exist. Inflecting on `shown`
    // would print the wrong "1 of 6 node".
    expect(filteredCountLabel(1, 6, 'node')).toBe('1 of 6 nodes');
  });

  it('singularises when the total really is one', () => {
    expect(filteredCountLabel(1, 1, 'node')).toBe('1 of 1 node');
  });

  it('handles an empty result set against a populated tree', () => {
    expect(filteredCountLabel(0, 6, 'node')).toBe('0 of 6 nodes');
  });

  it('handles an empty tree', () => {
    expect(filteredCountLabel(0, 0, 'node')).toBe('0 of 0 nodes');
  });

  it('coerces non-finite inputs instead of rendering NaN', () => {
    expect(filteredCountLabel(NaN, NaN, 'node')).toBe('0 of 0 nodes');
  });
});
