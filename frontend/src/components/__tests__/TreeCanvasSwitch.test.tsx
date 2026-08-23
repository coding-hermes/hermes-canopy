/**
 * Component tests — TreeCanvas renderer switch (STACK-03)
 *
 * Pins the threshold swap: the default export must pass trees at/below
 * CANVAS_THRESHOLD straight into the React Flow path (unchanged
 * behavior) and route anything larger to CanvasTreeView. Both renderers
 * are stubbed, so this never touches the live API or DB — the tree
 * fixture is synthetic layout data.
 *
 * Note: the deep geometry/colour maths lives in lib/canvasRenderer.ts
 * and is pinned by canvasRenderer.test.ts; this file only pins that
 * TreeCanvas actually performs the swap at the component boundary.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { act } from 'react';
import { createElement, type ReactNode } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { UseYjsTreeResult } from '../../stores/useYjsTree.ts';
import { CANVAS_THRESHOLD } from '../../lib/canvasRenderer.ts';

// ─── Renderer stubs ──────────────────────────────────────────────────

const canvasView = vi.fn((_props: TreeCanvasPropsStub) => null as ReactNode);
const flowView = vi.fn((_props: unknown) => null as ReactNode);

interface TreeCanvasPropsStub {
  tree: UseYjsTreeResult;
  onSelectionChange?: (nodeId: string | null) => void;
}

vi.mock('../CanvasTreeView.tsx', () => ({
  default: (props: TreeCanvasPropsStub) => canvasView(props),
}));

vi.mock('@xyflow/react', async () => {
  const actual = await import('@xyflow/react');
  return {
    ...actual,
    // Provider passes children through; React Flow itself is a marker.
    ReactFlowProvider: ({ children }: { children: ReactNode }) => children,
    ReactFlow: (props: Record<string, unknown>) => flowView(props),
    // TreeCanvasInner calls useReactFlow() during render; the passthrough
    // provider above provides no context, so stub the hook (the real
    // implementation would throw outside a real provider).
    useReactFlow: () => ({
      fitView: vi.fn(),
      setCenter: vi.fn(),
      zoomIn: vi.fn(),
      zoomOut: vi.fn(),
      getViewport: () => ({ x: 0, y: 0, zoom: 1 }),
      getNodes: () => [],
    }),
  };
});

import TreeCanvas from '../TreeCanvas.tsx';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

// ─── Synthetic fixture ────────────────────────────────────────────────

function fakeTree(nodeCount: number): UseYjsTreeResult {
  const nodes = Array.from({ length: nodeCount }, (_, i) => ({
    id: `n${i}`,
    position: { x: (i % 50) * 280, y: Math.floor(i / 50) * 160 },
    data: {
      label: `node ${i}`,
      nodeType: i % 5 === 0 ? 'card' : 'message',
      content: '',
      authorId: 'tester',
      createdAt: '2026-08-23T00:00-switch-fixture',
      isAgent: false,
      testMarker: true,
    },
  })) as unknown as UseYjsTreeResult['nodes'];

  return {
    nodes,
    edges: [],
    treeTitle: 'Synthetic Threshold Tree',
    isReady: true,
    multiParentNodes: new Set(),
    refresh: () => {},
  };
}

// ─── Harness ──────────────────────────────────────────────────────────

let container: HTMLDivElement;
let root: Root;

beforeEach(() => vi.clearAllMocks());

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

// ─── The switch ───────────────────────────────────────────────────────

describe('TreeCanvas renderer switch', () => {
  it('keeps trees at or below the threshold on the React Flow path', () => {
    act(() => {
      root.render(createElement(TreeCanvas, { tree: fakeTree(CANVAS_THRESHOLD) }));
    });
    expect(flowView).toHaveBeenCalled();
    expect(canvasView).not.toHaveBeenCalled();
  });

  it('sends trees strictly above the threshold to the canvas', () => {
    act(() => {
      root.render(
        createElement(TreeCanvas, { tree: fakeTree(CANVAS_THRESHOLD + 1) }),
      );
    });
    expect(canvasView).toHaveBeenCalled();
    expect(flowView).not.toHaveBeenCalled();
  });

  it('passes the tree through to whichever renderer is active', () => {
    act(() => {
      root.render(
        createElement(TreeCanvas, { tree: fakeTree(CANVAS_THRESHOLD + 1) }),
      );
    });
    const props = canvasView.mock.calls[0]?.[0] as TreeCanvasPropsStub;
    expect(props.tree.treeTitle).toBe('Synthetic Threshold Tree');
    expect(props.tree.nodes).toHaveLength(CANVAS_THRESHOLD + 1);
  });
});
