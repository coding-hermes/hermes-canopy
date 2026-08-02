/**
 * Unit tests — canvas geometry & glow styling (UI-04 branching tree canvas)
 *
 * The connector shape and its glow are the most visible part of the
 * mockup-parity work, and both are pure maths over four coordinates —
 * which makes them cheap to pin here and expensive to eyeball in a
 * screenshot.
 */

import { describe, it, expect } from 'vitest';
import { palette } from '../../theme';
import {
  bezierCurvature,
  connectorAccent,
  connectorPath,
  connectorStyle,
  ghostSlotPosition,
  isFrontierSlot,
  jointDotPosition,
  nodeGlowClass,
  nodeGlowShadow,
  shouldShowGhostSlot,
  type BezierPoints,
} from '../canvasGeometry';

const SPAN: BezierPoints = {
  sourceX: 0,
  sourceY: 0,
  targetX: 200,
  targetY: 100,
};

describe('bezierCurvature', () => {
  it('scales with the horizontal gap', () => {
    const near = bezierCurvature({ ...SPAN, targetX: 60 });
    const far = bezierCurvature({ ...SPAN, targetX: 260 });
    expect(far).toBeGreaterThan(near);
  });

  it('clamps to a floor so short hops still bow', () => {
    const c = bezierCurvature({
      sourceX: 0,
      sourceY: 0,
      targetX: 2,
      targetY: 0,
    });
    expect(c).toBe(24);
  });

  it('clamps to a ceiling so wide spans do not knot', () => {
    const c = bezierCurvature({
      sourceX: 0,
      sourceY: 0,
      targetX: 5000,
      targetY: 0,
    });
    expect(c).toBe(140);
  });

  it('gives a purely vertical drop some bow via dy', () => {
    const c = bezierCurvature({
      sourceX: 0,
      sourceY: 0,
      targetX: 0,
      targetY: 400,
    });
    expect(c).toBeGreaterThan(24);
  });

  it('honours explicit bounds', () => {
    expect(
      bezierCurvature(SPAN, { min: 0, max: 10 }),
    ).toBe(10);
  });

  it('is direction-agnostic (right-to-left spans behave the same)', () => {
    const ltr = bezierCurvature({ sourceX: 0, sourceY: 0, targetX: 200, targetY: 0 });
    const rtl = bezierCurvature({ sourceX: 200, sourceY: 0, targetX: 0, targetY: 0 });
    expect(ltr).toBe(rtl);
  });
});

describe('connectorPath', () => {
  it('emits a cubic bezier starting at the source and ending at the target', () => {
    const { path } = connectorPath(SPAN);
    expect(path.startsWith('M0,0 C')).toBe(true);
    expect(path.endsWith('200,100')).toBe(true);
  });

  it('bows horizontally — control points are offset in x, not y', () => {
    const { path } = connectorPath(SPAN);
    const [, controls] = path.split(' C');
    const [c1, c2] = controls!.split(' ');
    // c1 keeps the source's y, c2 keeps the target's y → horizontal bow
    expect(c1!.endsWith(',0')).toBe(true);
    expect(c2!.endsWith(',100')).toBe(true);
  });

  it('midpoint sits halfway vertically', () => {
    expect(connectorPath(SPAN).midY).toBeCloseTo(50, 6);
  });

  it('midpoint sits between the endpoints horizontally', () => {
    const { midX } = connectorPath(SPAN);
    expect(midX).toBeGreaterThan(0);
    expect(midX).toBeLessThan(200);
  });

  it('is symmetric for a straight horizontal run', () => {
    const { midX, midY } = connectorPath({
      sourceX: 0,
      sourceY: 50,
      targetX: 100,
      targetY: 50,
    });
    expect(midX).toBeCloseTo(50, 6);
    expect(midY).toBeCloseTo(50, 6);
  });

  it('handles a zero-length span without producing NaN', () => {
    const { path, midX, midY } = connectorPath({
      sourceX: 10,
      sourceY: 10,
      targetX: 10,
      targetY: 10,
    });
    expect(path).not.toContain('NaN');
    expect(Number.isFinite(midX)).toBe(true);
    expect(Number.isFinite(midY)).toBe(true);
  });
});

describe('jointDotPosition', () => {
  it('sits on the curve near the parent, not at the midpoint', () => {
    const { midX } = connectorPath(SPAN);
    const dot = jointDotPosition(SPAN);
    expect(dot.x).toBeLessThan(midX);
    expect(dot.x).toBeGreaterThan(SPAN.sourceX);
  });

  it('t=0 lands exactly on the source', () => {
    const dot = jointDotPosition(SPAN, 0);
    expect(dot.x).toBeCloseTo(SPAN.sourceX, 6);
    expect(dot.y).toBeCloseTo(SPAN.sourceY, 6);
  });

  it('t=1 lands exactly on the target', () => {
    const dot = jointDotPosition(SPAN, 1);
    expect(dot.x).toBeCloseTo(SPAN.targetX, 6);
    expect(dot.y).toBeCloseTo(SPAN.targetY, 6);
  });

  it('clamps out-of-range t instead of flying off the curve', () => {
    expect(jointDotPosition(SPAN, -5)).toEqual(jointDotPosition(SPAN, 0));
    expect(jointDotPosition(SPAN, 5)).toEqual(jointDotPosition(SPAN, 1));
  });

  it('advances monotonically along the curve', () => {
    const a = jointDotPosition(SPAN, 0.2);
    const b = jointDotPosition(SPAN, 0.6);
    expect(b.x).toBeGreaterThan(a.x);
    expect(b.y).toBeGreaterThan(a.y);
  });
});

