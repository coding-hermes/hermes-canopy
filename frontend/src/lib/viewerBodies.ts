/**
 * Hermes Canopy — built-in viewer body registry (SPEC-PL-02 phases 3–8).
 *
 * Maps a registered built-in viewer slug to the JS body source that
 * ViewerHost.buildViewerDoc injects into the §8.2 sandboxed srcDoc as the
 * SECOND <script> (after the §8.3 shim). Phase 3 ships `image` (§9.2)
 * and `json` (§9.6 zero-dep subset); phase 4 adds `audio_video` (§9.7);
 * phase 5 adds `markdown` (§9.5 zero-dep GFM subset); phase 6 adds `csv`
 * (§9.4 zero-dep table subset); phase 7 adds `code` (§9.3 read-only,
 * dependency-free source viewer); phase 8 adds `pdf` (§9.1 zero-dep
 * subset); phase 9 upgrades `pdf` to the pdf.js v4.x host-bundle canvas
 * foundation. Every built-in slug now resolves to a shipped body — a null
 * here means an unknown slug, not a pending phase.
 */

import { imageViewerBody } from './viewers/imageViewerBody';
import { jsonViewerBody } from './viewers/jsonViewerBody';
import { markdownViewerBody } from './viewers/markdownViewerBody';
import { mediaViewerBody } from './viewers/mediaViewerBody';
import { csvViewerBody } from './viewers/csvViewerBody';
import { codeViewerBody } from './viewers/codeViewerBody';
import { pdfViewerBody } from './viewers/pdfViewerBody';

/** Body sources for the built-in slugs that have shipped. */
const BODIES: Readonly<Record<string, string>> = {
  image: imageViewerBody,
  json: jsonViewerBody,
  audio_video: mediaViewerBody,
  markdown: markdownViewerBody,
  csv: csvViewerBody,
  code: codeViewerBody,
  pdf: pdfViewerBody,
};

/**
 * Returns the in-iframe body source for a known built-in slug, or null when
 * no body ships for this slug (unknown slug only — every built-in viewer
 * phase has landed).
 */
export function viewerBodyForSlug(slug: string): string | null {
  return Object.prototype.hasOwnProperty.call(BODIES, slug) ? BODIES[slug] : null;
}
