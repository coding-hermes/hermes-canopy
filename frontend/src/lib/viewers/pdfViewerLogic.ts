/**
 * Hermes Canopy — PDF viewer logic (SPEC-PL-02 §9.1, phase 9 pdf.js host
 * bundle).
 *
 * The sandboxed viewer body cannot import npm packages, so the pure helpers
 * here are serialized (Function.toString) into pdfViewerBody.ts and executed
 * inside ViewerHost's sandboxed iframe. Every helper is therefore
 * self-contained: it may not reference module-scope identifiers.
 *
 * Phase 9 makes locally-bundled pdf.js v4.x the primary render path: the
 * HOST resolves same-origin pdf.js module/worker asset URLs (pdfAssets.ts)
 * and injects them through the §8.3 bootstrap; the body initializes pdf.js
 * from those URLs, loads the signed stream URL in a Range-capable way, and
 * renders the active page to a canvas.
 *
 * Phase-scope deferrals from §9.1 (named here so later phases inherit an
 * accurate map): the invisible text layer / text selection, pdf_text_selected
 * events, the 50-page render LRU cache, continuous-scroll virtualization,
 * single-page-mode toggle (`p`), and find-in-document (Ctrl/Cmd+F). These are
 * NOT implemented in this phase; pdf.js is loaded but only the single
 * active-page canvas is rendered.
 */

export interface PdfViewerFileMeta {
  id?: string;
  filename?: string;
  mimeType?: string;
  byteSize?: number;
}

export interface PdfViewerConfig {
  initialPage: number;
  initialZoom: number | 'fit-width';
  pdfSinglePageMode: boolean;
  textLayer: boolean;
  annotations: boolean;
}

export type PdfRenderMode = 'pdfjs' | 'fallback';

export type PdfRenderReason =
  | 'PDFJS_ASSETS_READY'
  | 'PDFJS_ASSETS_MISSING'
  | 'STREAM_URL_MISSING'
  | 'NOT_A_PDF_FILE';

export interface PdfRenderPlan {
  mode: PdfRenderMode;
  reason: PdfRenderReason;
}

export interface PdfViewerState {
  filename: string;
  mimeType: string;
  byteSize: number; // -1 when the byte size is unknown
  displaySize: string;
  hasStreamUrl: boolean;
}

// ── pdf.js structural seams ─────────────────────────────────
//
// Minimal structural types for the surface this phase drives. The real
// pdf.js namespace satisfies these; tests satisfy them with fakes so no
// browser worker is ever required in jsdom. The production module loader
// lives as a string literal in pdfViewerBody.ts so bundler transforms never
// rewrite its dynamic import.

// ── §9.1 pure helpers (each serialized into the body) ───────

/**
 * A file counts as a PDF when its MIME (case-insensitive, parameters
 * stripped) is application/pdf or its filename extension is .pdf.
 */
export function isPdfFile(fileMeta: PdfViewerFileMeta | undefined): boolean {
  const meta = fileMeta || {};
  const mime =
    typeof meta.mimeType === 'string' ? meta.mimeType.trim().toLowerCase().split(';', 1)[0].trim() : '';
  if (mime === 'application/pdf') return true;
  const filename = typeof meta.filename === 'string' ? meta.filename.trim().toLowerCase() : '';
  const match = /\.([a-z0-9]+)$/.exec(filename);
  return match !== null && match[1] === 'pdf';
}

/** Format a byte count as B / KB / MB / GB / TB (1024-based, one decimal). */
export function formatByteSize(rawSize: unknown): string {
  if (typeof rawSize !== 'number' || !Number.isFinite(rawSize) || rawSize < 0) return '0 B';
  const size = Math.floor(rawSize);
  if (size < 1024) return size + ' B';
  const units = ['KB', 'MB', 'GB', 'TB'];
  let value = size;
  let unitIndex = -1;
  do {
    value /= 1024;
    unitIndex += 1;
  } while (value >= 1024 && unitIndex < units.length - 1);
  const rounded = Math.round(value * 10) / 10;
  return rounded + ' ' + units[unitIndex];
}

