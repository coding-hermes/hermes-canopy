/**
 * Hermes Canopy — markdown viewer body (SPEC-PL-02 §9.5, phase 5;
 * zero-dependency subset, same precedent as the §9.6 JSON phase).
 *
 * The string exported here is the SECOND <script> injected into the §8.2
 * sandboxed srcDoc by ViewerHost.buildViewerDoc (after the §8.3 shim). It
 * renders a zero-dependency GFM-subset markdown reader using ONLY the shim
 * surface:
 *   - canopy.viewer.getTextContent() for the file text (authoritative)
 *   - canopy.__bootstrap.config for viewer config (sync)
 *   - canopy.viewer.log_access / viewer.ready exactly where the phase-3/4
 *     bodies call them
 *   - canopy.__handlers registry for the §9.5 event names — frame-local
 *     this phase (host-side delivery needs the closure-captured mount
 *     nonce and is a later phase; no postMessage envelope is invented)
 *
 * Config honored from boot.config: fontSize (12–20, default 14),
 * maxWidth (default '75ch'), renderTaskLists (default true), linkTarget
 * (default '_blank').
 *
 * Render pipeline: source → renderMarkdown (escape + GFM subset) →
 * sanitizeHtml (conservative denylist standing in for the spec's DOMPurify
 * pass until a host-side bundle exists) → innerHTML of a fullscreen
 * article with embedded CSS.
 *
 * DEFERRED from spec §9.5 (host-library features; the sandboxed srcDoc
 * body cannot import npm packages — react-markdown/remark/rehype,
 * DOMPurify, KaTeX math, mermaid diagrams, syntax highlighting
 * (rehype-highlight), footnotes, and host-side event transport all land in
 * later phases).
 */

import {
  clampFontSize,
  countBlocks,
  countWords,
  isInternalHref,
  normalizeLinkTarget,
  renderMarkdown,
  sanitizeHtml,
} from './markdownViewerLogic';

/** Helper bundle handed to the body script (property names are load-bearing). */
export const markdownHelpers = {
  clampFontSize,
  countBlocks,
  countWords,
  isInternalHref,
  normalizeLinkTarget,
  renderMarkdown,
  sanitizeHtml,
};

type MarkdownHelpers = typeof markdownHelpers;

