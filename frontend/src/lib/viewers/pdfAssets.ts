/**
 * Hermes Canopy — pdf.js host-asset bridge (SPEC-PL-02 §9.1, phase 9).
 *
 * The pdf.js v4.x module and worker ship from the LOCKED `pdfjs-dist` npm
 * dependency. vite.config.ts copies `build/pdf.min.mjs` and
 * `build/pdf.worker.min.mjs` from node_modules into `public/pdfjs/` at
 * every dev/build start (public/ is copied verbatim into dist by Vite), so
 * these stable same-origin relative URLs are real local build outputs:
 * no CDN, no network dependency, no vendored blob committed to the repo
 * (public/pdfjs/ is gitignored; the bytes always come from node_modules).
 *
 * `?url` imports were rejected deliberately: Vite lib-mode builds inline
 * them as `data:` URIs, which the sandbox bootstrap must never carry.
 *
 * These URLs are injected ONLY into the pdf slug's §8.3 bootstrap
 * (`canopy.__bootstrap.pdfjs`) by ViewerHost; the sandboxed body loads the
 * module from that same-origin URL and points GlobalWorkerOptions.workerSrc
 * at the worker asset. The viewer CSP gains exactly one worker rule for
 * this (VIEWER_SANDBOX_CSP in ViewerHost.tsx).
 *
 * Intentionally eager: SPEC-PL-02 §9.1 specifies the pdf.js foundation in
 * the main chunk for the built-in viewer; splitting it into a lazy chunk is
 * a later optimization and is NOT part of this phase. cmaps/ and
 * standard_fonts/ copies are deferred to the text-layer phase that needs
 * them.
 */

export interface PdfjsAssets {
  moduleUrl: string;
  workerUrl: string;
}

/** Same-origin pdf.js module + worker asset URLs (emitted by the Vite build). */
export const PDFJS_ASSETS: PdfjsAssets = {
  moduleUrl: '/pdfjs/pdf.min.mjs',
  workerUrl: '/pdfjs/pdf.worker.min.mjs',
};

/**
 * Subset of the viewer bootstrap that carries pdf.js asset URLs. Every
 * non-pdf viewer gets `undefined` here and must never see these URLs.
 */
export interface PdfjsBootstrap {
  pdfjs?: PdfjsAssets | null;
}
