/**
 * Hermes Canopy — dependency-free PDF viewer body (SPEC-PL-02 §9.1, phase 8
 * zero-dep subset).
 *
 * Injected as ViewerHost's second sandbox script. The sandboxed srcDoc body
 * cannot import npm packages, so this phase ships the zero-dependency subset
 * of §9.1: the document renders through the browser's native PDF plugin via
 * an <object type="application/pdf"> element pointed at the signed stream
 * URL (bootstrap URL first, canopy.viewer.getStreamUrl authoritative), with
 * an honest fallback card — metadata header plus download/open affordances
 * on the same stream URL — when the plugin is unavailable, the stream URL is
 * missing, the file is not a PDF, or the embed fails to load.
 *
 * Deferred from §9.1 (everything that needs the pdf.js v4.x bundle the
 * sandbox cannot import): canvas page bitmap rendering, the invisible text
 * layer and text selection, the 50-page render LRU cache, Range-request
 * progressive page loading, page navigation and zoom keyboard shortcuts, and
 * the pdf_page_visible / pdf_text_selected / pdf_zoom_changed events. The
 * subset emits only pdf_ready (embed loaded; payload has no totalPages —
 * that needs pdf.js) and pdf_error, exclusively through the frame-local
 * canopy.__handlers registry; no postMessage envelopes are invented here.
 * initialPage applies via the #page= URL fragment; initialZoom and
 * pdfSinglePageMode are normalized but not applied (the native plugin does
 * not expose zoom or page-mode controls to a dependency-free body).
 */

import {
  buildPdfEmbedUrl,
  buildPdfRenderPlan,
  buildPdfViewerState,
  formatByteSize,
  normalizePdfConfig,
} from './pdfViewerLogic';

/** Helper bundle handed to the body script (property names are load-bearing). */
export const pdfHelpers = {
  buildPdfEmbedUrl,
  buildPdfRenderPlan,
  buildPdfViewerState,
  formatByteSize,
  normalizePdfConfig,
};

type PdfHelpers = typeof pdfHelpers;