/** Normalize the §9.1 config keys with the spec defaults; invalid input falls back. */
export function normalizePdfConfig(config: Record<string, unknown> | undefined): PdfViewerConfig {
  const raw = config || {};
  const rawPage = Number(raw.initialPage);
  const initialPage = Number.isFinite(rawPage) && rawPage >= 1 ? Math.floor(rawPage) : 1;
  let initialZoom: number | 'fit-width' = 'fit-width';
  if (typeof raw.initialZoom === 'number' && Number.isFinite(raw.initialZoom)) {
    initialZoom = Math.min(3, Math.max(0.5, raw.initialZoom));
  }
  return {
    initialPage,
    initialZoom,
    pdfSinglePageMode: raw.pdfSinglePageMode === true,
    textLayer: raw.textLayer !== false,
    annotations: raw.annotations !== false,
  };
}

/** Derive the metadata surface shared by the status bar and the fallback card. */
export function buildPdfViewerState(
  fileMeta: PdfViewerFileMeta | undefined,
  streamUrl: unknown,
): PdfViewerState {
  const meta = fileMeta || {};
  const filename =
    typeof meta.filename === 'string' && meta.filename.trim() ? meta.filename.trim() : 'Untitled';
  const mimeType = typeof meta.mimeType === 'string' ? meta.mimeType.trim() : '';
  const rawSize = meta.byteSize;
  let byteSize = -1;
  let displaySize = 'size unknown';
  // Self-contained copy of the formatByteSize contract: the serialized body
  // source cannot reference module-scope helpers.
  if (typeof rawSize === 'number' && Number.isFinite(rawSize) && rawSize >= 0) {
    byteSize = Math.floor(rawSize);
    if (byteSize < 1024) {
      displaySize = byteSize + ' B';
    } else {
      const units = ['KB', 'MB', 'GB', 'TB'];
      let value = byteSize;
      let unitIndex = -1;
      do {
        value /= 1024;
        unitIndex += 1;
      } while (value >= 1024 && unitIndex < units.length - 1);
      displaySize = Math.round(value * 10) / 10 + ' ' + units[unitIndex];
    }
  }
  return {
    filename,
    mimeType,
    byteSize,
    displaySize,
    hasStreamUrl: typeof streamUrl === 'string' && streamUrl.length > 0,
  };
}

/**
 * Read the host-injected pdf.js asset URLs out of the §8.3 bootstrap.
 * Returns null unless BOTH URLs are non-empty strings resolving to the
 * frame's own origin (relative URLs resolve against window.location.origin —
 * the srcDoc frame shares the parent origin). A partially-populated or
 * hostile bootstrap (remote origin, non-http scheme) is treated as absent so
 * the body falls back honestly instead of fetching a cross-origin script.
 * Data: URLs are rejected by design.
 */
export function readPdfjsAssets(bootstrap: {
  pdfjs?: { moduleUrl?: unknown; workerUrl?: unknown } | null;
} | undefined): { moduleUrl: string; workerUrl: string } | null {
  const raw = bootstrap && bootstrap.pdfjs;
  if (!raw || typeof raw !== 'object') return null;
  const moduleUrl = typeof raw.moduleUrl === 'string' ? raw.moduleUrl : '';
  const workerUrl = typeof raw.workerUrl === 'string' ? raw.workerUrl : '';
  if (!moduleUrl || !workerUrl) return null;
  const origin =
    typeof window !== 'undefined' && window.location && window.location.origin
      ? window.location.origin
      : 'https://canopy.invalid';
  for (const url of [moduleUrl, workerUrl]) {
    let parsed: URL;
    try {
      parsed = new URL(url, origin);
    } catch {
      return null;
    }
    if (parsed.protocol !== 'https:' && parsed.protocol !== 'http:') return null;
    if (parsed.origin !== origin) return null;
  }
  return { moduleUrl: moduleUrl, workerUrl: workerUrl };
}

/**
 * Map a pdf.js failure to the §9.1 pdf_error code set this phase uses.
 * pdf.js raises typed exceptions (name field); everything unknown collapses
 * to the generic load error.
 */
