/**
 * Hermes Canopy — native media viewer pure logic (SPEC-PL-02 §9.7).
 *
 * Every function is self-contained because mediaViewerBody.ts serializes the
 * tested functions with Function.toString for execution inside ViewerHost's
 * sandboxed iframe.
 */

export type MediaKind = 'audio' | 'video';

/** Infer the native element kind from MIME only; unsupported values stay unsupported. */
export function mediaKindForMime(rawMime: unknown): MediaKind | null {
  if (typeof rawMime !== 'string') return null;
  const mime = rawMime.trim().toLowerCase().split(';', 1)[0].trim();
  if (mime.startsWith('audio/')) return 'audio';
  if (mime.startsWith('video/')) return 'video';
  return null;
}

/** Format a finite non-negative second count as m:ss or h:mm:ss. */
export function formatMediaTime(rawSeconds: unknown): string {
  const seconds = typeof rawSeconds === 'number' && Number.isFinite(rawSeconds) && rawSeconds > 0
    ? Math.floor(rawSeconds)
    : 0;
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const remainder = seconds % 60;
  if (hours > 0) {
    return hours + ':' + String(minutes).padStart(2, '0') + ':' + String(remainder).padStart(2, '0');
  }
  return minutes + ':' + String(remainder).padStart(2, '0');
}

/** A duration is usable only when it is positive and finite. */
export function finiteMediaDuration(rawDuration: unknown): number {
  return typeof rawDuration === 'number' && Number.isFinite(rawDuration) && rawDuration > 0 ? rawDuration : 0;
}

/** Clamp displayed progress into a known finite duration. */
export function clampMediaProgress(rawCurrentTime: unknown, rawDuration: unknown): number {
  const duration =
    typeof rawDuration === 'number' && Number.isFinite(rawDuration) && rawDuration > 0 ? rawDuration : 0;
  if (duration === 0 || typeof rawCurrentTime !== 'number' || !Number.isFinite(rawCurrentTime)) return 0;
  return Math.min(duration, Math.max(0, rawCurrentTime));
}

/** Clamp a requested seek target; unknown duration makes seeking a bounded no-op at zero. */
export function clampSeekTarget(rawTarget: unknown, rawDuration: unknown): number {
  const duration =
    typeof rawDuration === 'number' && Number.isFinite(rawDuration) && rawDuration > 0 ? rawDuration : 0;
  if (duration === 0 || typeof rawTarget !== 'number' || !Number.isFinite(rawTarget)) return 0;
  return Math.min(duration, Math.max(0, rawTarget));
}

/** Normalize volume into [0, 1], using a separately bounded fallback for invalid input. */
export function clampVolume(rawVolume: unknown, fallback = 1): number {
  const boundedFallback =
    typeof fallback === 'number' && Number.isFinite(fallback) ? Math.min(1, Math.max(0, fallback)) : 1;
  if (typeof rawVolume !== 'number' || !Number.isFinite(rawVolume)) return boundedFallback;
  return Math.min(1, Math.max(0, rawVolume));
}

/** Normalize playback rate into the spec range [0.25, 2]. */
export function normalizePlaybackRate(rawRate: unknown, fallback = 1): number {
  const boundedFallback =
    typeof fallback === 'number' && Number.isFinite(fallback) ? Math.min(2, Math.max(0.25, fallback)) : 1;
  if (typeof rawRate !== 'number' || !Number.isFinite(rawRate)) return boundedFallback;
  return Math.min(2, Math.max(0.25, rawRate));
}

/** Map the §9.7 digit shortcut to a duration fraction (0 → 0%, 9 → 90%). */
export function keyboardJumpPercentage(key: unknown): number | null {
  if (typeof key !== 'string' || key.length !== 1 || key < '0' || key > '9') return null;
  return Number(key) / 10;
}
