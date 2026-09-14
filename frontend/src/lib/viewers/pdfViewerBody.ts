/**
 * Hermes Canopy — PDF viewer body (SPEC-PL-02 §9.1, phase 9 pdf.js host
 * bundle).
 *
 * Injected as ViewerHost's second sandbox script. The sandboxed srcDoc body
 * cannot import npm packages, so the HOST resolves Vite-emitted local pdf.js
 * v4.x module/worker asset URLs (lib/viewers/pdfAssets.ts) and injects them
 * through the §8.3 bootstrap (`canopy.__bootstrap.pdfjs`); the body then
 * dynamically imports the module from that same-origin URL, points
 * GlobalWorkerOptions.workerSrc at the local worker asset, loads the signed
 * stream URL in a Range-capable way ({url}), and renders the active page to
 * a <canvas>.
 *
 * The dynamic import goes through a composed loader seam
 * (composePdfViewerBody): the loader function source is a string literal so
 * bundler transforms never rewrite it, and tests inject a fake loader so no
 * real browser worker is needed in jsdom.
 *
 * Failure path: module import, worker/document load, or page render errors
 * all land on the honest fallback card — metadata header plus download/open
 * actions on the signed stream URL — with a pdf_error event and an access
 * log. The failure path is never a blank frame. The native <object> embed is
 * gone: pdf.js is the only primary path.
 *
 * Phase-scope deferrals from §9.1 (named so later phases inherit an accurate
 * map): the invisible text layer / text selection and pdf_text_selected, the
 * 50-page render LRU cache, continuous-scroll virtualization, single-page
 * mode (`p`), and find-in-document (Ctrl/Cmd+F). Events beyond this phase:
 * pdf_ready {totalPages}, pdf_page_visible {page,totalPages},
 * pdf_zoom_changed {zoom,fitMode}, pdf_error {code,message} — emitted
 * exclusively through the frame-local canopy.__handlers registry; no
 * postMessage envelopes are invented here.
 */

import {
  buildPdfRenderPlan,
  buildPdfViewerState,
  clampPdfPage,
  clampPdfZoom,
  classifyPdfjsError,
  formatByteSize,
  normalizePdfConfig,
  readPdfjsAssets,
  resolvePdfScale,
  stepPdfZoom,
} from './pdfViewerLogic';

// Minimal structural seams for the pdf.js surface this body drives; the real
// pdf.js module satisfies them at runtime.
interface PdfjsRenderTask {
  promise: Promise<void>;
  cancel(): void;
}

interface PdfjsDocument {
  numPages: number;
  getPage(pageNumber: number): Promise<{
    getViewport(params: { scale: number }): { width: number; height: number };
    render(params: {
      canvasContext: CanvasRenderingContext2D | null;
      viewport: { width: number; height: number };
    }): PdfjsRenderTask;
  }>;
  destroy(): Promise<void>;
}

interface PdfjsModule {
  GlobalWorkerOptions: { workerSrc: string };
  getDocument(params: Record<string, unknown>): {
    promise: Promise<PdfjsDocument>;
    destroy(): Promise<void>;
  };
}

type PdfjsModuleLoader = (moduleUrl: string) => Promise<PdfjsModule>;

/** Helper bundle handed to the body script (property names are load-bearing). */
export const pdfHelpers = {
  buildPdfRenderPlan,
  buildPdfViewerState,
  clampPdfPage,
  clampPdfZoom,
  classifyPdfjsError,
  formatByteSize,
  normalizePdfConfig,
  readPdfjsAssets,
  resolvePdfScale,
  stepPdfZoom,
};

type PdfHelpers = typeof pdfHelpers;

