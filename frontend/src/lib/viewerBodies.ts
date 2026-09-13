/**
 * Hermes Canopy — built-in viewer body registry (SPEC-PL-02 phase 3).
 *
 * Maps a registered built-in viewer slug to the JS body source that
 * ViewerHost.buildViewerDoc injects into the §8.2 sandboxed srcDoc as the
 * SECOND <script> (after the §8.3 shim). Phase 3 ships the first two real
 * bodies: `image` (§9.2) and `json` (§9.6 zero-dep subset). The remaining
 * built-ins (pdf, code, csv, markdown, audio_video) resolve null → shim-only
 * doc (the phase-2 behavior) until their phases land — a null here is the
 * "no body shipped for this slug" signal, not an error.
 */

import { imageViewerBody } from './viewers/imageViewerBody';
import { jsonViewerBody } from './viewers/jsonViewerBody';

/** Body sources for the built-in slugs that have shipped (phase 3: image + json). */
const BODIES: Readonly<Record<string, string>> = {
  image: imageViewerBody,
  json: jsonViewerBody,
};

/**
 * Returns the in-iframe body source for a known built-in slug, or null when
 * no body ships for this slug (unknown slug, or a built-in whose viewer
 * phase has not landed yet).
 */
export function viewerBodyForSlug(slug: string): string | null {
  return Object.prototype.hasOwnProperty.call(BODIES, slug) ? BODIES[slug] : null;
}
