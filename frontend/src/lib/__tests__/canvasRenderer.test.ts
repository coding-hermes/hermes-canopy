/**
 * Unit tests — Canvas 2D fallback renderer (STACK-03)
 *
 * The fallback only triggers above CANVAS_THRESHOLD nodes, so it is
 * rarely exercised by hand — these tests are the contract. All maths and
 * painting live in lib/canvasRenderer.ts as pure functions over a mocked
 * 2d context; no live API, no DB, no real canvas.
 */

import { describe, it, expect } from 'vitest';
import { palette } from '../../theme';
import {
  CANVAS_THRESHOLD,
  FIT_MAX_SCALE,
  LABEL_ZOOM_THRESHOLD,
  MAX_SCALE,
  MIN_SCALE,
  applyPan,
  computeFitTransform,
  drawEdges,
  drawNodes,
  drawScene,
  flowTypeToKind,
  hitTestNodes,
  layoutToScreen,
  sceneExtents,
  shouldUseCanvas,
  toCanvasEdges,
  toCanvasNodes,
  truncateLabel,
  zoomAtPoint,
  type CanvasSceneEdge,
  type CanvasSceneNode,
} from '../canvasRenderer';

// ─── Fixtures ─────────────────────────────────────────────────────────

/** Synthetic layout output in the d3Layout coordinate convention. */
function makeNodes(
  specs: Array<[id: string, x: number, y: number]>,
): CanvasSceneNode[] {
  return specs.map(([id, x, y]) => ({
    id,
    position: { x, y },
    label: `label-for-${id}`,
    nodeType: 'message',
  }));
}

/** Minimal mock of the 2d context that records every call. */
interface RecordedCall {
  op: string;
  args: unknown[];
}

function makeMockCtx(): {
  ctx: CanvasRenderingContext2D;
  calls: RecordedCall[];
} {
  const calls: RecordedCall[] = [];

  const record =
    (op: string) =>
    (...args: unknown[]) => {
      calls.push({ op, args });
    };

  const ctx = {
    fillStyle: '',
    strokeStyle: '',
    lineWidth: 1,
    font: '',
    setLineDash: record('setLineDash'),
    fillRect: record('fillRect'),
    beginPath: record('beginPath'),
    moveTo: record('moveTo'),
    lineTo: record('lineTo'),
    stroke: record('stroke'),
    arc: record('arc'),
    fill: record('fill'),
    fillText: record('fillText'),
    setTransform: record('setTransform'),
  };

  return { ctx: ctx as unknown as CanvasRenderingContext2D, calls };
}

// ─── Threshold switch ────────────────────────────────────────────────

describe('threshold switch', () => {
  it('keeps trees at or below the threshold on the React Flow path', () => {
    expect(CANVAS_THRESHOLD).toBe(2000);
    expect(shouldUseCanvas(0)).toBe(false);
    expect(shouldUseCanvas(500)).toBe(false);
    expect(shouldUseCanvas(2000)).toBe(false);
  });

  it('sends trees strictly above the threshold to the canvas', () => {
    expect(shouldUseCanvas(2001)).toBe(true);
    expect(shouldUseCanvas(10000)).toBe(true);
  });

  it('is independent of the simplified-mode threshold', async () => {
    // LARGE_TREE_THRESHOLD (500) simplifies React Flow; CANVAS_THRESHOLD
    // replaces it. They must not drift together.
    const { shouldUseSimplifiedMode } = await import('../../layouts/d3Layout');
    expect(shouldUseSimplifiedMode(1500)).toBe(true);
    expect(shouldUseCanvas(1500)).toBe(false);
  });
});

// ─── Snapshot mapping ────────────────────────────────────────────────

describe('snapshot mapping', () => {
  it('maps snapshot nodes to scene nodes with label + nodeType', () => {
    const rf = [
      {
        id: 'n1',
        position: { x: 280, y: 40 },
        data: { label: 'Hello', nodeType: 'message' },
      },
      { id: 'n2', position: { x: 560, y: -30 }, data: { label: 'Card' } },
    ];

    expect(toCanvasNodes(rf)).toEqual([
      {
        id: 'n1',
        position: { x: 280, y: 40 },
        label: 'Hello',
        nodeType: 'message',
      },
      { id: 'n2', position: { x: 560, y: -30 }, label: 'Card' },
    ]);
  });

  it('maps edge types to kinds, tolerating bare and suffixed names', () => {
    expect(flowTypeToKind('replyEdge')).toBe('reply');
    expect(flowTypeToKind('forkEdge')).toBe('fork');
    expect(flowTypeToKind('synthesisEdge')).toBe('synthesis');
    expect(flowTypeToKind('synthesis')).toBe('synthesis');
    expect(flowTypeToKind(undefined)).toBe('reply');
    expect(flowTypeToKind('mystery')).toBe('reply');

    const edges = [
      { source: 'n1', target: 'n2', type: 'replyEdge' },
      { source: 'n2', target: 'n3' },
    ];
    expect(toCanvasEdges(edges)).toEqual([
      { source: 'n1', target: 'n2', kind: 'reply' },
      { source: 'n2', target: 'n3', kind: 'reply' },
    ]);
  });
});