function pdfViewerBodyScript(H: PdfHelpers, LOAD: PdfjsModuleLoader): void {
  'use strict';
  var canopy = (window as unknown as { canopy?: Record<string, unknown> }).canopy;
  var boot = ((canopy && canopy.__bootstrap) || {}) as {
    fileMeta?: { id?: string; filename?: string; mimeType?: string; byteSize?: number };
    streamUrl?: string;
    config?: Record<string, unknown>;
    pdfjs?: { moduleUrl?: unknown; workerUrl?: unknown } | null;
  };
  var config = boot.config || {};
  var fileMeta = boot.fileMeta || {};
  var viewerApi = ((canopy && canopy.viewer) || {}) as Record<string, (...args: unknown[]) => unknown>;
  var handlers =
    (canopy && (canopy as { __handlers?: Record<string, Array<(payload: unknown) => void>> }).__handlers) || {};

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

  // ── pdf.js state ──────────────────────────────────────────
  var viewerConfig = H.normalizePdfConfig(config);
  var currentUrl = typeof boot.streamUrl === 'string' ? boot.streamUrl : '';
  var assets = H.readPdfjsAssets(boot as { pdfjs?: { moduleUrl?: unknown; workerUrl?: unknown } | null });
  var pdfDoc: PdfjsDocument | null = null;
  var renderTask: PdfjsRenderTask | null = null;
  var loadTask: { promise: Promise<PdfjsDocument>; destroy(): Promise<void> } | null = null;
  var renderSeq = 0; // increments per render request; stale completions are dropped
  var loadSeq = 0; // increments per document load; stale loads are dropped
  var destroyedDocs = 0; // destroy() acknowledgements, for honest leak accounting
  var currentPage = viewerConfig.initialPage;
  var totalPages = 0;
  var currentScale = 0;
  var lastZoomKey = '';
  var destroyed = false;

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

  var toolbarEl: HTMLElement | null = null;
  var prevBtn: HTMLButtonElement | null = null;
  var nextBtn: HTMLButtonElement | null = null;
  var pageLabel: HTMLElement | null = null;
  var zoomInBtn: HTMLButtonElement | null = null;
  var zoomOutBtn: HTMLButtonElement | null = null;
  var zoomResetBtn: HTMLButtonElement | null = null;

  function clearContent(): void {
    while (contentHost.firstChild) contentHost.removeChild(contentHost.firstChild);
  }

  function statusText(): string {
    var state = H.buildPdfViewerState(fileMeta, currentUrl);
    var pageInfo =
      totalPages > 0
        ? ' · page ' + currentPage + ' of ' + totalPages
        : ' · loading document…';
    return (
      state.filename + ' · ' + (state.mimeType || 'unknown type') + ' · ' + state.displaySize + pageInfo
    );
  }

  function updateStatus(): void {
    statusBar.textContent = statusText();
  }

  function updateControls(): void {
    var controlsActive = pdfDoc !== null && totalPages > 0;
    if (prevBtn) prevBtn.disabled = !controlsActive || currentPage <= 1;
    if (nextBtn) nextBtn.disabled = !controlsActive || currentPage >= totalPages;
    if (zoomInBtn) zoomInBtn.disabled = !controlsActive;
    if (zoomOutBtn) zoomOutBtn.disabled = !controlsActive;
    if (zoomResetBtn) {
      zoomResetBtn.disabled = !controlsActive;
      zoomResetBtn.textContent = currentScale > 0 ? Math.round(currentScale * 100) + '%' : '100%';
    }
    if (pageLabel) {
      pageLabel.textContent = controlsActive ? currentPage + ' / ' + totalPages : '– / –';
    }
  }

  /** Build the pdf.js control toolbar once (nav + zoom + download). */
  function buildToolbar(): void {
    if (toolbarEl) return;
    var bar = document.createElement('div');
    bar.setAttribute('data-pdf-toolbar', '');
    bar.setAttribute('role', 'toolbar');
    bar.setAttribute('aria-label', 'PDF controls');
    bar.setAttribute(
      'style',
      'flex:0 0 auto;display:flex;align-items:center;gap:8px;padding:6px 14px;' +
        'background:#111827;border-bottom:1px solid #374151;',
    );
    var buttonStyle =
      'padding:4px 10px;background:#374151;border:1px solid #4b5563;border-radius:6px;color:#f9fafb;cursor:pointer;';
    function button(label: string, ariaLabel: string, attr: string): HTMLButtonElement {
      var b = document.createElement('button');
      b.type = 'button';
      b.textContent = label;
      b.setAttribute('aria-label', ariaLabel);
      b.setAttribute(attr, '');
      b.setAttribute('style', buttonStyle);
      bar.appendChild(b);
      return b;
    }
    prevBtn = button('‹ Prev', 'Previous page', 'data-pdf-prev');
    nextBtn = button('Next ›', 'Next page', 'data-pdf-next');
    pageLabel = document.createElement('span');
    pageLabel.setAttribute('data-pdf-page-label', '');
    pageLabel.setAttribute('style', 'color:#9ca3af;');
    pageLabel.textContent = totalPages > 0 ? currentPage + ' / ' + totalPages : '– / –';
    bar.appendChild(pageLabel);
    zoomOutBtn = button('−', 'Zoom out', 'data-pdf-zoom-out');
    zoomInBtn = button('+', 'Zoom in', 'data-pdf-zoom-in');
    zoomResetBtn = button('100%', 'Reset zoom', 'data-pdf-zoom-reset');
    prevBtn.onclick = function () {
      navigate(-1);
    };
    nextBtn.onclick = function () {
      navigate(1);
    };
    zoomOutBtn.onclick = function () {
      applyZoom(-1);
    };
    zoomInBtn.onclick = function () {
      applyZoom(1);
    };
    zoomResetBtn.onclick = function () {
      resetZoom();
    };
    if (currentUrl) {
      var state = H.buildPdfViewerState(fileMeta, currentUrl);
      var download = document.createElement('a');
      download.setAttribute('data-pdf-download', '');
      download.setAttribute('href', currentUrl);
      download.setAttribute('download', state.filename);
      download.textContent = 'Download';
      download.setAttribute('style', buttonStyle + 'display:inline-block;text-decoration:none;');
      bar.appendChild(download);
    }
    rootEl.appendChild(bar);
    toolbarEl = bar;
    updateControls();
  }

  /**
   * Honest fallback card: metadata header + download/open actions on the
   * signed stream URL. Every failure class lands here; never a blank frame.
   */
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

  function failure(code: string, error: unknown): void {
    var message =
      error && typeof (error as { message?: unknown }).message === 'string'
        ? String((error as { message: string }).message)
        : 'The PDF could not be rendered inline. Use the download or open actions below.';
    showFallback(code, message);
  }

  // ── pdf.js render pipeline ────────────────────────────────

  function cancelInFlightRender(): void {
    if (renderTask) {
      try {
        renderTask.cancel();
      } catch {
        /* Cancel is best-effort; a completed task cannot be cancelled. */
      }
      renderTask = null;
    }
  }

  /** Tear down the loaded document (cancel renders, destroy pdf.js doc). */
  function teardownDocument(): void {
    cancelInFlightRender();
    renderSeq += 1; // invalidate any in-flight page completion
    var doc = pdfDoc;
    pdfDoc = null;
    if (doc && typeof doc.destroy === 'function') {
      var destroyedDoc = doc;
      try {
        void destroyedDoc.destroy().then(function () {
          destroyedDocs += 1;
        });
      } catch {
        /* Destroy is best-effort during replacement. */
      }
    }
  }

  /**
   * Destroy an in-flight pdf.js loading task. Used when a newer load
   * supersedes the current one (signed-URL reconcile) or on teardown, so
   * the dropped task never leaks its worker or half-built document.
   */
  function invalidateInFlightLoad(): void {
    var task = loadTask;
    loadTask = null;
    if (task && typeof task.destroy === 'function') {
      try {
        void task.destroy();
      } catch {
        /* Destroy is best-effort during replacement. */
      }
    }
  }

  /** Full teardown: cancels loads, destroys the document, detaches keys. */
  function teardownAll(): void {
    destroyed = true;
    invalidateInFlightLoad();
    teardownDocument();
    window.removeEventListener('keydown', onKeyDown);
  }

  function emitZoomChangedIfChanged(scale: number, fitMode: boolean): void {
    var key = String(scale) + '|' + (fitMode ? 'fit' : 'abs');
    if (key === lastZoomKey) return;
    lastZoomKey = key;
    emit('pdf_zoom_changed', { zoom: scale, fitMode: fitMode });
  }

  function renderActivePage(): void {
    if (destroyed || !pdfDoc) return;
    var seq = ++renderSeq;
    cancelInFlightRender();
    currentPage = H.clampPdfPage(currentPage, totalPages);
    updateControls();
    var doc = pdfDoc;
    doc.getPage(currentPage).then(
      function (page) {
        if (destroyed || seq !== renderSeq) return;
        var base = page.getViewport({ scale: 1 });
        var scale = H.resolvePdfScale(
          viewerConfig.initialZoom,
          base.width,
          contentHost.clientWidth || 0,
        );
        var viewport = page.getViewport({ scale: scale });
        var canvas = document.createElement('canvas');
        canvas.setAttribute('data-pdf-canvas', '');
        canvas.width = Math.max(1, Math.floor(viewport.width));
        canvas.height = Math.max(1, Math.floor(viewport.height));
        canvas.setAttribute('aria-label', 'Page ' + currentPage + ' of ' + totalPages);
        canvas.setAttribute(
          'style',
          'display:block;margin:0 auto;background:#ffffff;box-shadow:0 1px 4px rgba(0,0,0,0.4);',
        );
        var context = canvas.getContext('2d');
        clearContent();
        contentHost.appendChild(canvas);
        var task = page.render({ canvasContext: context, viewport: viewport });
        renderTask = task;
        task.promise.then(
          function () {
            if (destroyed || seq !== renderSeq) return;
            renderTask = null;
            currentScale = scale;
            updateStatus();
            updateControls();
            emitZoomChangedIfChanged(scale, viewerConfig.initialZoom === 'fit-width');
            emit('pdf_page_visible', { page: currentPage, totalPages: totalPages });
          },
          function (renderError: unknown) {
            renderTask = null;
            if (destroyed || seq !== renderSeq) return;
            failure('PDF_RENDER_ERROR', renderError);
          },
        );
      },
      function (pageError: unknown) {
        if (destroyed || seq !== renderSeq) return;
        failure('PDF_PAGE_ERROR', pageError);
      },
    );
  }

  function navigate(delta: number): void {
    if (!pdfDoc || totalPages < 1) return;
    currentPage = H.clampPdfPage(currentPage + delta, totalPages);
    renderActivePage();
  }

  function applyZoom(direction: number): void {
    if (!pdfDoc) return;
    // Zooming from fit-width pins a numeric scale; further steps multiply it.
    viewerConfig.initialZoom = H.stepPdfZoom(currentScale || 1, direction);
    renderActivePage();
  }

  function resetZoom(): void {
    if (!pdfDoc) return;
    viewerConfig.initialZoom = 'fit-width';
    renderActivePage();
  }

  function downloadCurrent(): void {
    if (!currentUrl) return;
    var state = H.buildPdfViewerState(fileMeta, currentUrl);
    safeLog('download', {
      fileId: fileMeta.id || (canopy && canopy.fileId) || '',
      filename: state.filename,
    });
    var link = document.createElement('a');
    link.setAttribute('href', currentUrl);
    link.setAttribute('download', state.filename);
    document.body.appendChild(link);
    link.click();
    if (link.parentNode) link.parentNode.removeChild(link);
  }

  /** §9.1 keyboard subset for this phase (text layer, find, `p` deferred). */
  function onKeyDown(event: KeyboardEvent): void {
    if (destroyed || !pdfDoc) return;
    var key = typeof event.key === 'string' ? event.key : '';
    if (event.ctrlKey || event.metaKey) {
      var lower = key.toLowerCase();
      if (lower === '+' || lower === '=') {
        event.preventDefault();
        applyZoom(1);
      } else if (lower === '-') {
        event.preventDefault();
        applyZoom(-1);
      } else if (lower === '0') {
        event.preventDefault();
        resetZoom();
      } else if (lower === 's') {
        event.preventDefault();
        downloadCurrent();
      }
      return;
    }
    if (key === 'ArrowLeft') navigate(-1);
    else if (key === 'ArrowRight') navigate(1);
    else if (key === 'Home') {
      currentPage = 1;
      renderActivePage();
    } else if (key === 'End') {
      currentPage = totalPages;
      renderActivePage();
    }
  }

  /** Load the local pdf.js module, configure the worker, load the document. */
  function loadPdfjsDocument(): void {
    rootEl.setAttribute('data-pdf-mode', 'pdfjs');
    updateStatus();
    var resolvedAssets = assets;
    if (!resolvedAssets) {
      failure('PDFJS_ASSETS_MISSING', null);
      return;
    }
    var seq = ++loadSeq;
    var moduleUrl = resolvedAssets.moduleUrl;
    var workerUrl = resolvedAssets.workerUrl;
    LOAD(moduleUrl).then(
      function (pdfjs) {
        if (destroyed || seq !== loadSeq) return;
        if (!pdfjs || typeof pdfjs.getDocument !== 'function' || !pdfjs.GlobalWorkerOptions) {
          failure('PDFJS_LOAD_ERROR', null);
          return;
        }
        pdfjs.GlobalWorkerOptions.workerSrc = workerUrl;
        // {url} hands pdf.js the signed stream URL; pdf.js issues Range
        // requests when the server advertises them (progressive loading).
        var task = pdfjs.getDocument({ url: currentUrl });
        loadTask = task;
        return task.promise.then(
          function (doc) {
            if (destroyed || seq !== loadSeq) return;
            loadTask = null;
            pdfDoc = doc;
            totalPages = doc.numPages;
            updateStatus();
            emit('pdf_ready', { totalPages: totalPages });
            window.addEventListener('keydown', onKeyDown);
            renderActivePage();
          },
          function (loadError: unknown) {
            if (destroyed || seq !== loadSeq) return;
            loadTask = null;
            failure(H.classifyPdfjsError(loadError), loadError);
          },
        );
      },
      function (importError: unknown) {
        if (destroyed || seq !== loadSeq) return;
        failure(H.classifyPdfjsError(importError), importError);
      },
    );
  }

  function renderCurrent(): void {
    var plan = H.buildPdfRenderPlan({
      fileMeta: fileMeta,
      streamUrl: currentUrl,
      assetsAvailable: assets !== null,
    });
    rootEl.setAttribute('data-pdf-mode', plan.mode);
    if (plan.mode === 'pdfjs') {
      buildToolbar();
      loadPdfjsDocument();
      return;
    }
    var messages: Record<string, string> = {
      NOT_A_PDF_FILE:
        'This viewer only renders PDF documents, and the file metadata does not identify it as one.',
      STREAM_URL_MISSING:
        'No stream URL is available for this file, so it cannot be displayed or downloaded right now.',
      PDFJS_ASSETS_MISSING:
        'The local pdf.js renderer assets were not provided, so the document cannot be rendered inline. Use the download or open actions below.',
    };
    showFallback(plan.reason, messages[plan.reason] || 'The PDF cannot be rendered inline.');
  }

  /**
   * Reconcile the bootstrap URL against the authoritative signed stream URL.
   * In pdfjs mode a different URL tears the loaded document down and reloads
   * from the new URL; in fallback mode it re-renders the card so the
   * download/open actions point at the fresh URL.
   */
  function adoptStreamUrl(rawUrl: unknown): boolean {
    if (typeof rawUrl !== 'string' || rawUrl.length === 0) return false;
    if (rawUrl === currentUrl) return true;
    currentUrl = rawUrl;
    if (assets !== null) {
      // pdf.js is the active path: tear down whatever exists (a loaded
      // document, an in-flight loading task, or a module import still
      // pending) and restart the load on the new URL so the stale document
      // can never win the loadSeq race.
      invalidateInFlightLoad();
      teardownDocument();
      loadSeq += 1; // invalidate in-flight import/load continuations
      totalPages = 0;
      updateStatus();
      loadPdfjsDocument();
      return true;
    }
    // Fallback card (or pre-load failure): re-render with the fresh URL.
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

  // Frame replacement destroys this context; pagehide covers an explicit
  // teardown signal so in-flight pdf.js work is cancelled, not leaked.
  window.addEventListener('pagehide', teardownAll);

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

/**
 * Production pdf.js module loader source. Kept as a STRING LITERAL so
 * bundler transforms never rewrite the dynamic import; the sandbox frame
 * performs a real dynamic import of the host-resolved same-origin module
 * URL. Relative URLs resolve against the frame's document base URI (the
 * srcDoc frame inherits the parent document's base).
 */
export const PDFJS_MODULE_LOADER_SOURCE =
  'async function (moduleUrl) { return await import(moduleUrl); }';

/**
 * Test loader seam: same contract as the production loader but delegates to
 * an injectable global. composePdfViewerBody(PDFJS_LOADER_FAKE) executes the
 * EXACT shipped body string in tests with globalThis.__pdfjsLoader stubbed,
 * so the fake sits at the true import() seam of the production source.
 */
export const PDFJS_LOADER_FAKE =
  'async function (moduleUrl) { return await globalThis.__pdfjsLoader(moduleUrl); }';

/**
 * Install the test loader hook on globalThis. Returns the remover. The fake
 * may resolve to any module-shaped value; the body validates the shape it
 * needs at runtime.
 */
export function installPdfjsLoaderFake(loader: (moduleUrl: string) => Promise<unknown>): () => void {
  const holder = globalThis as { __pdfjsLoader?: (moduleUrl: string) => Promise<unknown> };
  holder.__pdfjsLoader = loader;
  return function removePdfjsLoaderFake() {
    delete holder.__pdfjsLoader;
  };
}

/**
 * Compose the full in-iframe body source: the serialized script IIFE, the
 * serialized helper bundle, and the module-loader source as the second
 * argument. Exposed so tests can inject a fake loader at the exact seam the
 * frame uses for `import()` without duplicating the body source.
 */
export function composePdfViewerBody(loaderSource: string): string {
  return (
    '(' +
    pdfViewerBodyScript.toString() +
    ')({' +
    Object.entries(pdfHelpers)
      .map(function ([name, helper]) {
        return name + ': ' + helper.toString();
      })
      .join(', ') +
    '}, ' +
    loaderSource +
    ');'
  );
}

/** The full production body source injected by ViewerHost after the shim. */
export const pdfViewerBody: string = composePdfViewerBody(PDFJS_MODULE_LOADER_SOURCE);
