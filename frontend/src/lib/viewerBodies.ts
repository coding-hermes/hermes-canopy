/**
 * Hermes Canopy — built-in viewer body registry (SPEC-PL-02 phase 5).
 *
 * Maps a registered built-in viewer slug to the JS body source that
 * ViewerHost.buildViewerDoc injects into the §8.2 sandboxed srcDoc as the
 * SECOND <script> (after the §8.3 shim). Phase 3 ships `image` (§9.2)
 * and `json` (§9.6 zero-dep subset); phase 4 adds `audio_video` (§9.7);
 * phase 5 adds `markdown` (§9.5 zero-dep GFM subset). The remaining
 * built-ins (pdf, code, csv) resolve null → shim-only doc (the phase-2
 * behavior) until their phases land — a null here is the "no body shipped
 * for this slug" signal, not an error.
 */

import { imageViewerBody } from './viewers/imageViewerBody';
import { jsonViewerBody } from './viewers/jsonViewerBody';
import { markdownViewerBody } from './viewers/markdownViewerBody';
import { mediaViewerBody } from './viewers/mediaViewerBody';

/** Body sources for the built-in slugs that have shipped. */
const BODIES: Readonly<Record<string, string>> = {
  image: imageViewerBody,
  json: jsonViewerBody,
  audio_video: mediaViewerBody,
  markdown: markdownViewerBody,
};

/**
 * Returns the in-iframe body source for a known built-in slug, or null when
 * no body ships for this slug (unknown slug, or a built-in whose viewer
 * phase has not landed yet).
 */
export function viewerBodyForSlug(slug: string): string | null {
  return Object.prototype.hasOwnProperty.call(BODIES, slug) ? BODIES[slug] : null;
}
