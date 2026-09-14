/**
 * Unit tests — PDF viewer logic (SPEC-PL-02 §9.1, phase 9 pdf.js host
 * bundle). These exact helpers are serialized into pdfViewerBody.ts, so
 * boundary behavior here also pins the sandboxed viewer's render decisions.
 */

import { describe, expect, it } from 'vitest';
import {
  buildPdfRenderPlan,
  buildPdfViewerState,
  classifyPdfjsError,
  clampPdfPage,
  clampPdfZoom,
  formatByteSize,
  isPdfFile,
  normalizePdfConfig,
  readPdfjsAssets,
  resolvePdfScale,
  stepPdfZoom,
} from '../pdfViewerLogic';

describe('isPdfFile', () => {
  it('accepts the application/pdf MIME type case-insensitively, parameters stripped', () => {
    expect(isPdfFile({ mimeType: 'application/pdf' })).toBe(true);
    expect(isPdfFile({ mimeType: 'APPLICATION/PDF' })).toBe(true);
    expect(isPdfFile({ mimeType: ' application/pdf; charset=binary ' })).toBe(true);
  });

  it('accepts a .pdf extension regardless of case when the MIME disagrees', () => {
    expect(isPdfFile({ filename: 'doc.pdf', mimeType: 'application/octet-stream' })).toBe(true);
    expect(isPdfFile({ filename: 'DOC.PDF' })).toBe(true);
    expect(isPdfFile({ filename: '/a/b/contract.Pdf', mimeType: 'text/plain' })).toBe(true);
  });

  it('rejects non-PDF files, missing metadata, and bare "pdf" names', () => {
    expect(isPdfFile({ mimeType: 'image/png', filename: 'photo.png' })).toBe(false);
    expect(isPdfFile({ mimeType: 'application/octet-stream', filename: 'blob.bin' })).toBe(false);
    expect(isPdfFile({ filename: 'pdf' })).toBe(false); // no extension
    expect(isPdfFile({ filename: 'x.pdfx' })).toBe(false);
    expect(isPdfFile({})).toBe(false);
    expect(isPdfFile(undefined)).toBe(false);
  });
});

describe('formatByteSize', () => {
  it('formats bytes and 1024-based units with one decimal', () => {
    expect(formatByteSize(0)).toBe('0 B');
    expect(formatByteSize(1023)).toBe('1023 B');
    expect(formatByteSize(1024)).toBe('1 KB');
    expect(formatByteSize(1536)).toBe('1.5 KB');
    expect(formatByteSize(1048576)).toBe('1 MB');
    expect(formatByteSize(2_400_000)).toBe('2.3 MB');
    expect(formatByteSize(1073741824)).toBe('1 GB');
    expect(formatByteSize(5 * 1024 ** 4)).toBe('5 TB');
  });

  it('renders invalid sizes as a bounded zero', () => {
    expect(formatByteSize(-1)).toBe('0 B');
    expect(formatByteSize(Number.NaN)).toBe('0 B');
    expect(formatByteSize(Number.POSITIVE_INFINITY)).toBe('0 B');
    expect(formatByteSize('2048')).toBe('0 B'); // strings are not sizes
    expect(formatByteSize(undefined)).toBe('0 B');
  });
});

describe('normalizePdfConfig', () => {
  it('applies the §9.1 defaults to absent and invalid config', () => {
    expect(normalizePdfConfig(undefined)).toEqual({
      initialPage: 1,
      initialZoom: 'fit-width',
      pdfSinglePageMode: false,
      textLayer: true,
      annotations: true,
    });
    expect(normalizePdfConfig({})).toEqual(normalizePdfConfig(undefined));
    expect(normalizePdfConfig({ initialPage: -4 }).initialPage).toBe(1);
    expect(normalizePdfConfig({ initialPage: 0 }).initialPage).toBe(1);
    expect(normalizePdfConfig({ initialPage: Number.NaN }).initialPage).toBe(1);
    expect(normalizePdfConfig({ pdfSinglePageMode: 'yes' }).pdfSinglePageMode).toBe(false);
  });

  it('honors explicit overrides within the §9.1 bounds', () => {
    expect(
      normalizePdfConfig({
        initialPage: 7,
        initialZoom: 1.5,
        pdfSinglePageMode: true,
        textLayer: false,
        annotations: false,
      }),
    ).toEqual({
      initialPage: 7,
      initialZoom: 1.5,
      pdfSinglePageMode: true,
      textLayer: false,
      annotations: false,
    });
    expect(normalizePdfConfig({ initialPage: 2.9 }).initialPage).toBe(2);
    expect(normalizePdfConfig({ initialZoom: 0.2 }).initialZoom).toBe(0.5);
    expect(normalizePdfConfig({ initialZoom: 9 }).initialZoom).toBe(3);
    expect(normalizePdfConfig({ initialZoom: 'fit-width' }).initialZoom).toBe('fit-width');
  });
});