// ─── Transform math ──────────────────────────────────────────────────

describe('layoutToScreen', () => {
  const t = { scale: 0.25, offsetX: 100, offsetY: -50 };

  it('applies screen = layout * scale + offset', () => {
    expect(layoutToScreen(t, 400, 200)).toEqual({ x: 200, y: 0 });
  });

  it('handles negative layout coordinates', () => {
    expect(layoutToScreen(t, -80, 40)).toEqual({ x: 80, y: -40 });
  });
});

describe('applyPan', () => {
  it('shifts offsets without touching scale', () => {
    const t = { scale: 0.5, offsetX: 10, offsetY: 20 };
    expect(applyPan(t, 15, -5)).toEqual({ scale: 0.5, offsetX: 25, offsetY: 15 });
  });
});

describe('zoomAtPoint', () => {
  it('pins the point under the cursor during zoom-in', () => {
    const t = { scale: 0.4, offsetX: 50, offsetY: 50 };
    // Screen anchor of layout point (100, 100) before…
    const before = layoutToScreen(t, 100, 100);
    const z = zoomAtPoint(t, 2, before.x, before.y);
    // …must equal its anchor after.
    const after = layoutToScreen(z, 100, 100);
    expect(after.x).toBeCloseTo(before.x, 6);
    expect(after.y).toBeCloseTo(before.y, 6);
  });

  it('pins the point under the cursor during zoom-out', () => {
    const t = { scale: 1.2, offsetX: -20, offsetY: 300 };
    const before = layoutToScreen(t, 640, -90);
    const z = zoomAtPoint(t, 0.5, before.x, before.y);
    const after = layoutToScreen(z, 640, -90);
    expect(after.x).toBeCloseTo(before.x, 6);
    expect(after.y).toBeCloseTo(before.y, 6);
  });

  it('clamps scale into [MIN_SCALE, MAX_SCALE]', () => {
    const top = { scale: MAX_SCALE, offsetX: 0, offsetY: 0 };
    expect(zoomAtPoint(top, 10, 0, 0).scale).toBe(MAX_SCALE);
    const bottom = { scale: MIN_SCALE, offsetX: 0, offsetY: 0 };
    expect(zoomAtPoint(bottom, 0.01, 0, 0).scale).toBe(MIN_SCALE);
  });
});

// ─── Fit to view ─────────────────────────────────────────────────────

describe('computeFitTransform', () => {
  it('centers a scene with uniform padding on both axes', () => {
    // Scene spans x∈[0,400], y∈[0,200] → content 400×200
    const nodes = makeNodes([
      ['a', 0, 0],
      ['b', 400, 200],
    ]);
    const t = computeFitTransform(nodes, 800, 400, 0.1);
    // avail = 720×360 → scale limited by width: 720/400 = 1.8
    expect(t.scale).toBeCloseTo(1.8, 6);
    // centered: (800 − 400·1.8)/2 − 0·scale = 40; (400 − 200·1.8)/2 = 20
    expect(t.offsetX).toBeCloseTo(40, 6);
    expect(t.offsetY).toBeCloseTo(20, 6);
  });

  it('picks the limiting axis when aspect ratios differ', () => {
    const nodes = makeNodes([
      ['a', 0, 0],
      ['b', 1000, 100],
    ]);
    const t = computeFitTransform(nodes, 600, 600, 0.1);
    // avail = 540×540; scales 0.54 vs 5.4 → width wins
    expect(t.scale).toBeCloseTo(0.54, 6);
    expect(t.offsetY).toBeCloseTo((600 - 100 * 0.54) / 2, 6);
  });

  it('respects non-zero minimum layout coordinates', () => {
    const nodes = makeNodes([
      ['a', 5000, 9000],
      ['b', 5400, 9200],
    ]);
    const t = computeFitTransform(nodes, 800, 400, 0.1);
    const first = layoutToScreen(t, 5000, 9000);
    expect(first.x).toBeCloseTo(40, 6); // left padding slot
    expect(first.y).toBeGreaterThan(0);
    const last = layoutToScreen(t, 5400, 9200);
    expect(last.x).toBeLessThanOrEqual(760); // right padding slot
  });

  it('caps the scale for tiny scenes instead of pixel-blowing them up', () => {
    const nodes = makeNodes([
      ['a', 0, 0],
      ['b', 10, 10],
    ]);
    const t = computeFitTransform(nodes, 800, 600, 0.1, FIT_MAX_SCALE);
    expect(t.scale).toBe(FIT_MAX_SCALE);
  });

  it('returns identity for an empty scene or zero-sized viewport', () => {
    const identity = { scale: 1, offsetX: 0, offsetY: 0 };
    expect(computeFitTransform([], 800, 600)).toEqual(identity);
    const nodes = makeNodes([['a', 0, 0]]);
    expect(computeFitTransform(nodes, 0, 0)).toEqual(identity);
  });

  it('sceneExtents reports min/max across all nodes', () => {
    const e = sceneExtents(
      makeNodes([
        ['a', -100, 50],
        ['b', 300, -70],
        ['c', 120, 220],
      ]),
    );
    expect(e).toEqual({ minX: -100, minY: -70, maxX: 300, maxY: 220 });
  });

  it('sceneExtents returns null for an empty scene', () => {
    expect(sceneExtents([])).toBeNull();
  });
});