export function classifyPdfjsError(error: unknown): string {
  const name = error && typeof error === 'object' ? String((error as { name?: unknown }).name) : '';
  if (name === 'MissingPDFException') return 'PDF_MISSING';
  if (name === 'InvalidPDFException') return 'PDF_INVALID';
  if (name === 'UnexpectedResponseException') return 'PDF_RESPONSE_ERROR';
  return 'PDFJS_LOAD_ERROR';
}

/** Zoom bounds from §9.1: 0.5–3.0; non-finite input resets to 1. */
export function clampPdfZoom(zoom: unknown): number {
  const value = typeof zoom === 'number' ? zoom : Number(zoom);
  if (!Number.isFinite(value)) return 1;
  return Math.min(3, Math.max(0.5, value));
}

/**
 * One zoom-in/out step from the current scale, clamped to the §9.1 bounds
 * (0.5–3.0, ×1.25 step). Self-contained by serialization contract: the
 * ×1.25 factor and the clamp are inlined because the serialized body source
 * cannot reference module-scope identifiers.
 */
export function stepPdfZoom(scale: number, direction: number): number {
  const factor = direction >= 0 ? 1.25 : 1 / 1.25;
  const value = scale * factor;
  if (!Number.isFinite(value)) return 1;
  return Math.min(3, Math.max(0.5, value));
}

/**
 * Resolve the render scale for a page: explicit numeric zoom passes through
 * the 0.5–3.0 clamp; 'fit-width' divides the container width by the page's
 * scale-1 width. Degenerate measurements (jsdom reports 0) fall back to
 * scale 1 instead of producing a zero-size canvas. Self-contained by
 * serialization contract: the clamp is inlined because the serialized body
 * source cannot reference the module-scope clampPdfZoom helper.
 */
export function resolvePdfScale(
  zoom: number | 'fit-width',
  basePageWidth: number,
  containerWidth: number,
): number {
  if (zoom !== 'fit-width') {
    const value = typeof zoom === 'number' ? zoom : Number(zoom);
    if (!Number.isFinite(value)) return 1;
    return Math.min(3, Math.max(0.5, value));
  }
  if (!(basePageWidth > 0) || !(containerWidth > 0)) return 1;
  const fit = containerWidth / basePageWidth;
  if (!Number.isFinite(fit)) return 1;
  return Math.min(3, Math.max(0.5, fit));
}

/** Clamp a 1-indexed page request into [1, totalPages]; degenerate input → 1. */
export function clampPdfPage(page: unknown, totalPages: number): number {
  const value = typeof page === 'number' ? page : Number(page);
  if (!Number.isFinite(value) || totalPages < 1) return 1;
  return Math.min(totalPages, Math.max(1, Math.floor(value)));
}

/**
 * Decide the phase-9 render path before touching the DOM: the pdf.js canvas
 * path requires the file to be a PDF, a stream URL, and host-injected local
 * pdf.js assets; anything else gets the honest fallback. Keeping the decision
 * pure makes the body's branch unit-testable without a real browser worker.
 */
export function buildPdfRenderPlan(options: {
  fileMeta?: PdfViewerFileMeta;
  streamUrl?: unknown;
  assetsAvailable?: boolean;
}): PdfRenderPlan {
  const opts = options || {};
  const meta = opts.fileMeta || {};
  // Self-contained isPdfFile: the serialized body source cannot reference
  // module-scope helpers.
  const mime =
    typeof meta.mimeType === 'string' ? meta.mimeType.trim().toLowerCase().split(';', 1)[0].trim() : '';
  const filename = typeof meta.filename === 'string' ? meta.filename.trim().toLowerCase() : '';
  const extensionMatch = /\.([a-z0-9]+)$/.exec(filename);
  const isPdf = mime === 'application/pdf' || (extensionMatch !== null && extensionMatch[1] === 'pdf');
  if (!isPdf) return { mode: 'fallback', reason: 'NOT_A_PDF_FILE' };
  if (typeof opts.streamUrl !== 'string' || opts.streamUrl.length === 0) {
    return { mode: 'fallback', reason: 'STREAM_URL_MISSING' };
  }
  if (opts.assetsAvailable !== true) return { mode: 'fallback', reason: 'PDFJS_ASSETS_MISSING' };
  return { mode: 'pdfjs', reason: 'PDFJS_ASSETS_READY' };
}