describe('buildPdfViewerState', () => {
  it('derives the metadata surface with a formatted size', () => {
    const state = buildPdfViewerState(
      { id: 'f-1', filename: ' report.pdf ', mimeType: 'application/pdf', byteSize: 2400000 },
      '/stream/report.pdf',
    );
    expect(state).toEqual({
      filename: 'report.pdf',
      mimeType: 'application/pdf',
      byteSize: 2400000,
      displaySize: '2.3 MB',
      hasStreamUrl: true,
    });
  });

  it('degrades honestly when fields are missing', () => {
    const state = buildPdfViewerState({}, undefined);
    expect(state.filename).toBe('Untitled');
    expect(state.mimeType).toBe('');
    expect(state.byteSize).toBe(-1);
    expect(state.displaySize).toBe('size unknown');
    expect(state.hasStreamUrl).toBe(false);
    expect(buildPdfViewerState({ byteSize: Number.NaN }, '').displaySize).toBe('size unknown');
    expect(buildPdfViewerState({ byteSize: -5 }, '').displaySize).toBe('size unknown');
    expect(buildPdfViewerState({ filename: '   ' }, 'x').filename).toBe('Untitled');
  });
});

describe('buildPdfRenderPlan', () => {
  const pdfMeta = { filename: 'doc.pdf', mimeType: 'application/pdf' };

  it('chooses the pdf.js path only when every condition agrees', () => {
    expect(
      buildPdfRenderPlan({ fileMeta: pdfMeta, streamUrl: '/stream/doc.pdf', assetsAvailable: true }),
    ).toEqual({ mode: 'pdfjs', reason: 'PDFJS_ASSETS_READY' });
  });

  it('falls back when assets are missing, the URL is missing, or the file is not a PDF', () => {
    expect(
      buildPdfRenderPlan({ fileMeta: pdfMeta, streamUrl: '/stream/doc.pdf', assetsAvailable: false }),
    ).toEqual({ mode: 'fallback', reason: 'PDFJS_ASSETS_MISSING' });
    expect(
      buildPdfRenderPlan({ fileMeta: pdfMeta, streamUrl: '', assetsAvailable: true }),
    ).toEqual({ mode: 'fallback', reason: 'STREAM_URL_MISSING' });
    expect(
      buildPdfRenderPlan({ fileMeta: pdfMeta, streamUrl: undefined, assetsAvailable: true }),
    ).toEqual({ mode: 'fallback', reason: 'STREAM_URL_MISSING' });
    expect(
      buildPdfRenderPlan({
        fileMeta: { filename: 'notes.txt', mimeType: 'text/plain' },
        streamUrl: '/stream/notes.txt',
        assetsAvailable: true,
      }),
    ).toEqual({ mode: 'fallback', reason: 'NOT_A_PDF_FILE' });
  });
});

describe('readPdfjsAssets', () => {
  it('accepts relative and absolute same-origin module/worker URL pairs', () => {
    expect(
      readPdfjsAssets({
        pdfjs: { moduleUrl: '/assets/pdf.min-abc123.mjs', workerUrl: '/assets/pdf.worker.min-def456.mjs' },
      }),
    ).toEqual({ moduleUrl: '/assets/pdf.min-abc123.mjs', workerUrl: '/assets/pdf.worker.min-def456.mjs' });
    // Absolute URLs on the frame's own origin are equally acceptable.
    const origin = window.location.origin;
    expect(
      readPdfjsAssets({
        pdfjs: { moduleUrl: `${origin}/assets/pdf.min.mjs`, workerUrl: '/assets/w.mjs' },
      }),
    ).toEqual({ moduleUrl: `${origin}/assets/pdf.min.mjs`, workerUrl: '/assets/w.mjs' });
  });

  it('rejects missing, partial, and malformed bootstrap entries', () => {
    expect(readPdfjsAssets(undefined)).toBeNull();
    expect(readPdfjsAssets({})).toBeNull();
    expect(readPdfjsAssets({ pdfjs: null })).toBeNull();
    expect(readPdfjsAssets({ pdfjs: { moduleUrl: '/assets/pdf.min.mjs' } })).toBeNull();
    expect(readPdfjsAssets({ pdfjs: { moduleUrl: '', workerUrl: '/assets/w.mjs' } })).toBeNull();
    expect(readPdfjsAssets({ pdfjs: { moduleUrl: 42, workerUrl: '/assets/w.mjs' } })).toBeNull();
  });

  it('rejects cross-origin and non-http(s) sources (no CDN, no data:, no javascript:)', () => {
    expect(
      readPdfjsAssets({
        pdfjs: {
          moduleUrl: 'https://cdn.example.com/pdf.min.mjs',
          workerUrl: '/assets/w.mjs',
        },
      }),
    ).toBeNull();
    expect(
      readPdfjsAssets({ pdfjs: { moduleUrl: 'data:text/javascript,module', workerUrl: '/w.mjs' } }),
    ).toBeNull();
    expect(
      readPdfjsAssets({
        pdfjs: { moduleUrl: 'javascript:///assets/pdf.min.mjs', workerUrl: '/w.mjs' },
      }),
    ).toBeNull();
    expect(
      readPdfjsAssets({ pdfjs: { moduleUrl: 'file:///assets/pdf.min.mjs', workerUrl: '/w.mjs' } }),
    ).toBeNull();
  });
});