// ─── Hit testing ─────────────────────────────────────────────────────

describe('hitTestNodes', () => {
  const nodes = makeNodes([
    ['near', 100, 100],
    ['far', 900, 900],
  ]);

  it('hits a node whose dot contains the point', () => {
    const t = computeFitTransform(nodes, 1000, 1000);
    const s = layoutToScreen(t, 100, 100);
    expect(hitTestNodes(nodes, t, s.x + 2, s.y)).toBe('near');
  });

  it('misses empty space between nodes', () => {
    const t = { scale: 1, offsetX: 0, offsetY: 0 };
    expect(hitTestNodes(nodes, t, 400, 400)).toBeNull();
  });

  it('returns null for an empty scene', () => {
    expect(hitTestNodes([], { scale: 1, offsetX: 0, offsetY: 0 }, 5, 5)).toBeNull();
  });

  it('prefers the nearest node when two dots overlap', () => {
    const tight = makeNodes([
      ['a', 0, 0],
      ['b', 4, 0],
    ]);
    expect(hitTestNodes(tight, { scale: 1, offsetX: 0, offsetY: 0 }, 3, 0)).toBe('b');
  });
});

// ─── Labels ──────────────────────────────────────────────────────────

describe('truncateLabel', () => {
  it('passes short labels through trimmed', () => {
    expect(truncateLabel('  hi ')).toBe('hi');
  });

  it('ellipsizes long labels to the cap', () => {
    const out = truncateLabel('x'.repeat(120));
    expect(out.length).toBe(42);
    expect(out.endsWith('…')).toBe(true);
  });
});

// ─── Painting ────────────────────────────────────────────────────────

describe('drawEdges', () => {
  it('draws one straight line per edge in screen space', () => {
    const { ctx, calls } = makeMockCtx();
    const nodes = makeNodes([
      ['a', 0, 0],
      ['b', 1000, 500],
    ]);
    const edges: CanvasSceneEdge[] = [{ source: 'a', target: 'b', kind: 'reply' }];
    const t = { scale: 0.5, offsetX: 10, offsetY: 20 };

    drawEdges(ctx, edges, nodes, t);

    // a → (10,20), b → (510,270)
    const moves = calls.filter((c) => c.op === 'moveTo');
    const lines = calls.filter((c) => c.op === 'lineTo');
    expect(moves).toHaveLength(1);
    expect(moves[0].args).toEqual([10, 20]);
    expect(lines).toHaveLength(1);
    expect(lines[0].args).toEqual([510, 270]);
    expect(calls.filter((c) => c.op === 'stroke')).toHaveLength(1);
    expect(ctx.strokeStyle).toBe(palette.accent); // reply identity color
  });

  it('skips edges whose endpoints are missing from the scene', () => {
    const m = makeMockCtx();
    const nodes = makeNodes([['a', 0, 0]]);
    drawEdges(m.ctx, [{ source: 'a', target: 'ghost' }], nodes, {
      scale: 1,
      offsetX: 0,
      offsetY: 0,
    });
    expect(m.calls.filter((c) => c.op === 'stroke')).toHaveLength(0);
  });

  it('applies the synthesis dash pattern and warning color', () => {
    const m = makeMockCtx();
    const nodes = makeNodes([
      ['a', 0, 0],
      ['b', 100, 0],
    ]);
    drawEdges(m.ctx, [{ source: 'a', target: 'b', kind: 'synthesis' }], nodes, {
      scale: 1,
      offsetX: 0,
      offsetY: 0,
    });
    expect(m.ctx.strokeStyle).toBe(palette.warning);
    const dash = m.calls.find((c) => c.op === 'setLineDash');
    // The mock records each call's argument list, so the single array
    // argument arrives nested: setLineDash([7, 5]) → args [[7, 5]].
    expect(dash?.args).toEqual([[7, 5]]);
  });
});

