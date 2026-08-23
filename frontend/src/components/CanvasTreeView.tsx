/**
 * Hermes Canopy — Canvas Tree View (Canvas 2D fallback, STACK-03)
 *
 * specs/ARCHITECTURE.md §2.2: trees beyond CANVAS_THRESHOLD nodes are
 * painted here instead of through React Flow's DOM renderer. The scene
 * is the SAME layout snapshot `TreeCanvas` consumes (`useYjsTree` →
 * `computeD3Layout` positions), so geometry matches by construction —
 * only the painting changes.
 *
 * Interaction surface mirrors the React Flow canvas where it matters:
 *   - initial fit-to-view, re-fit on container resize
 *   - drag to pan, wheel to zoom around the cursor
 *   - Ctrl/Cmd+0 fit, Ctrl/Cmd+= zoom in, Ctrl/Cmd+- zoom out (same
 *     chords as TreeCanvas)
 *   - click selects a node (dot hit-testing), background click clears
 *   - devicePixelRatio-aware backing store for crisp hi-dpi rendering
 *
 * Deliberately NOT ported: ghost reply slots, collapse chevrons,
 * minimap, collaborative cursors — at 2000+ nodes the overview shape of
 * the tree is the deliverable (the React Flow path keeps those).
 */

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type WheelEvent as ReactWheelEvent,
} from 'react';
import type { UseYjsTreeResult } from '../stores/useYjsTree.ts';
import { palette } from '../theme.ts';
import {
  CANVAS_THRESHOLD,
  FIT_MAX_SCALE,
  FIT_PADDING,
  applyPan,
  computeFitTransform,
  drawScene,
  hitTestNodes,
  shouldUseCanvas,
  toCanvasEdges,
  toCanvasNodes,
  zoomAtPoint,
  type ViewTransform,
} from '../lib/canvasRenderer.ts';

/** Wheel steps multiply the scale by this factor per ~100px of delta. */
const WHEEL_ZOOM_FACTOR = 1.15;
/** Pointer travel (px) beyond which a press counts as a pan, not a click. */
const DRAG_SLOP = 3;

export interface CanvasTreeViewProps {
  tree: UseYjsTreeResult;
  /** Click selection — same contract as TreeCanvas. */
  onSelectionChange?: (nodeId: string | null) => void;
}

// ─── Component ────────────────────────────────────────────────────────

/**
 * Self-contained fallback renderer. Exported unwrapped (no provider
 * needed — that's a React Flow concern); `TreeCanvas` swaps it in above
 * CANVAS_THRESHOLD.
 */