function pdfViewerBodyScript(H: PdfHelpers): void {
  'use strict';
  var canopy = (window as unknown as { canopy?: Record<string, unknown> }).canopy;
  var boot = ((canopy && canopy.__bootstrap) || {}) as {
    fileMeta?: { id?: string; filename?: string; mimeType?: string; byteSize?: number };
    streamUrl?: string;
    config?: Record<string, unknown>;
  };
  var config = boot.config || {};
  var fileMeta = boot.fileMeta || {};
  var viewerApi = ((canopy && canopy.viewer) || {}) as Record<string, (...args: unknown[]) => unknown>;
  var handlers =
    (canopy && (canopy as { __handlers?: Record<string, Array<(payload: unknown) => void>> }).__handlers) || {};
  var navigatorWithPdf = navigator as Navigator & { pdfViewerEnabled?: boolean };
  var navigatorWithMimes = navigator as Navigator & {
    mimeTypes?: { namedItem?: (name: string) => unknown };
  };

  function emit(event: string, payload: Record<string, unknown>): void {
    var list = handlers[event];
    if (!Array.isArray(list)) return;
    for (var index = 0; index < list.length; index += 1) {
      try {
        list[index](payload);
      } catch {
        /* One consumer cannot break the viewer. */
      }
    }
  }

  function safeLog(action: string, metadata: Record<string, unknown>): void {
    if (!viewerApi || typeof viewerApi.logAccess !== 'function') return;
    try {
      Promise.resolve(viewerApi.logAccess(action, metadata)).catch(function () {
        /* Access logging is observational. */
      });
    } catch {
      /* Synchronous test stubs are observational too. */
    }
  }

  function notifyReady(): void {
    if (!viewerApi || typeof viewerApi.ready !== 'function') return;
    try {
      Promise.resolve(viewerApi.ready()).catch(function () {
        /* Acknowledgement must not change a successful render. */
      });
    } catch {
      /* Acknowledgement must not change a successful render. */
    }
  }

  /**
   * Probe the browser's built-in PDF viewer: navigator.pdfViewerEnabled is
   * the modern signal, the application/pdf mimeTypes entry the legacy one.
   */
  function probePdfPlugin(): boolean {
    if (navigatorWithPdf.pdfViewerEnabled === true) return true;
    if (navigatorWithPdf.pdfViewerEnabled === false) return false;
    try {
      if (navigatorWithMimes.mimeTypes && typeof navigatorWithMimes.mimeTypes.namedItem === 'function') {
        return Boolean(navigatorWithMimes.mimeTypes.namedItem('application/pdf'));
      }
    } catch {
      /* The probe is best-effort; an inaccessible mimeTypes list means no. */
    }
    return false;
  }

  // ── DOM scaffold ──────────────────────────────────────────
  var rootEl = document.getElementById('root') || document.createElement('div');
  if (!rootEl.parentNode) {
    rootEl.id = 'root';
    document.body.appendChild(rootEl);
  }
  while (rootEl.firstChild) rootEl.removeChild(rootEl.firstChild);
  rootEl.setAttribute('data-pdf-viewer', '');
  rootEl.setAttribute('role', 'region');
  rootEl.setAttribute('aria-label', 'PDF viewer');
  rootEl.setAttribute(
    'style',
    'box-sizing:border-box;margin:0;min-height:100vh;width:100%;display:flex;flex-direction:column;' +
      'overflow:hidden;background:#111827;color:#f3f4f6;font:14px/1.4 system-ui,sans-serif;',
  );

  var statusBar = document.createElement('div');
  statusBar.setAttribute('data-pdf-status', '');
  statusBar.setAttribute('role', 'status');
  statusBar.setAttribute('aria-live', 'polite');
  statusBar.setAttribute(
    'style',
    'flex:0 0 auto;padding:8px 14px;background:#1f2937;border-bottom:1px solid #374151;color:#cbd5e1;',
  );
  rootEl.appendChild(statusBar);

  var contentHost = document.createElement('div');
  contentHost.setAttribute('data-pdf-content', '');
  contentHost.setAttribute(
    'style',
    'flex:1 1 auto;min-height:0;overflow:auto;display:flex;flex-direction:column;',
  );
  rootEl.appendChild(contentHost);

  var viewerConfig = H.normalizePdfConfig(config);
  var currentUrl = typeof boot.streamUrl === 'string' ? boot.streamUrl : '';
  var pluginOk = probePdfPlugin();

  function clearContent(): void {
    while (contentHost.firstChild) contentHost.removeChild(contentHost.firstChild);
  }

  function statusText(): string {
    var state = H.buildPdfViewerState(fileMeta, currentUrl);
    return (
      state.filename +
      ' · ' +
      (state.mimeType || 'unknown type') +
      ' · ' +
      state.displaySize +
      ' · ' +
      (pluginOk ? 'native viewer' : 'fallback')
    );
  }

  /** Build the honest fallback card: metadata header + download/open actions. */
  function showFallback(code: string, message: string): void {
    clearContent();
    rootEl.setAttribute('data-pdf-mode', 'fallback');
    var state = H.buildPdfViewerState(fileMeta, currentUrl);

    var card = document.createElement('div');
    card.setAttribute('data-pdf-fallback', '');
    card.setAttribute('role', 'alert');
    card.setAttribute(
      'style',
      'box-sizing:border-box;margin:auto;max-width:560px;padding:24px;background:#1f2937;' +
        'border:1px solid #374151;border-radius:10px;display:flex;flex-direction:column;gap:10px;',
    );

    var title = document.createElement('div');
    title.setAttribute('data-pdf-fallback-title', '');
    title.setAttribute('role', 'heading');
    title.setAttribute('aria-level', '2');
    title.textContent = 'Inline PDF unavailable';
    title.setAttribute('style', 'font-size:16px;font-weight:600;color:#f9fafb;');
    card.appendChild(title);

    var meta = document.createElement('div');
    meta.setAttribute('data-pdf-fallback-meta', '');
    meta.textContent =
      state.filename + ' · ' + (state.mimeType || 'unknown type') + ' · ' + state.displaySize;
    meta.setAttribute('style', 'color:#cbd5e1;');
    card.appendChild(meta);

    var reason = document.createElement('div');
    reason.setAttribute('data-pdf-fallback-message', '');
    reason.textContent = message;
    reason.setAttribute('style', 'color:#fde68a;white-space:pre-wrap;');
    card.appendChild(reason);

    if (state.hasStreamUrl) {
      var actions = document.createElement('div');
      actions.setAttribute('data-pdf-fallback-actions', '');
      actions.setAttribute('style', 'display:flex;gap:10px;flex-wrap:wrap;margin-top:6px;');
      var linkStyle =
        'padding:8px 12px;background:#374151;border:1px solid #4b5563;border-radius:6px;color:#f9fafb;text-decoration:none;';
      var download = document.createElement('a');
      download.setAttribute('data-pdf-download', '');
      download.setAttribute('href', currentUrl);
      download.setAttribute('download', state.filename);
      download.textContent = 'Download PDF';
      download.setAttribute('style', linkStyle);
      actions.appendChild(download);
      var openTab = document.createElement('a');
      openTab.setAttribute('data-pdf-open', '');
      openTab.setAttribute('href', currentUrl);
      openTab.setAttribute('target', '_blank');
      openTab.setAttribute('rel', 'noopener noreferrer');
      openTab.textContent = 'Open in new tab';
      openTab.setAttribute('style', linkStyle);
      actions.appendChild(openTab);
      card.appendChild(actions);
    }

    contentHost.appendChild(card);
    emit('pdf_error', { code: code, message: message });
    safeLog('error', {
      code: code,
      errorCode: code,
      message: message,
      fileId: fileMeta.id || (canopy && canopy.fileId) || '',
    });
  }

  /** Mount the native browser PDF plugin against the embed URL. */
  function renderNativeEmbed(embedUrl: string): void {
    clearContent();
    var state = H.buildPdfViewerState(fileMeta, currentUrl);
    var embed = document.createElement('object');
    embed.setAttribute('data-pdf-embed', '');
    embed.type = 'application/pdf';
    embed.setAttribute('data', embedUrl);
    embed.setAttribute('aria-label', state.filename + ' PDF document');
    embed.setAttribute('style', 'display:block;width:100%;height:100%;flex:1 1 auto;border:0;');
    embed.addEventListener('load', function () {
      // totalPages needs pdf.js and stays honestly absent from the payload.
      emit('pdf_ready', { nativePlugin: true });
    });
    embed.addEventListener('error', function () {
      showFallback(
        'PDF_EMBED_ERROR',
        'The browser could not render this PDF inline. Use the download or open actions below.',
      );
    });
    contentHost.appendChild(embed);
  }

  function renderCurrent(): void {
    statusBar.textContent = statusText();
    var plan = H.buildPdfRenderPlan({ fileMeta: fileMeta, streamUrl: currentUrl, pluginAvailable: pluginOk });
    rootEl.setAttribute('data-pdf-mode', plan.mode);
    if (plan.mode === 'native-embed') {
      renderNativeEmbed(H.buildPdfEmbedUrl(currentUrl, viewerConfig));
      return;
    }
    var messages: Record<string, string> = {
      NOT_A_PDF_FILE:
        'This viewer only renders PDF documents, and the file metadata does not identify it as one.',
      STREAM_URL_MISSING:
        'No stream URL is available for this file, so it cannot be displayed or downloaded right now.',
      PDF_PLUGIN_UNAVAILABLE:
        'This browser has no built-in PDF viewer, so the document cannot be rendered inline. Use the download or open actions below.',
    };
    showFallback(plan.reason, messages[plan.reason] || 'The PDF cannot be rendered inline.');
  }

  function adoptStreamUrl(rawUrl: unknown): boolean {
    if (typeof rawUrl !== 'string' || rawUrl.length === 0) return false;
    if (rawUrl === currentUrl) return true;
    currentUrl = rawUrl;
    renderCurrent();
    return true;
  }

  safeLog('open', {
    fileId: fileMeta.id || (canopy && canopy.fileId) || '',
    filename: fileMeta.filename || '',
    mimeType: fileMeta.mimeType || '',
    pdfSinglePageMode: viewerConfig.pdfSinglePageMode,
  });

  renderCurrent();
  notifyReady();

  if (viewerApi && typeof viewerApi.getStreamUrl === 'function') {
    try {
      Promise.resolve(viewerApi.getStreamUrl(null))
        .then(function (result) {
          var response = result as { url?: unknown } | string | null;
          var url = typeof response === 'string' ? response : response && response.url;
          /* A rejection (or empty URL) preserves the bootstrap render. */
          adoptStreamUrl(url);
        })
        .catch(function () {
          /* Preserve the already-working bootstrap URL or fallback card. */
        });
    } catch {
      /* Preserve the already-working bootstrap URL or fallback card. */
    }
  }
}

/** The full in-iframe script source (IIFE) injected after the shim. */
export const pdfViewerBody: string =
  '(' +
  pdfViewerBodyScript.toString() +
  ')({' +
  Object.entries(pdfHelpers)
    .map(function ([name, helper]) {
      return name + ': ' + helper.toString();
    })
    .join(', ') +
  '});';