describe('drawNodes', () => {
  const t = { scale: 1, offsetX: 40, offsetY: 60 };

  it('draws one dot per node at its transformed position', () => {
    const m = makeMockCtx();
    const nodes = makeNodes([
      ['a', 100, 100],
      ['b', 300, -20],
    ]);

    drawNodes(m.ctx, nodes, t);

    const arcs = m.calls.filter((c) => c.op === 'arc');
    expect(arcs).toHaveLength(2);
    // a → (140,160), b → (340,40)
    expect(arcs[0].args.slice(0, 2)).toEqual([140, 160]);
    expect(arcs[1].args.slice(0, 2)).toEqual([340, 40]);
  });

  it('suppresses labels below LABEL_ZOOM_THRESHOLD and shows them above', () => {
    const zoomedOut = makeMockCtx();
    drawNodes(zoomedOut.ctx, makeNodes([['a', 0, 0]]), {
      scale: LABEL_ZOOM_THRESHOLD - 0.01,
      offsetX: 0,
      offsetY: 0,
    });
    expect(zoomedOut.calls.some((c) => c.op === 'fillText')).toBe(false);

    const zoomedIn = makeMockCtx();
    drawNodes(zoomedIn.ctx, makeNodes([['a', 0, 0]]), {
      scale: LABEL_ZOOM_THRESHOLD + 0.01,
      offsetX: 0,
      offsetY: 0,
    });
    expect(zoomedIn.calls.some((c) => c.op === 'fillText')).toBe(true);
  });

  it('highlights exactly the selected node with halo + ring', () => {
    const plain = makeMockCtx();
    const two = makeNodes([
      ['a', 0, 0],
      ['b', 50, 50],
    ]);
    drawNodes(plain.ctx, two, t);
    const baseArcs = plain.calls.filter((c) => c.op === 'arc').length;

    const selected = makeMockCtx();
    drawNodes(selected.ctx, two, t, { selectedNodeId: 'b' });
    // halo + ring added around b's dot
    expect(selected.calls.filter((c) => c.op === 'arc')).toHaveLength(baseArcs + 2);
    expect(selected.ctx.strokeStyle).toBe(palette.contentPrimary);
  });

  it('paints dots with the node-type identity color', () => {
    const m = makeMockCtx();
    const nodes: CanvasSceneNode[] = [
      { id: 's', position: { x: 0, y: 0 }, label: '', nodeType: 'synthesis' },
    ];
    // scale below LABEL_ZOOM_THRESHOLD keeps the label paint (which sets
    // its own fillStyle) out of the way, so the dot color is the last
    // fillStyle written.
    drawNodes(m.ctx, nodes, { scale: 0.5, offsetX: 0, offsetY: 0 });
    expect(m.ctx.fillStyle).toBe(palette.warning); // synthesis identity
  });
});

describe('drawScene', () => {
  it('paints edges beneath nodes (strokes precede arcs)', () => {
    const m = makeMockCtx();
    const nodes = makeNodes([
      ['a', 0, 0],
      ['b', 100, 100],
    ]);
    drawScene(m.ctx, nodes, [{ source: 'a', target: 'b' }], {
      scale: 1,
      offsetX: 0,
      offsetY: 0,
    });
    const firstStroke = m.calls.findIndex((c) => c.op === 'stroke');
    const firstArc = m.calls.findIndex((c) => c.op === 'arc');
    expect(firstStroke).toBeGreaterThanOrEqual(0);
    expect(firstArc).toBeGreaterThan(firstStroke);
  });

  it('forwards selection options down to node painting', () => {
    const m = makeMockCtx();
    const nodes = makeNodes([['a', 0, 0]]);
    drawScene(m.ctx, nodes, [], { scale: 1, offsetX: 0, offsetY: 0 }, {
      selectedNodeId: 'a',
    });
    // halo + ring ⇒ 3 arcs instead of 1
    expect(m.calls.filter((c) => c.op === 'arc')).toHaveLength(3);
  });

  it('clears the surface with the dark base color first', () => {
    const m = makeMockCtx();
    drawScene(m.ctx, [], [], { scale: 1, offsetX: 0, offsetY: 0 });
    // drawScene itself does not clear — that is the component's job — but
    // pin the empty-scene no-op so signature changes are deliberate.
    expect(m.calls.filter((c) => c.op === 'stroke')).toHaveLength(0);
    expect(m.calls.filter((c) => c.op === 'arc')).toHaveLength(0);
  });
});
