/**
 * Unit tests — native audio/video viewer pure logic (SPEC-PL-02 §9.7).
 * These exact helpers are serialized into mediaViewerBody.ts, so boundary
 * behavior here also pins the sandboxed viewer's runtime math.
 */

import { describe, expect, it } from 'vitest';
import {
  clampMediaProgress,
  clampSeekTarget,
  clampVolume,
  finiteMediaDuration,
  formatMediaTime,
  keyboardJumpPercentage,
  mediaKindForMime,
  normalizePlaybackRate,
} from '../mediaViewerLogic';

describe('mediaKindForMime', () => {
  it('maps audio/* and video/* MIME types without guessing from extensions', () => {
    expect(mediaKindForMime('audio/mpeg')).toBe('audio');
    expect(mediaKindForMime('audio/mp4; codecs="mp4a.40.2"')).toBe('audio');
    expect(mediaKindForMime('video/mp4')).toBe('video');
    expect(mediaKindForMime(' VIDEO/WEBM ')).toBe('video');
  });

  it('rejects absent and unsupported MIME values honestly', () => {
    expect(mediaKindForMime('application/octet-stream')).toBeNull();
    expect(mediaKindForMime('image/png')).toBeNull();
    expect(mediaKindForMime('')).toBeNull();
    expect(mediaKindForMime(undefined)).toBeNull();
  });
});

describe('formatMediaTime', () => {
  it('formats seconds as m:ss and long media as h:mm:ss', () => {
    expect(formatMediaTime(0)).toBe('0:00');
    expect(formatMediaTime(65.9)).toBe('1:05');
    expect(formatMediaTime(3661)).toBe('1:01:01');
  });

  it('renders negative and non-finite values as a bounded zero', () => {
    expect(formatMediaTime(-1)).toBe('0:00');
    expect(formatMediaTime(Number.NaN)).toBe('0:00');
    expect(formatMediaTime(Number.POSITIVE_INFINITY)).toBe('0:00');
  });
});

describe('finiteMediaDuration', () => {
  it('keeps positive finite durations and collapses invalid boundaries to zero', () => {
    expect(finiteMediaDuration(12.5)).toBe(12.5);
    expect(finiteMediaDuration(0)).toBe(0);
    expect(finiteMediaDuration(-5)).toBe(0);
    expect(finiteMediaDuration(Number.NaN)).toBe(0);
    expect(finiteMediaDuration(Number.POSITIVE_INFINITY)).toBe(0);
    expect(finiteMediaDuration('12')).toBe(0);
  });
});

describe('clampMediaProgress', () => {
  it('clamps current time into a finite duration', () => {
    expect(clampMediaProgress(25, 100)).toBe(25);
    expect(clampMediaProgress(-5, 100)).toBe(0);
    expect(clampMediaProgress(105, 100)).toBe(100);
  });

  it('returns zero for unknown duration or non-finite progress', () => {
    expect(clampMediaProgress(10, 0)).toBe(0);
    expect(clampMediaProgress(10, Number.NaN)).toBe(0);
    expect(clampMediaProgress(Number.NaN, 100)).toBe(0);
    expect(clampMediaProgress(Number.POSITIVE_INFINITY, 100)).toBe(0);
  });
});

describe('clampSeekTarget', () => {
  it('clamps seek targets and refuses seeking without a finite duration', () => {
    expect(clampSeekTarget(30, 120)).toBe(30);
    expect(clampSeekTarget(-1, 120)).toBe(0);
    expect(clampSeekTarget(121, 120)).toBe(120);
    expect(clampSeekTarget(30, 0)).toBe(0);
    expect(clampSeekTarget(Number.NaN, 120)).toBe(0);
    expect(clampSeekTarget(Number.POSITIVE_INFINITY, 120)).toBe(0);
  });
});

describe('clampVolume', () => {
  it('clamps finite volume into [0, 1]', () => {
    expect(clampVolume(-0.2)).toBe(0);
    expect(clampVolume(0.4)).toBe(0.4);
    expect(clampVolume(1.2)).toBe(1);
  });

  it('uses a bounded fallback for NaN, Infinity, and non-numbers', () => {
    expect(clampVolume(Number.NaN)).toBe(1);
    expect(clampVolume(Number.POSITIVE_INFINITY, 0.25)).toBe(0.25);
    expect(clampVolume('quiet', 2)).toBe(1);
  });
});

describe('normalizePlaybackRate', () => {
  it('clamps finite rates into the spec range [0.25, 2]', () => {
    expect(normalizePlaybackRate(0.1)).toBe(0.25);
    expect(normalizePlaybackRate(0.75)).toBe(0.75);
    expect(normalizePlaybackRate(3)).toBe(2);
  });

  it('uses a bounded fallback for NaN, Infinity, and non-numbers', () => {
    expect(normalizePlaybackRate(Number.NaN)).toBe(1);
    expect(normalizePlaybackRate(Number.NEGATIVE_INFINITY, 1.5)).toBe(1.5);
    expect(normalizePlaybackRate('fast', 9)).toBe(2);
  });
});

describe('keyboardJumpPercentage', () => {
  it('maps 0-9 to 0%-90% of finite duration', () => {
    expect(keyboardJumpPercentage('0')).toBe(0);
    expect(keyboardJumpPercentage('5')).toBe(0.5);
    expect(keyboardJumpPercentage('9')).toBe(0.9);
  });

  it('rejects all other key shapes', () => {
    expect(keyboardJumpPercentage('10')).toBeNull();
    expect(keyboardJumpPercentage('-1')).toBeNull();
    expect(keyboardJumpPercentage('x')).toBeNull();
    expect(keyboardJumpPercentage('')).toBeNull();
  });
});
