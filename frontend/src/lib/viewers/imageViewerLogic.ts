/**
 * Hermes Canopy — image viewer pure logic (SPEC-PL-02 §9.2, phase 3).
 *
 * Unit-testable math consumed by imageViewerBody.ts (the in-iframe shell).
 * Every function here is self-contained (no module-level references) so the
 * body can serialize them with Function.prototype.toString and run the SAME
 * tested code inside the sandboxed frame.
 *
 * Zoom model: a scale factor applied to the <img> via CSS transform.
 *   - keyboard steps: 1 + 0.15 × sensitivity per press
 *   - wheel steps:    e^(−deltaY / 400 × sensitivity)
 *   - clamp: [0.05, 40]
 * Rotation: cumulative degrees normalized to [0, 360).
 * Fit: contain-scaling against the viewport, never above 1× for "fit".
 */

export const ZOOM_MIN = 0.05;
export const ZOOM_MAX = 40;

/** Config sensitivity clamp (spec: 0.5–3.0; non-number → default 1.0). */
export function clampSensitivity(raw: unknown, fallback = 1.0): number {
  if (typeof raw !== 'number' || !Number.isFinite(raw)) return fallback;
  return Math.min(3.0, Math.max(0.5, raw));
}

/** One wheel tick's multiplier for a deltaY, sensitivity-scaled. */
export function wheelZoomMultiplier(deltaY: number, sensitivity: number): number {
  return Math.exp((-deltaY / 400) * sensitivity);
}

/** One keyboard +/- step multiplier, sensitivity-scaled. */
export function keyboardZoomMultiplier(direction: 1 | -1, sensitivity: number): number {
  return 1 + direction * 0.15 * sensitivity;
}

/** Clamp a zoom level into [0.05, 40]; NaN rescues to 1. (Literals inlined: serialization-safe.) */
export function clampZoom(zoom: number): number {
  if (Number.isNaN(zoom)) return 1;
  return Math.min(40, Math.max(0.05, zoom));
}

/** Contain-fit scale for an image in a viewport; capped at 1× (never blow up small images for "fit"). */
export function computeFitZoom(
  viewportWidth: number,
  viewportHeight: number,
  imageWidth: number,
  imageHeight: number,
): number {
  if (imageWidth <= 0 || imageHeight <= 0 || viewportWidth <= 0 || viewportHeight <= 0) return 1;
  return Math.min(1, viewportWidth / imageWidth, viewportHeight / imageHeight);
}

/** Cumulative rotation normalized into [0, 360). */
export function normalizeRotation(degrees: number): number {
  return ((degrees % 360) + 360) % 360;
}

/** Pan clamp: keeps translate within ±limit px; NaN/±∞ collapse to 0. */
export function clampPan(value: number, limit?: number): number {
  const max = typeof limit === 'number' ? limit : 20000;
  if (!Number.isFinite(value)) return 0;
  return Math.min(max, Math.max(-max, value));
}