describe('zoom and page bounds', () => {
  it('clamps zoom into the §9.1 0.5–3.0 band and resets non-finite input', () => {
    expect(clampPdfZoom(1)).toBe(1);
    expect(clampPdfZoom(0.2)).toBe(0.5);
    expect(clampPdfZoom(9)).toBe(3);
    expect(clampPdfZoom(Number.NaN)).toBe(1);
    expect(clampPdfZoom('2')).toBe(2);
  });

  it('steps zoom by ×1.25 with a hard 0.5 floor and 3.0 ceiling', () => {
    expect(stepPdfZoom(1, 1)).toBeCloseTo(1.25, 12);
    expect(stepPdfZoom(2.8, 1)).toBe(3);
    expect(stepPdfZoom(1, -1)).toBeCloseTo(0.8, 12);
    expect(stepPdfZoom(0.55, -1)).toBe(0.5);
  });

  it('resolves fit-width against the container and guards degenerate measures', () => {
    expect(resolvePdfScale('fit-width', 800, 400)).toBeCloseTo(0.5, 12);
    expect(resolvePdfScale('fit-width', 800, 1600)).toBeCloseTo(2, 12);
    expect(resolvePdfScale('fit-width', 800, 99999)).toBe(3); // clamped ceiling
    expect(resolvePdfScale('fit-width', 0, 400)).toBe(1); // jsdom zero-size guard
    expect(resolvePdfScale('fit-width', 800, 0)).toBe(1);
    expect(resolvePdfScale(1.5, 0, 0)).toBe(1.5); // explicit zoom unaffected
  });

  it('clamps page requests into [1, totalPages]', () => {
    expect(clampPdfPage(1, 10)).toBe(1);
    expect(clampPdfPage(5, 10)).toBe(5);
    expect(clampPdfPage(11, 10)).toBe(10);
    expect(clampPdfPage(0, 10)).toBe(1);
    expect(clampPdfPage(-3, 10)).toBe(1);
    expect(clampPdfPage(Number.NaN, 10)).toBe(1);
    expect(clampPdfPage(4.9, 10)).toBe(4);
    expect(clampPdfPage(3, 0)).toBe(1);
    expect(clampPdfPage('7', 10)).toBe(7);
  });
});

describe('classifyPdfjsError', () => {
  it('maps pdf.js typed exceptions to §9.1 pdf_error codes', () => {
    expect(classifyPdfjsError({ name: 'MissingPDFException' })).toBe('PDF_MISSING');
    expect(classifyPdfjsError({ name: 'InvalidPDFException' })).toBe('PDF_INVALID');
    expect(classifyPdfjsError({ name: 'UnexpectedResponseException' })).toBe('PDF_RESPONSE_ERROR');
  });

  it('collapses unknown and non-object failures to the generic load error', () => {
    expect(classifyPdfjsError(new Error('boom'))).toBe('PDFJS_LOAD_ERROR');
    expect(classifyPdfjsError('string failure')).toBe('PDFJS_LOAD_ERROR');
    expect(classifyPdfjsError(undefined)).toBe('PDFJS_LOAD_ERROR');
    expect(classifyPdfjsError({ name: 'SomethingElseException' })).toBe('PDFJS_LOAD_ERROR');
  });
});
