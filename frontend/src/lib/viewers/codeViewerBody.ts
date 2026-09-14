/**
 * Hermes Canopy — dependency-free code viewer body (SPEC-PL-02 §9.3, phase 7).
 *
 * This read-only body runs in the existing ViewerHost sandbox. It uses DOM
 * construction and textContent only; source, filenames, and config never pass
 * through HTML parsing. Detection and rendering are bounded so large files
 * disclose truncation rather than freezing the frame.
 *
 * Deferred from §9.3: Monaco, semantic editor actions, minimap, syntax
 * highlighting, multi-cursor, replace, comments, formatting, command palette,
 * definition lookup, and all other host-bundle editor features.
 */

import {
  detectCodeLanguage,
  findCodeMatches,
  normalizeCodeConfig,
  truncateCodeText,
} from './codeViewerLogic';

/** Helper bundle handed to the body script; property names are load-bearing. */
export const codeHelpers = {
  detectCodeLanguage,
  findCodeMatches,
  normalizeCodeConfig,
  truncateCodeText,
};

type CodeHelpers = typeof codeHelpers;

function codeViewerBodyScript(H: CodeHelpers): void {
  'use strict';
  var canopy = (window as unknown as { canopy?: Record<string, unknown> }).canopy;
  var boot = ((canopy && canopy.__bootstrap) || {}) as {
    fileMeta?: { id?: string; filename?: string; mimeType?: string };
    config?: Record<string, unknown>;
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
        /* One consumer cannot break the read-only viewer. */
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

  function errorMessage(error: unknown, fallback: string): string {
    if (error && typeof error === 'object' && typeof (error as { message?: unknown }).message === 'string') {
      return (error as { message: string }).message;
    }
    if (typeof error === 'string' && error) return error;
    return fallback;
  }

  var viewerConfig = H.normalizeCodeConfig(config);
  var rootEl = document.getElementById('root') || document.createElement('div');
  if (!rootEl.parentNode) {
    rootEl.id = 'root';
    document.body.appendChild(rootEl);
  }
  while (rootEl.firstChild) rootEl.removeChild(rootEl.firstChild);
  rootEl.setAttribute('data-code-viewer', '');
  rootEl.setAttribute('role', 'region');
  rootEl.setAttribute('aria-label', 'Read-only code viewer');
  rootEl.setAttribute(
    'style',
    'box-sizing:border-box;margin:0;min-height:100vh;width:100%;overflow:auto;background:#111827;color:#e5e7eb;',
  );

  var styleEl = document.createElement('style');
  styleEl.setAttribute('data-code-style', '');
  styleEl.textContent = [
    '[data-code-toolbar]{position:sticky;top:0;z-index:5;display:flex;align-items:center;gap:8px;flex-wrap:wrap;padding:8px 12px;background:#111827;border-bottom:1px solid #374151;font:13px/1.4 system-ui,sans-serif;}',
    '[data-code-toolbar] button,[data-code-toolbar] input{font:inherit;border:1px solid #4b5563;border-radius:4px;background:#1f2937;color:#f9fafb;padding:4px 7px;}',
    '[data-code-toolbar] button{cursor:pointer;}[data-code-toolbar] button:focus-visible,[data-code-toolbar] input:focus-visible{outline:2px solid #93c5fd;outline-offset:1px;}',
    '[data-code-stats]{color:#cbd5e1;}[data-code-status]{color:#fde68a;}[data-code-search],[data-code-goto]{display:none;align-items:center;gap:5px;}',
    '[data-code-lines]{counter-reset:code-line;overflow:auto;padding:10px 0 24px;font-size:' + viewerConfig.fontSize + 'px;line-height:1.55;font-family:' + viewerConfig.fontFamily + ';tab-size:' + viewerConfig.tabSize + ';-moz-tab-size:' + viewerConfig.tabSize + ';}',
    '[data-code-line]{display:flex;min-height:1.55em;cursor:text;outline:none;}',
    '[data-code-line]:focus-visible,[data-code-line][data-code-cursor="true"]{background:rgba(59,130,246,.14);}',
    '[data-code-gutter]{box-sizing:border-box;flex:0 0 4.5em;padding:0 1em 0 1em;color:#6b7280;text-align:right;user-select:none;}',
    '[data-code-text]{display:block;min-width:0;padding:0 1em;white-space:' + (viewerConfig.wordWrap === 'off' ? 'pre' : 'pre-wrap') + ';overflow-wrap:' + (viewerConfig.wordWrap === 'off' ? 'normal' : 'anywhere') + ';}',
    '[data-code-match]{border-radius:2px;background:#854d0e;color:#fff7ed;padding:0 1px;}[data-code-match][data-current-match="true"]{background:#f59e0b;color:#111827;outline:1px solid #fde68a;}',
    '[data-code-truncated]{margin:10px 12px;padding:9px 12px;border:1px solid #92400e;border-radius:5px;background:#422006;color:#fde68a;font:13px/1.4 system-ui,sans-serif;}',
    '[data-code-error]{margin:12px;padding:12px 14px;border:1px solid #9f1239;border-radius:6px;background:#3f1d24;color:#fecdd3;white-space:pre-wrap;font:13px/1.5 ui-monospace,monospace;}',
  ].join('\n');
  rootEl.appendChild(styleEl);

  var toolbar = document.createElement('div');
  toolbar.setAttribute('data-code-toolbar', '');
  toolbar.setAttribute('role', 'toolbar');
  toolbar.setAttribute('aria-label', 'Code viewer controls');
  rootEl.appendChild(toolbar);
  var stats = document.createElement('span');
  stats.setAttribute('data-code-stats', '');
  toolbar.appendChild(stats);
  var status = document.createElement('span');
  status.setAttribute('data-code-status', '');
  status.setAttribute('role', 'status');
  toolbar.appendChild(status);

  var searchBox = document.createElement('span');
  searchBox.setAttribute('data-code-search', '');
  searchBox.setAttribute('role', 'group');
  searchBox.setAttribute('aria-label', 'Search code');
  var searchLabel = document.createElement('label');
  searchLabel.textContent = 'Find';
  var searchInput = document.createElement('input');
  searchInput.type = 'search';
  searchInput.setAttribute('data-code-search-input', '');
  searchInput.setAttribute('aria-label', 'Find in code');
  searchInput.placeholder = 'Search';
  var searchCount = document.createElement('span');
  searchCount.setAttribute('data-code-search-count', '');
  searchBox.appendChild(searchLabel);
  searchBox.appendChild(searchInput);
  searchBox.appendChild(searchCount);
  toolbar.appendChild(searchBox);

  var gotoBox = document.createElement('span');
  gotoBox.setAttribute('data-code-goto', '');
  gotoBox.setAttribute('role', 'group');
  gotoBox.setAttribute('aria-label', 'Go to line');
  var gotoLabel = document.createElement('label');
  gotoLabel.textContent = 'Line';
  var gotoInput = document.createElement('input');
  gotoInput.type = 'text';
  gotoInput.inputMode = 'numeric';
  gotoInput.setAttribute('data-code-goto-input', '');
  gotoInput.setAttribute('aria-label', 'Go to line number');
  gotoInput.placeholder = '1';
  gotoBox.appendChild(gotoLabel);
  gotoBox.appendChild(gotoInput);
  toolbar.appendChild(gotoBox);

  var linesHost = document.createElement('div');
  linesHost.setAttribute('data-code-lines', '');
  linesHost.setAttribute('role', 'document');
  rootEl.appendChild(linesHost);
  var errorBox = document.createElement('div');
  errorBox.setAttribute('data-code-error', '');
  errorBox.setAttribute('role', 'alert');
  errorBox.style.display = 'none';
  rootEl.appendChild(errorBox);

  var lines: string[] = [];
  var matches: Array<{ line: number; column: number; length: number }> = [];
  var currentMatch = -1;
  var searchOpen = false;
  var gotoOpen = false;
  var cursor = { line: 1, column: 1 };
  var detectedLanguage = 'plaintext';
  var displayText = '';

  function showError(code: string, message: string, line?: number): void {
    var location = typeof line === 'number' ? ' (line ' + line + ')' : '';
    errorBox.textContent = 'code_error [' + code + ']' + location + '\n' + message;
    errorBox.style.display = 'block';
    var payload: Record<string, unknown> = { code: code, message: message };
    if (typeof line === 'number') payload.line = line;
    emit('code_error', payload);
    safeLog('error', {
      code: code,
      errorCode: code,
      message: message,
      line: line,
      fileId: fileMeta.id || (canopy && canopy.fileId) || '',
    });
  }

  function updateCursorDecoration(): void {
    var existing = linesHost.querySelectorAll('[data-code-line]');
    for (var index = 0; index < existing.length; index += 1) {
      var lineEl = existing[index] as HTMLElement;
      if (Number(lineEl.getAttribute('data-code-line')) === cursor.line) lineEl.setAttribute('data-code-cursor', 'true');
      else lineEl.removeAttribute('data-code-cursor');
    }
  }

  function cursorColumn(line: number, column: number): number {
    var length = (lines[line - 1] || '').length;
    return Math.min(length + 1, Math.max(1, Math.round(column)));
  }

  function moveCursor(line: number, column: number, focusLine: boolean, forceEvent = false): void {
    var safeLine = Math.min(lines.length || 1, Math.max(1, Math.round(line)));
    var safeColumn = cursorColumn(safeLine, column);
    var changed = safeLine !== cursor.line || safeColumn !== cursor.column;
    cursor = { line: safeLine, column: safeColumn };
    updateCursorDecoration();
    if (focusLine) {
      var target = linesHost.querySelector('[data-code-line="' + safeLine + '"]') as HTMLElement | null;
      if (target) {
        target.focus();
        if (typeof target.scrollIntoView === 'function') target.scrollIntoView({ block: 'center' });
      }
    }
    if (changed || forceEvent) emit('code_cursor_moved', { line: cursor.line, column: cursor.column });
  }

  function appendHighlighted(lineEl: HTMLElement, text: string, lineNumber: number): void {
    var lineMatches = matches.filter(function (match) {
      return match.line === lineNumber;
    });
    if (lineMatches.length === 0) {
      lineEl.textContent = text;
      return;
    }
    var offset = 0;
    for (var index = 0; index < lineMatches.length; index += 1) {
      var match = lineMatches[index];
      var start = Math.max(0, match.column - 1);
      if (start > offset) {
        var prefix = document.createElement('span');
        prefix.textContent = text.slice(offset, start);
        lineEl.appendChild(prefix);
      }
      var mark = document.createElement('mark');
      mark.setAttribute('data-code-match', String(index));
      var globalIndex = matches.indexOf(match);
      if (globalIndex === currentMatch) mark.setAttribute('data-current-match', 'true');
      mark.textContent = text.slice(start, start + match.length);
      lineEl.appendChild(mark);
      offset = Math.max(offset, start + match.length);
    }
    if (offset < text.length) {
      var suffix = document.createElement('span');
      suffix.textContent = text.slice(offset);
      lineEl.appendChild(suffix);
    }
  }

  function renderLines(): void {
    while (linesHost.firstChild) linesHost.removeChild(linesHost.firstChild);
    for (var index = 0; index < lines.length; index += 1) {
      var lineNumber = index + 1;
      var lineEl = document.createElement('div');
      lineEl.setAttribute('data-code-line', String(lineNumber));
      lineEl.setAttribute('tabindex', '0');
      lineEl.setAttribute('role', 'listitem');
      lineEl.setAttribute('aria-label', 'Line ' + lineNumber);
      if (viewerConfig.showLineNumbers) {
        var gutter = document.createElement('span');
        gutter.setAttribute('data-code-gutter', '');
        gutter.setAttribute('aria-hidden', 'true');
        gutter.textContent = String(lineNumber);
        lineEl.appendChild(gutter);
      }
      var textEl = document.createElement('span');
      textEl.setAttribute('data-code-text', '');
      appendHighlighted(textEl, lines[index], lineNumber);
      lineEl.appendChild(textEl);
      lineEl.addEventListener('click', function (event: Event) {
        var clicked = event.currentTarget as HTMLElement;
        moveCursor(Number(clicked.getAttribute('data-code-line')), 1, false, true);
      });
      lineEl.addEventListener('focus', function (event: Event) {
        var focused = event.currentTarget as HTMLElement;
        moveCursor(Number(focused.getAttribute('data-code-line')), 1, false, true);
      });
      lineEl.addEventListener('keydown', function (event: KeyboardEvent) {
        var currentLine = Number((event.currentTarget as HTMLElement).getAttribute('data-code-line'));
        var length = (lines[currentLine - 1] || '').length;
        if (event.key === 'ArrowUp' || event.key === 'ArrowDown') {
          event.preventDefault();
          moveCursor(currentLine + (event.key === 'ArrowUp' ? -1 : 1), cursor.column, true, true);
        } else if (event.key === 'ArrowLeft') {
          event.preventDefault();
          if (cursor.column > 1) moveCursor(currentLine, cursor.column - 1, false, true);
          else if (currentLine > 1) moveCursor(currentLine - 1, (lines[currentLine - 2] || '').length + 1, true, true);
        } else if (event.key === 'ArrowRight') {
          event.preventDefault();
          if (cursor.column <= length) moveCursor(currentLine, cursor.column + 1, false, true);
          else if (currentLine < lines.length) moveCursor(currentLine + 1, 1, true, true);
        } else if (event.key === 'Home') {
          event.preventDefault();
          moveCursor(currentLine, 1, false, true);
        } else if (event.key === 'End') {
          event.preventDefault();
          moveCursor(currentLine, length + 1, false, true);
        }
      });
      linesHost.appendChild(lineEl);
    }
    updateCursorDecoration();
  }

  function scrollToLine(line: number): void {
    var lineEl = linesHost.querySelector('[data-code-line="' + line + '"]') as HTMLElement | null;
    if (lineEl && typeof lineEl.scrollIntoView === 'function') lineEl.scrollIntoView({ block: 'center' });
  }

  function updateSearch(): void {
    matches = H.findCodeMatches(displayText, searchInput.value);
    currentMatch = matches.length > 0 ? 0 : -1;
    searchCount.textContent = matches.length > 0 ? '1/' + matches.length : '0 matches';
    status.textContent = matches.length + (matches.length === 1 ? ' match' : ' matches');
    safeLog('preview_text', {
      activity: 'search',
      query: searchInput.value,
      matchCount: matches.length,
      fileId: fileMeta.id || (canopy && canopy.fileId) || '',
    });
    renderLines();
    if (currentMatch >= 0) {
      moveCursor(matches[currentMatch].line, matches[currentMatch].column, false, true);
      scrollToLine(matches[currentMatch].line);
    }
  }

  function openSearch(): void {
    searchOpen = true;
    gotoOpen = false;
    gotoBox.style.display = 'none';
    searchBox.style.display = 'inline-flex';
    searchInput.focus();
    searchInput.select();
  }

  function closeSearch(): void {
    searchOpen = false;
    searchBox.style.display = 'none';
    searchInput.value = '';
    matches = [];
    currentMatch = -1;
    searchCount.textContent = '';
    status.textContent = '';
    renderLines();
  }

  function openGoto(): void {
    gotoOpen = true;
    searchOpen = false;
    searchBox.style.display = 'none';
    gotoBox.style.display = 'inline-flex';
    gotoInput.focus();
    gotoInput.select();
  }

  function closeGoto(): void {
    gotoOpen = false;
    gotoBox.style.display = 'none';
    gotoInput.value = '';
  }

  function goToLine(): void {
    var raw = gotoInput.value.trim();
    var line = Number(raw);
    if (!/^\d+$/.test(raw) || !Number.isSafeInteger(line) || line < 1 || line > lines.length) {
      status.textContent = 'Enter a line from 1 to ' + lines.length + '.';
      return;
    }
    moveCursor(line, 1, true, true);
    status.textContent = 'Line ' + line + ' of ' + lines.length;
    safeLog('preview_text', {
      activity: 'go_to_line',
      line: line,
      fileId: fileMeta.id || (canopy && canopy.fileId) || '',
    });
  }

  searchInput.addEventListener('input', updateSearch);
  searchInput.addEventListener('keydown', function (event: KeyboardEvent) {
    if (event.key === 'Enter') {
      event.preventDefault();
      if (matches.length === 0) return;
      currentMatch = event.shiftKey
        ? (currentMatch - 1 + matches.length) % matches.length
        : (currentMatch + 1) % matches.length;
      searchCount.textContent = currentMatch + 1 + '/' + matches.length;
      safeLog('preview_text', { activity: 'search_navigate', direction: event.shiftKey ? 'previous' : 'next', match: currentMatch + 1 });
      renderLines();
      moveCursor(matches[currentMatch].line, matches[currentMatch].column, false, true);
      scrollToLine(matches[currentMatch].line);
    } else if (event.key === 'Escape') {
      event.preventDefault();
      closeSearch();
    }
  });
  gotoInput.addEventListener('keydown', function (event: KeyboardEvent) {
    if (event.key === 'Enter') {
      event.preventDefault();
      goToLine();
    } else if (event.key === 'Escape') {
      event.preventDefault();
      closeGoto();
    }
  });
  window.addEventListener('keydown', function (event: KeyboardEvent) {
    if ((event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === 'f') {
      event.preventDefault();
      openSearch();
    } else if ((event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === 'g') {
      event.preventDefault();
      openGoto();
    } else if (event.key === 'Escape') {
      if (searchOpen) closeSearch();
      else if (gotoOpen) closeGoto();
    }
  });

  function renderFromText(source: string): void {
    try {
      var bounded = H.truncateCodeText(source, 2_000_000);
      displayText = bounded.text.replace(/\r\n?/g, '\n');
      lines = displayText.split('\n');
      var lineTruncated = false;
      if (lines.length > 100_000) {
        lines = lines.slice(0, 100_000);
        displayText = lines.join('\n');
        lineTruncated = true;
      }
      var detection = H.detectCodeLanguage(fileMeta.filename || '', source);
      detectedLanguage = detection.language;
      emit('code_language_detected', {
        language: detection.language,
        confidence: detection.confidence,
        source: detection.source,
      });
      stats.textContent = (fileMeta.filename || 'Untitled') + ' · ' + detection.language + ' · ' + lines.length + ' line' + (lines.length === 1 ? '' : 's');
      if (bounded.truncated || lineTruncated) {
        var notice = document.createElement('div');
        notice.setAttribute('data-code-truncated', '');
        notice.setAttribute('role', 'status');
        notice.textContent = bounded.truncated
          ? 'Showing a bounded prefix of ' + bounded.originalLength + ' characters; this read-only viewer capped rendering at 2,000,000 characters.'
          : 'Showing the first 100,000 lines; this read-only viewer capped line rendering to keep the frame responsive.';
        rootEl.insertBefore(notice, linesHost);
      }
      errorBox.style.display = 'none';
      renderLines();
      moveCursor(1, 1, false, true);
      emit('code_ready', { language: detectedLanguage });
      notifyReady();
    } catch (error) {
      showError('RENDER_ERROR', errorMessage(error, 'The code viewer could not render this document.'));
    }
  }

  safeLog('preview_text', {
    activity: 'view',
    fileId: fileMeta.id || (canopy && canopy.fileId) || '',
    filename: fileMeta.filename || '',
    mimeType: fileMeta.mimeType || '',
    readOnly: true,
  });

  if (viewerApi && typeof viewerApi.getTextContent === 'function') {
    try {
      Promise.resolve(viewerApi.getTextContent())
        .then(function (text) {
          renderFromText(typeof text === 'string' ? text : String(text));
        })
        .catch(function (error: unknown) {
          showError('TEXT_CONTENT_ERROR', errorMessage(error, 'The file text could not be loaded.'));
        });
    } catch (error) {
      showError('TEXT_CONTENT_ERROR', errorMessage(error, 'The file text could not be loaded.'));
    }
  } else {
    showError('SHIM_MISSING', 'canopy.viewer.getTextContent is not available in this frame.');
  }
}

/** The full in-iframe script source (IIFE) injected after the shim. */
export const codeViewerBody: string =
  '(' +
  codeViewerBodyScript.toString() +
  ')({' +
  Object.entries(codeHelpers)
    .map(function ([name, helper]) {
      return name + ': ' + helper.toString();
    })
    .join(', ') +
  '});';
