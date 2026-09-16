/**
 * SPEC-PL-06 §7.2 — convergence edge rendering contract.
 *
 * Two renderers must agree on the same routes and the same labels: the
 * React Flow path (ReactFlow edge type + GlowConnector styling) and the
 * Canvas 2D fallback (canvasRenderer). These tests pin both, plus the
 * dispatch that keeps a reference edge from being drawn as a synthesis
 * contribution (§8.2 forbids claiming the node is a synthesis).
 */

import { describe, expect, it } from 'vitest';
import { getFlowEdgeType } from '../../layouts/d3Layout.ts';
import { connectorStyle } from '../../lib/canvasGeometry.ts';
import {
  flowEdgeKind,
  toCanvasEdges,
} from '../../lib/canvasRenderer.ts';
import { alpha } from '../../theme.ts';

// ─── React Flow dispatch ───────────────────────────────────────────────

describe('§7.2 reference edge dispatch', () => {
  it('routes a reference edge to referenceEdge, never to synthesisEdge', () => {
    expect(getFlowEdgeType('reference', false)).toBe('referenceEdge');
    // Even when the layout also sees the target as multi-parent: a
    // convergence edge is not a SPEC-API-04 synthesis contribution.
    expect(getFlowEdgeType('reference', true)).toBe('referenceEdge');
    expect(getFlowEdgeType('synthesis', false)).toBe('synthesisEdge');
    expect(getFlowEdgeType('reply', false)).toBe('replyEdge');
  });

  it('strokes exactly 2.5px in the edge colour, solid', () => {
    const style = connectorStyle('reference', {}, 'ref-4');
    expect(style.strokeWidth).toBe(2.5);
    expect(style.dash).toBeUndefined();
    expect(style.stroke).toBe(alpha('#DB2777', 0.92));
    expect(style.glow.startsWith('#DB2777')).toBe(true);
  });

  it('highlights by opacity, never by width', () => {
    const selected = connectorStyle('reference', { selected: true }, 'ref-0');
    const dimmed = connectorStyle('reference', { dimmed: true }, 'ref-0');
    const normal = connectorStyle('reference', {}, 'ref-0');

    expect(selected.strokeWidth).toBe(2.5);
    expect(dimmed.strokeWidth).toBe(2.5);
    expect(selected.stroke).toBe(alpha('#2563EB', 1));
    expect(dimmed.stroke).toBe(alpha('#2563EB', 0.22));
    expect(normal.stroke).toBe(alpha('#2563EB', 0.92));
  });

  it('falls back to ref-0 for an unknown colour key', () => {
    expect(connectorStyle('reference', {}, 'ref-bogus').stroke).toBe(alpha('#2563EB', 0.92));
  });

  it('leaves the other connector kinds untouched', () => {
    const reply = connectorStyle('reply', {});
    expect(reply.strokeWidth).toBe(1.6);
    expect(reply.dash).toBeUndefined();
    const synthesis = connectorStyle('synthesis', {});
    expect(synthesis.dash).toBe('7 5');
  });
});

// ─── Canvas 2D fallback parity ─────────────────────────────────────────

describe('§7.2 canvas fallback parity', () => {
  const referenceEdge = {
    id: 'edge-1',
    source: 'src-a',
    target: 'reply',
    type: 'referenceEdge',
    data: { edgeType: 'reference', isReference: true, colorKey: 'ref-2', sourceLabel: 'R3' },
  };

  it('keeps the R# label and the color_key on the scene edge', () => {
    const [scene] = toCanvasEdges([referenceEdge]);
    expect(scene).toEqual({
      source: 'src-a',
      target: 'reply',
      kind: 'reference',
      colorKey: 'ref-2',
      label: 'R3',
    });
  });

  it('detects a reference edge from its persisted type even without the component name', () => {
    expect(flowEdgeKind({ source: 'a', target: 'b', data: { edgeType: 'reference' } })).toBe(
      'reference',
    );
    expect(
      flowEdgeKind({ source: 'a', target: 'b', data: { edgeType: 'reply' }, type: 'replyEdge' }),
    ).toBe('reply');
  });

  it('does not carry a label or colour on lineage edges', () => {
    const [scene] = toCanvasEdges([
      { source: 'a', target: 'b', type: 'replyEdge', data: { edgeType: 'reply' } },
    ]);
    expect(scene.kind).toBe('reply');
    expect(scene.label).toBeUndefined();
    expect(scene.colorKey).toBeUndefined();
  });

  it('paints the same route with a 2.5px stroke and the R# label', async () => {
    const { drawEdges } = await import('../../lib/canvasRenderer.ts');
    const calls: string[] = [];
    const ctx = {
      strokeStyle: '',
      fillStyle: '',
      lineWidth: 0,
      font: '',
      textAlign: 'start',
      textBaseline: 'alphabetic',
      setLineDash: () => {},
      beginPath: () => calls.push('begin'),
      moveTo: (x: number, y: number) => calls.push(`move:${x},${y}`),
      lineTo: (x: number, y: number) => calls.push(`line:${x},${y}`),
      stroke: () => calls.push('stroke'),
      strokeText: (t: string) => calls.push(`strokeText:${t}`),
      fillText: (t: string) => calls.push(`fillText:${t}`),
    } as unknown as CanvasRenderingContext2D;

    const nodes = [
      { id: 'src-a', position: { x: 0, y: 0 }, label: 'A' },
      { id: 'reply', position: { x: 300, y: 0 }, label: 'Reply' },
    ];

    drawEdges(ctx, toCanvasEdges([referenceEdge]), nodes, { scale: 1, offsetX: 0, offsetY: 0 });

    expect(calls).toContain('move:0,0');
    expect(calls).toContain('line:300,0');
    expect(calls).toContain('fillText:R3');
    expect(calls).toContain('strokeText:R3');
  });

  it('keeps other kinds on the flat 1px overview line', async () => {
    const { drawEdges } = await import('../../lib/canvasRenderer.ts');
    const seen: number[] = [];
    const ctx = {
      strokeStyle: '',
      fillStyle: '',
      get lineWidth() {
        return 0;
      },
      set lineWidth(v: number) {
        seen.push(v);
      },
      font: '',
      textAlign: 'start',
      textBaseline: 'alphabetic',
      setLineDash: () => {},
      beginPath: () => {},
      moveTo: () => {},
      lineTo: () => {},
      stroke: () => {},
      strokeText: () => {},
      fillText: () => {},
    } as unknown as CanvasRenderingContext2D;

    const nodes = [
      { id: 'a', position: { x: 0, y: 0 }, label: 'A' },
      { id: 'b', position: { x: 100, y: 0 }, label: 'B' },
    ];
    drawEdges(
      ctx,
      toCanvasEdges([{ source: 'a', target: 'b', type: 'replyEdge' }]),
      nodes,
      { scale: 1, offsetX: 0, offsetY: 0 },
    );
    expect(seen).toEqual([1]);
  });
});