function markdownViewerBodyScript(H: MarkdownHelpers): void {
  'use strict';
  var canopy = (window as unknown as { canopy?: Record<string, unknown> }).canopy;
  var boot = ((canopy && canopy.__bootstrap) || {}) as {
    fileMeta?: { id?: string; filename?: string; mimeType?: string };
    config?: Record<string, unknown>;
  };
  var config = boot.config || {};
  var viewerApi = ((canopy && canopy.viewer) || {}) as Record<string, (...args: unknown[]) => unknown>;
  var fileMeta = boot.fileMeta || {};

  function emit(event: string, payload: Record<string, unknown>): void {
    // Frame-local dispatch through the shim's handler registry. Host event
    // transport needs the closure-captured mount nonce and is intentionally
    // not invented here (later phase).
    var handlerMap =
      (canopy && (canopy as { __handlers?: Record<string, Array<(value: unknown) => void>> }).__handlers) || {};
    var handlers = handlerMap[event];
    if (!Array.isArray(handlers)) return;
    for (var index = 0; index < handlers.length; index += 1) {
      try {
        handlers[index](payload);
      } catch {
        /* one consumer cannot break the markdown viewer */
      }
    }
  }

  function safeLog(action: string, metadata: Record<string, unknown>): void {
    if (!viewerApi || typeof viewerApi.logAccess !== 'function') return;
    try {
      Promise.resolve(viewerApi.logAccess(action, metadata)).catch(function () {
        /* access logging is observational and must never break rendering */
      });
    } catch {
      /* same guarantee for synchronous host stubs */
    }
  }

  function notifyReady(): void {
    if (!viewerApi || typeof viewerApi.ready !== 'function') return;
    try {
      Promise.resolve(viewerApi.ready()).catch(function () {
        /* rendering never depends on the acknowledgement */
      });
    } catch {
      /* rendering never depends on the acknowledgement */
    }
  }

  function errorMessage(error: unknown, fallback: string): string {
    if (error instanceof Error && error.message) return error.message;
    if (typeof error === 'string' && error) return error;
    return fallback;
  }

  // ── Config (spec §9.5 defaults; clamped in the tested helpers) ──
  var fontSize = H.clampFontSize(config.fontSize, 14);
  var maxWidth = typeof config.maxWidth === 'string' && config.maxWidth ? config.maxWidth : '75ch';
  var linkTarget = H.normalizeLinkTarget(config.linkTarget, '_blank');
  var renderTaskLists = config.renderTaskLists !== false;

  // ── DOM scaffold ──────────────────────────────────────────
  var rootEl = document.getElementById('root') || document.body;
  rootEl.innerHTML = '';
  rootEl.setAttribute('data-markdown-viewer', '');
  rootEl.setAttribute(
    'style',
    'box-sizing:border-box;margin:0;min-height:100vh;overflow:auto;background:#1a1a1a;color:#e2e2e2;',
  );

  // Embedded CSS: readable typography, table borders, code block styling.
  var styleEl = document.createElement('style');
  styleEl.setAttribute('data-markdown-style', '');
  styleEl.textContent = [
    '.canopy-md-article { font-family: system-ui, -apple-system, "Segoe UI", sans-serif;',
    '  line-height: 1.6; margin: 0 auto; padding: 24px 28px 48px;',
    '  font-size: ' + fontSize + 'px; max-width: ' + maxWidth + '; }',
    '.canopy-md-article h1, .canopy-md-article h2, .canopy-md-article h3,',
    '.canopy-md-article h4, .canopy-md-article h5, .canopy-md-article h6',
    '  { line-height: 1.3; margin: 1.4em 0 0.6em; font-weight: 600; }',
    '.canopy-md-article h1 { font-size: 1.7em; border-bottom: 1px solid #3a3a3a; padding-bottom: 0.3em; }',
    '.canopy-md-article h2 { font-size: 1.45em; border-bottom: 1px solid #3a3a3a; padding-bottom: 0.25em; }',
    '.canopy-md-article h3 { font-size: 1.25em; }',
    '.canopy-md-article p { margin: 0.7em 0; }',
    '.canopy-md-article a { color: #7ab8ff; }',
    '.canopy-md-article code { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;',
    '  font-size: 0.9em; background: #2a2a2a; border: 1px solid #3a3a3a; border-radius: 4px; padding: 1px 5px; }',
    '.canopy-md-code { position: relative; margin: 1em 0; }',
    '.canopy-md-code-lang { position: absolute; top: 0; right: 0; padding: 2px 8px;',
    '  font: 11px/1.5 ui-monospace, monospace; color: #9a9a9a; background: #2f2f2f;',
    '  border-left: 1px solid #3a3a3a; border-bottom: 1px solid #3a3a3a; border-radius: 0 4px 0 4px; }',
    '.canopy-md-code pre { margin: 0; background: #242424; border: 1px solid #3a3a3a; border-radius: 6px;',
    '  padding: 12px 14px; overflow-x: auto; }',
    '.canopy-md-code pre code { background: transparent; border: 0; padding: 0; font-size: 0.9em; }',
    '.canopy-md-table { border-collapse: collapse; margin: 1em 0; display: block; overflow-x: auto; max-width: 100%; }',
    '.canopy-md-table th, .canopy-md-table td { border: 1px solid #3f3f3f; padding: 6px 12px; text-align: left; }',
    '.canopy-md-table thead th { background: #262626; font-weight: 600; }',
    '.canopy-md-article blockquote { margin: 1em 0; padding: 4px 16px; border-left: 4px solid #4a6fa5;',
    '  background: #22252b; color: #c3c8d1; }',
    '.canopy-md-article hr { border: 0; border-top: 1px solid #3a3a3a; margin: 1.6em 0; }',
    '.canopy-md-article ul, .canopy-md-article ol { margin: 0.7em 0; padding-left: 1.6em; }',
    '.canopy-md-article li { margin: 0.25em 0; }',
    '.canopy-md-task { list-style: none; margin-left: -1.2em; }',
    '.canopy-md-task input[type="checkbox"] { margin-right: 6px; vertical-align: middle; }',
    '[data-markdown-error] { padding: 12px 16px; background: #3f1d24; color: #fecdd3;',
    '  border: 1px solid #9f1239; border-radius: 8px; white-space: pre-wrap;',
    '  font: 13px/1.5 ui-monospace, monospace; }',
  ].join('\n');
  rootEl.appendChild(styleEl);

  var errorBox = document.createElement('div');
  errorBox.setAttribute('data-markdown-error', '');
  errorBox.setAttribute('data-visible', 'false');
  errorBox.setAttribute('role', 'alert');
  errorBox.style.display = 'none';
  rootEl.appendChild(errorBox);

  var article = document.createElement('article');
  article.className = 'canopy-md-article';
  article.setAttribute('data-markdown-content', '');
  rootEl.appendChild(article);

  function showError(code: string, message: string): void {
    errorBox.style.display = 'block';
    errorBox.setAttribute('data-visible', 'true');
    errorBox.textContent = 'markdown_error\n' + code + '\n' + message;
    emit('markdown_error', { code: code, message: message });
    safeLog('error', {
      code: code,
      errorCode: code,
      message: message,
      fileId: fileMeta.id || (canopy && canopy.fileId) || '',
    });
  }

  function renderFromText(text: string): void {
    try {
      var raw = H.renderMarkdown(text, { renderTaskLists: renderTaskLists, linkTarget: linkTarget });
      var safe = H.sanitizeHtml(raw);
      article.innerHTML = safe;
      errorBox.style.display = 'none';
      errorBox.setAttribute('data-visible', 'false');
      // §9.5 counters are computed from the SOURCE, not the HTML.
      emit('markdown_rendered', { wordCount: H.countWords(text), paragraphCount: H.countBlocks(text) });
    } catch (error) {
      showError('RENDER_ERROR', errorMessage(error, 'The markdown renderer failed on this document.'));
    }
  }

  // ── Delegated link clicks (§9.5 markdown_link_clicked) ────
  article.addEventListener('click', function (event: MouseEvent) {
    var target = event.target as (HTMLElement & { getAttribute?: (name: string) => string | null }) | null;
    while (target && target !== article) {
      if (target.tagName === 'A' || target.tagName === 'a') {
        var href = typeof target.getAttribute === 'function' ? target.getAttribute('href') || '' : '';
        if (href !== '') {
          var internal = H.isInternalHref(href);
          if (internal) event.preventDefault();
          emit('markdown_link_clicked', { href: href, internal: internal });
        }
        return;
      }
      target = (target.parentElement as typeof target) || null;
    }
  });

  // ── Content load: getTextContent authoritative ────────────
  safeLog('view', {
    fileId: fileMeta.id || (canopy && canopy.fileId) || '',
    filename: fileMeta.filename || '',
    mimeType: fileMeta.mimeType || '',
    fontSize: fontSize,
    maxWidth: maxWidth,
  });
  if (viewerApi && typeof viewerApi.getTextContent === 'function') {
    Promise.resolve(viewerApi.getTextContent())
      .then(function (text) {
        renderFromText(typeof text === 'string' ? text : String(text));
      })
      .catch(function (error: unknown) {
        showError('TEXT_CONTENT_ERROR', errorMessage(error, 'The file text could not be loaded.'));
      });
  } else {
    showError('SHIM_MISSING', 'canopy.viewer.getTextContent is not available in this frame.');
  }

  notifyReady();
}

/** The full in-iframe script source (IIFE) injected after the shim. */
export const markdownViewerBody: string =
  '(' +
  markdownViewerBodyScript.toString() +
  ')(' +
  '{' +
  Object.entries(markdownHelpers)
    .map(function ([name, helper]) {
      return name + ': ' + helper.toString();
    })
    .join(', ') +
  '});';