export default function CanvasTreeView({
  tree,
  onSelectionChange,
}: CanvasTreeViewProps) {
  const { nodes: rfNodes, edges: rfEdges, treeTitle } = tree;
  const nodeCount = rfNodes.length;

  const containerRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);

  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);
  const [transform, setTransform] = useState<ViewTransform>(() => ({
    scale: 1,
    offsetX: 0,
    offsetY: 0,
  }));
  const transformRef = useRef(transform);
  transformRef.current = transform;

  const sceneNodes = useMemo(() => toCanvasNodes(rfNodes), [rfNodes]);
  const sceneEdges = useMemo(() => toCanvasEdges(rfEdges), [rfEdges]);
  const sceneRef = useRef({ nodes: sceneNodes, edges: sceneEdges });
  sceneRef.current = { nodes: sceneNodes, edges: sceneEdges };

  // ─── Fit-to-view ─────────────────────────────────────────────────

  const fitToView = useCallback(() => {
    const el = containerRef.current;
    if (!el) return;
    setTransform(
      computeFitTransform(
        sceneRef.current.nodes,
        el.clientWidth,
        el.clientHeight,
        FIT_PADDING,
        FIT_MAX_SCALE,
      ),
    );
  }, []);

  /** Fit once mounted, then again whenever the container resizes. */
  useEffect(() => {
    fitToView();
    const el = containerRef.current;
    if (!el || typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(() => fitToView());
    observer.observe(el);
    return () => observer.disconnect();
  }, [fitToView]);

  // ─── Painting ────────────────────────────────────────────────────

  useEffect(() => {
    const canvas = canvasRef.current;
    const container = containerRef.current;
    if (!canvas || !container) return;

    const width = container.clientWidth;
    const height = container.clientHeight;
    if (width <= 0 || height <= 0) return;

    // Hi-dpi: size the backing store in device pixels, paint in CSS px.
    const dpr = window.devicePixelRatio || 1;
    if (
      canvas.width !== Math.round(width * dpr) ||
      canvas.height !== Math.round(height * dpr)
    ) {
      canvas.width = Math.round(width * dpr);
      canvas.height = Math.round(height * dpr);
      canvas.style.width = `${width}px`;
      canvas.style.height = `${height}px`;
    }

    const ctx = canvas.getContext('2d');
    if (!ctx) return;

    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.fillStyle = palette.surfaceBase;
    ctx.fillRect(0, 0, width, height);

    drawScene(ctx, sceneNodes, sceneEdges, transform, { selectedNodeId });
  }, [sceneNodes, sceneEdges, transform, selectedNodeId]);

  // ─── Pan / zoom interactions ──────────────────────────────────────

  /**
   * Live pan gesture. `dragged` latches once travel exceeds DRAG_SLOP so
   * the synthetic click that follows pointerup can be suppressed —
   * pointerup fires before click, so checking pan state there is too
   * late.
   */
  const panState = useRef<{
    pointerId: number;
    lastX: number;
    lastY: number;
    dragged: boolean;
  } | null>(null);

  const handlePointerDown = useCallback(
    (e: React.PointerEvent<HTMLDivElement>) => {
      if (e.pointerType === 'mouse' && e.button !== 0) return;
      panState.current = {
        pointerId: e.pointerId,
        lastX: e.clientX,
        lastY: e.clientY,
        dragged: false,
      };
      e.currentTarget.setPointerCapture(e.pointerId);
    },
    [],
  );

  const handlePointerMove = useCallback((e: React.PointerEvent<HTMLDivElement>) => {
    const st = panState.current;
    if (!st || st.pointerId !== e.pointerId) return;
    const dx = e.clientX - st.lastX;
    const dy = e.clientY - st.lastY;
    if (dx === 0 && dy === 0) return;
    st.lastX = e.clientX;
    st.lastY = e.clientY;
    if (Math.abs(dx) > DRAG_SLOP || Math.abs(dy) > DRAG_SLOP) st.dragged = true;
    setTransform((t) => applyPan(t, dx, dy));
  }, []);

  const endPan = useCallback((e: React.PointerEvent<HTMLDivElement>) => {
    if (panState.current?.pointerId !== e.pointerId) return;
    panState.current = null;
    e.currentTarget.releasePointerCapture?.(e.pointerId);
  }, []);

  /**
   * Wheel zoom anchored at the cursor. `deltaMode` 1+ (line/page units,
   * Firefox) is normalized to pixels first.
   */
  const handleWheel = useCallback((e: ReactWheelEvent<HTMLDivElement>) => {
    e.preventDefault();
    const bounds = e.currentTarget.getBoundingClientRect();
    const px = e.clientX - bounds.left;
    const py = e.clientY - bounds.top;
    const delta =
      e.deltaMode === 1
        ? e.deltaY * 16
        : e.deltaMode === 2
          ? e.deltaY * 100
          : e.deltaY;
    const magnitude = Math.min(Math.abs(delta) / 100, 3);
    const factor = Math.pow(WHEEL_ZOOM_FACTOR, -Math.sign(delta) * magnitude);
    setTransform((t) => zoomAtPoint(t, factor, px, py));
  }, []);

  // ─── Selection ────────────────────────────────────────────────────

  const handleClick = useCallback(
    (e: React.MouseEvent<HTMLDivElement>) => {
      // A pan that ends over a dot must not select it.
      if (panState.current?.dragged) return;
      const bounds = e.currentTarget.getBoundingClientRect();
      const hit = hitTestNodes(
        sceneRef.current.nodes,
        transformRef.current,
        e.clientX - bounds.left,
        e.clientY - bounds.top,
      );
      setSelectedNodeId(hit);
      onSelectionChange?.(hit);
    },
    [onSelectionChange],
  );

  /** Double-click anywhere re-fits — matches React Flow's discoverability. */
  const handleDoubleClick = useCallback(() => {
    fitToView();
  }, [fitToView]);

  // ─── Keyboard shortcuts (same chords as TreeCanvas) ───────────────

  useEffect(() => {
    function zoomAtCenter(t: ViewTransform, factor: number): ViewTransform {
      const el = containerRef.current;
      const cx = (el?.clientWidth ?? 0) / 2;
      const cy = (el?.clientHeight ?? 0) / 2;
      return zoomAtPoint(t, factor, cx, cy);
    }

    function handleKeyDown(e: KeyboardEvent): void {
      const mod = e.ctrlKey || e.metaKey;
      if (!mod) return;
      if (e.key === '0') {
        e.preventDefault();
        fitToView();
      } else if (e.key === '=' || e.key === '+') {
        e.preventDefault();
        setTransform((t) => zoomAtCenter(t, 1.25));
      } else if (e.key === '-') {
        e.preventDefault();
        setTransform((t) => zoomAtCenter(t, 0.8));
      }
    }
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [fitToView]);

  // ─── Render ────────────────────────────────────────────────────────

  return (
    <div
      ref={containerRef}
      className="h-full w-full relative bg-surface-base touch-none"
      role="application"
      aria-label={`Tree canvas (performance mode): ${treeTitle || 'Untitled'} — ${nodeCount} nodes`}
      aria-roledescription="Interactive tree visualization"
      onPointerDown={handlePointerDown}
      onPointerMove={handlePointerMove}
      onPointerUp={endPan}
      onPointerCancel={endPan}
      onWheel={handleWheel}
      onClick={handleClick}
      onDoubleClick={handleDoubleClick}
      style={{ cursor: 'grab' }}
    >
      <canvas ref={canvasRef} className="block" />

      {/* Performance-mode banner — mirrors TreeCanvas' large-tree notice */}
      {shouldUseCanvas(nodeCount) && (
        <div className="glass absolute top-2 left-1/2 -translate-x-1/2 z-10 rounded-lg px-4 py-1.5 text-sm text-status-warning ring-1 ring-inset ring-amber-400/30">
          ⚠️ Large tree ({nodeCount} nodes) — performance mode active
          (canvas renderer, threshold {CANVAS_THRESHOLD})
        </div>
      )}
    </div>
  );
}
