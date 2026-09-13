/**
 * Unit tests — image viewer pure logic (SPEC-PL-02 §9.2, phase 3).
 * These exact functions are serialized into the in-iframe body script
 * (imageViewerBody.ts), so their behavior pins the shipped viewer math.
 */

import { describe, it, expect } from 'vitest';
import {
  clampPan,
  clampSensitivity,
  clampZoom,
  computeFitZoom,
  keyboardZoomMultiplier,
  normalizeRotation,
  wheelZoomMultiplier,
} from '../imageViewerLogic';

describe('clampSensitivity', () => {
  it('clamps below 0.5 up to 0.5 and above 3.0 down to 3.0 (spec range)', () => {
    expect(clampSensitivity(0.1)).toBe(0.5);
    expect(clampSensitivity(5)).toBe(3.0);
  });

  it('passes in-range values through', () => {
    expect(clampSensitivity(1.7)).toBe(1.7);
    expect(clampSensitivity(0.5)).toBe(0.5);
    expect(clampSensitivity(3.0)).toBe(3.0);
  });

  it('falls back to 1.0 on garbage (undefined / NaN / non-numeric)', () => {
    expect(clampSensitivity(undefined)).toBe(1.0);
    expect(clampSensitivity(Number.NaN)).toBe(1.0);
    expect(clampSensitivity('loud')).toBe(1.0);
    expect(clampSensitivity(null)).toBe(1.0);
  });
});

describe('wheelZoomMultiplier', () => {
  it('zooms out on positive deltaY and in on negative deltaY', () => {
    expect(wheelZoomMultiplier(100, 1)).toBeCloseTo(Math.exp(-0.25), 12);
    expect(wheelZoomMultiplier(-100, 1)).toBeCloseTo(Math.exp(0.25), 12);
  });

  it('scales with sensitivity', () => {
    const slow = wheelZoomMultiplier(100, 0.5);
    const fast = wheelZoomMultiplier(100, 3.0);
    expect(fast).toBeLessThan(slow);
    expect(slow).toBeLessThan(1);
    expect(wheelZoomMultiplier(100, 0)).toBe(1);
  });
});

describe('keyboardZoomMultiplier', () => {
  it('steps 15% per press at sensitivity 1', () => {
    expect(keyboardZoomMultiplier(1, 1)).toBeCloseTo(1.15, 12);
    expect(keyboardZoomMultiplier(-1, 1)).toBeCloseTo(0.85, 12);
  });

  it('amplifies with sensitivity (+ at 3.0 → 1.45)', () => {
    expect(keyboardZoomMultiplier(1, 3.0)).toBeCloseTo(1.45, 12);
    expect(keyboardZoomMultiplier(-1, 0.5)).toBeCloseTo(0.925, 12);
  });
});

describe('clampZoom', () => {
  it('clamps into [0.05, 40]', () => {
    expect(clampZoom(0.001)).toBe(0.05);
    expect(clampZoom(100)).toBe(40);
    expect(clampZoom(2)).toBe(2);
  });

  it('rescues non-finite zoom to 1', () => {
    expect(clampZoom(Number.NaN)).toBe(1);
    expect(clampZoom(Number.POSITIVE_INFINITY)).toBe(40);
  });
});

describe('computeFitZoom', () => {
  it('scales a large image down to contain (never above 1)', () => {
    expect(computeFitZoom(800, 600, 1600, 1200)).toBeCloseTo(0.5, 12);
    expect(computeFitZoom(800, 600, 1600, 900)).toBeCloseTo(0.5, 12); // width-bound
    expect(computeFitZoom(800, 600, 400, 1200)).toBeCloseTo(0.5, 12); // height-bound
  });

  it('caps small images at 1× (fit never blows up)', () => {
    expect(computeFitZoom(800, 600, 100, 100)).toBe(1);
  });

  it('degrades to 1 on zero/unknown dimensions', () => {
    expect(computeFitZoom(800, 600, 0, 0)).toBe(1);
    expect(computeFitZoom(0, 0, 100, 100)).toBe(1);
  });
});

describe('normalizeRotation', () => {
  it('keeps 90° increments and wraps into [0, 360)', () => {
    expect(normalizeRotation(90)).toBe(90);
    expect(normalizeRotation(360)).toBe(0);
    expect(normalizeRotation(-90)).toBe(270);
    expect(normalizeRotation(450)).toBe(90);
    expect(normalizeRotation(-360)).toBe(0);
  });
});

describe('clampPan', () => {
  it('bounds translation and rescues non-finite to 0', () => {
    expect(clampPan(25000)).toBe(20000);
    expect(clampPan(-25000)).toBe(-20000);
    expect(clampPan(Number.NaN)).toBe(0);
    expect(clampPan(3)).toBe(3);
  });

  it('honors an explicit limit', () => {
    expect(clampPan(50, 25)).toBe(25);
  });
});