describe('connectorAccent', () => {
  it('maps each kind to a token colour', () => {
    expect(connectorAccent('reply')).toBe(palette.accent);
    expect(connectorAccent('fork')).toBe(palette.accent3);
    expect(connectorAccent('synthesis')).toBe(palette.warning);
  });
});

describe('connectorStyle', () => {
  it('paints a wider, fainter halo beneath the core stroke', () => {
    const s = connectorStyle('reply');
    expect(s.glowWidth).toBeGreaterThan(s.strokeWidth);
  });

  it('brightens and thickens when selected', () => {
    const base = connectorStyle('reply');
    const sel = connectorStyle('reply', { selected: true });
    expect(sel.strokeWidth).toBeGreaterThan(base.strokeWidth);
    expect(sel.glowWidth).toBeGreaterThan(base.glowWidth);
  });

  it('dims a connector into a collapsed branch', () => {
    const base = connectorStyle('reply');
    const dim = connectorStyle('reply', { dimmed: true });
    expect(dim.strokeWidth).toBeLessThanOrEqual(base.strokeWidth);
    expect(dim.stroke).not.toBe(base.stroke);
  });

  it('selection outranks dimming', () => {
    expect(connectorStyle('reply', { selected: true, dimmed: true })).toEqual(
      connectorStyle('reply', { selected: true }),
    );
  });

  it('dashes synthesis connectors only', () => {
    expect(connectorStyle('synthesis').dash).toBeDefined();
    expect(connectorStyle('reply').dash).toBeUndefined();
    expect(connectorStyle('fork').dash).toBeUndefined();
  });

  it('derives every colour from the kind accent — no stray hues', () => {
    for (const kind of ['reply', 'fork', 'synthesis'] as const) {
      const accent = connectorAccent(kind);
      const s = connectorStyle(kind);
      expect(s.stroke.startsWith(accent)).toBe(true);
      expect(s.glow.startsWith(accent)).toBe(true);
      expect(s.dot.startsWith(accent)).toBe(true);
    }
  });

  it('emits 8-digit hex with an alpha suffix', () => {
    expect(connectorStyle('reply').stroke).toMatch(/^#[0-9a-f]{8}$/i);
  });
});

describe('nodeGlowClass', () => {
  it('gives the selected node the violet neon', () => {
    expect(nodeGlowClass({ selected: true })).toBe('glow-accent-2');
  });

  it('gives a keyboard-focused node the cyan neon', () => {
    expect(nodeGlowClass({ focused: true })).toBe('glow-accent');
  });

  it('selection outranks focus', () => {
    expect(nodeGlowClass({ selected: true, focused: true })).toBe(
      'glow-accent-2',
    );
  });

  it('is empty when the node is idle', () => {
    expect(nodeGlowClass({})).toBe('');
  });
});

describe('nodeGlowShadow', () => {
  it('returns undefined when there is no glow', () => {
    expect(nodeGlowShadow(palette.accent2, 'none')).toBeUndefined();
  });

  it('composes the shadow from the given accent', () => {
    const shadow = nodeGlowShadow(palette.accent2, 'soft');
    expect(shadow).toContain(palette.accent2);
  });

  it('strong is more intense than soft', () => {
    const soft = nodeGlowShadow(palette.accent2, 'soft')!;
    const strong = nodeGlowShadow(palette.accent2, 'strong')!;
    expect(strong).not.toBe(soft);
    expect(strong).toContain('26px');
    expect(soft).toContain('14px');
  });
});

describe('isFrontierSlot', () => {
  it('marks an expanded leaf as the growing edge', () => {
    expect(isFrontierSlot({ childCount: 0, collapsed: false })).toBe(true);
  });

  it('is false for a node that already has replies', () => {
    expect(isFrontierSlot({ childCount: 2, collapsed: false })).toBe(false);
  });

  it('is false under a collapsed node — its children are merely hidden', () => {
    expect(isFrontierSlot({ childCount: 0, collapsed: true })).toBe(false);
  });

  it('is independent of who is looking', () => {
    // no readOnly parameter at all — this is a question about the graph
    expect(isFrontierSlot({ childCount: 0, collapsed: false })).toBe(
      isFrontierSlot({ childCount: 0, collapsed: false }),
    );
  });
});

describe('shouldShowGhostSlot', () => {
  it('offers a slot on an expanded leaf', () => {
    expect(shouldShowGhostSlot({ childCount: 0, collapsed: false })).toBe(true);
  });

  it('does not clutter nodes that already have replies', () => {
    expect(shouldShowGhostSlot({ childCount: 2, collapsed: false })).toBe(false);
  });

  it('never offers a slot under a collapsed node', () => {
    expect(shouldShowGhostSlot({ childCount: 0, collapsed: true })).toBe(false);
  });

  it('is suppressed in read-only mode', () => {
    expect(
      shouldShowGhostSlot({ childCount: 0, collapsed: false, readOnly: true }),
    ).toBe(false);
  });

  it('is the frontier test plus a write check', () => {
    for (const childCount of [0, 1]) {
      for (const collapsed of [false, true]) {
        expect(shouldShowGhostSlot({ childCount, collapsed })).toBe(
          isFrontierSlot({ childCount, collapsed }),
        );
      }
    }
  });
});

describe('ghostSlotPosition', () => {
  it('drops the slot below its parent by default', () => {
    expect(ghostSlotPosition({ x: 10, y: 20 })).toEqual({ x: 10, y: 170 });
  });

  it('honours an explicit offset', () => {
    expect(ghostSlotPosition({ x: 10, y: 20 }, { dx: 5, dy: 40 })).toEqual({
      x: 15,
      y: 60,
    });
  });
});
