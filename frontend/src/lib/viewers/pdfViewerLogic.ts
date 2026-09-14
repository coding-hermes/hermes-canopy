/**
 * Hermes Canopy — dependency-free PDF viewer logic (SPEC-PL-02 §9.1, phase 8).
 *
 * Zero-dependency subset of §9.1: the sandboxed viewer body cannot import npm
 * packages, so these helpers drive a native browser PDF embed plus an honest
 * download fallback instead of a pdf.js canvas. Every function is
 * self-contained because pdfViewerBody.ts serializes the tested functions
 * with Function.toString for execution inside ViewerHost's sandboxed iframe.
 *
 * Deferred from §9.1: the pdf.js v4.x bundle, canvas page bitmap rendering,
 * the invisible text layer and text selection, the 50-page render LRU,
 * Range-request progressive page loading, page navigation and zoom keyboard
 * shortcuts, and the pdf_page_visible / pdf_text_selected / pdf_zoom_changed
 * event stream. initialPage is applied via the #page= URL fragment;
 * initialZoom is normalized but not applied (native plugin zoom control is
 * out of reach for a dependency-free body).
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

export type PdfRenderMode = 'native-embed' | 'fallback';

export type PdfRenderReason =
  | 'PDF_PLUGIN_AVAILABLE'
  | 'PDF_PLUGIN_UNAVAILABLE'
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
 * Decide the zero-dep render path before touching the DOM: the native
 * browser embed only when the file, stream URL, and plugin probe all agree,
 * otherwise an honest fallback. Keeping the decision pure makes the body's
 * branch unit-testable without a real PDF plugin.
 */
export function buildPdfRenderPlan(options: {
  fileMeta?: PdfViewerFileMeta;
  streamUrl?: unknown;
  pluginAvailable?: boolean;
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
  if (opts.pluginAvailable !== true) return { mode: 'fallback', reason: 'PDF_PLUGIN_UNAVAILABLE' };
  return { mode: 'native-embed', reason: 'PDF_PLUGIN_AVAILABLE' };
}

/**
 * Apply the §9.1 initialPage through the #page=N viewer-fragment convention.
 * Fragments never reach the server, so a signed URL's query stays intact; a
 * URL that already carries a fragment, or the default page 1, is returned
 * unchanged.
 */
export function buildPdfEmbedUrl(streamUrl: unknown, config: PdfViewerConfig | undefined): string {
  if (typeof streamUrl !== 'string' || streamUrl.length === 0) return '';
  const page = config && Number.isFinite(config.initialPage) ? Math.floor(config.initialPage) : 1;
  if (page <= 1 || streamUrl.indexOf('#') !== -1) return streamUrl;
  return streamUrl + '#page=' + page;
}
