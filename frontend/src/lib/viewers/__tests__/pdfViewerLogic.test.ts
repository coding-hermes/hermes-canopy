/**
 * Unit tests — dependency-free PDF viewer logic (SPEC-PL-02 §9.1, phase 8).
 * These exact helpers are serialized into pdfViewerBody.ts, so boundary
 * behavior here also pins the sandboxed viewer's render decisions.
 */

import { describe, expect, it } from 'vitest';
import {
  buildPdfEmbedUrl,
  buildPdfRenderPlan,
  buildPdfViewerState,
  formatByteSize,
  isPdfFile,
  normalizePdfConfig,
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

  it('chooses the native embed only when every condition agrees', () => {
    expect(
      buildPdfRenderPlan({ fileMeta: pdfMeta, streamUrl: '/stream/doc.pdf', pluginAvailable: true }),
    ).toEqual({ mode: 'native-embed', reason: 'PDF_PLUGIN_AVAILABLE' });
  });

  it('falls back when the plugin is unavailable, the URL is missing, or the file is not a PDF', () => {
    expect(
      buildPdfRenderPlan({ fileMeta: pdfMeta, streamUrl: '/stream/doc.pdf', pluginAvailable: false }),
    ).toEqual({ mode: 'fallback', reason: 'PDF_PLUGIN_UNAVAILABLE' });
    expect(
      buildPdfRenderPlan({ fileMeta: pdfMeta, streamUrl: '', pluginAvailable: true }),
    ).toEqual({ mode: 'fallback', reason: 'STREAM_URL_MISSING' });
    expect(
      buildPdfRenderPlan({ fileMeta: pdfMeta, streamUrl: undefined, pluginAvailable: true }),
    ).toEqual({ mode: 'fallback', reason: 'STREAM_URL_MISSING' });
    expect(
      buildPdfRenderPlan({
        fileMeta: { filename: 'notes.txt', mimeType: 'text/plain' },
        streamUrl: '/stream/notes.txt',
        pluginAvailable: true,
      }),
    ).toEqual({ mode: 'fallback', reason: 'NOT_A_PDF_FILE' });
  });
});

describe('buildPdfEmbedUrl', () => {
  it('appends the #page fragment for pages beyond the first', () => {
    expect(buildPdfEmbedUrl('/stream/doc.pdf', { ...normalizePdfConfig({ initialPage: 3 }) })).toBe(
      '/stream/doc.pdf#page=3',
    );
  });

  it('leaves URLs unchanged for page 1, invalid pages, existing fragments, and empty input', () => {
    const defaults = normalizePdfConfig(undefined);
    expect(buildPdfEmbedUrl('/stream/doc.pdf', defaults)).toBe('/stream/doc.pdf');
    expect(buildPdfEmbedUrl('/stream/doc.pdf', { ...defaults, initialPage: 0 })).toBe('/stream/doc.pdf');
    expect(buildPdfEmbedUrl('/stream/doc.pdf', undefined)).toBe('/stream/doc.pdf');
    expect(buildPdfEmbedUrl('/stream/doc.pdf#page=2', { ...defaults, initialPage: 5 })).toBe(
      '/stream/doc.pdf#page=2',
    );
    expect(buildPdfEmbedUrl('', { ...defaults, initialPage: 5 })).toBe('');
    expect(buildPdfEmbedUrl(undefined, defaults)).toBe('');
  });
});
