/**
 * Hermes Canopy — Accessibility Utilities
 *
 * Shared helpers for WCAG 2.1 AA compliance.
 * - ARIA live region announcements
 * - Screen reader testing helpers
 */

/**
 * Announce a message to screen readers via the hidden
 * aria-live region in the main layout.
 */
export function announceToScreenReader(message: string): void {
  const el = document.getElementById('aria-live-announcer');
  if (el) {
    // Clear and reset to re-trigger announcement for repeated messages
    el.textContent = '';
    // Use requestAnimationFrame to ensure the clear is processed
    requestAnimationFrame(() => {
      el.textContent = message;
    });
  }
}

/**
 * Generate a unique ID for use with aria-labelledby / aria-describedby.
 */
let _idCounter = 0;
export function useAccessibleId(prefix: string): string {
  return `${prefix}-${++_idCounter}-${Math.random().toString(36).slice(2, 7)}`;
}
