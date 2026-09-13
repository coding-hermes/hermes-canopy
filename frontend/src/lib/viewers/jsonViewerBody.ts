/**
 * Hermes Canopy — JSON viewer body (SPEC-PL-02 §9.6, phase 3; zero-dep subset).
 *
 * The string exported here is the SECOND <script> injected into the §8.2
 * sandboxed srcDoc by ViewerHost.buildViewerDoc (after the §8.3 shim). It
 * renders a collapsible JSON/JSONC tree using ONLY the shim surface:
 *   - canopy.viewer.getTextContent() for the file text (authoritative)
 *   - canopy.__bootstrap.config for viewer config (sync)
 *   - canopy.viewer.on(...) registry for the §9.6 event names — frame-local
 *     this phase (host-side delivery is a later phase; the mount nonce is
 *     closure-captured inside the shim and unreachable here, so no invented
 *     postMessage envelope)
 *
 * Serialization contract: `jsonHelpers` methods that call sibling helpers do
 * so via `this`, so Function.prototype.toString serialization stays valid
 * (free module-scope identifiers would not survive). The pure logic is
 * unit-tested both standalone (jsonViewerLogic.ts exports) and as shipped in
 * this bundle.
 *
 * Deferred to later phases (spec §9.6 full): virtualized rendering
 * (10 000+ keys), sortKeys, themes, EXIF-style metadata, JSON5 unquoted
 * keys (the spec marks JSON5 optional; JSONC subset is implemented).
 */

import {
  appendJsonIndex,
  appendJsonPath,
  collapseDepthFromConfig,
  flagFromConfig,
  isJsoncSyntaxIssue,
  jsonChildCount,
  jsonTypeName,
  scalarPreview,
  stripJsonComments,
  stripTrailingCommas,
  validateStrictJson,
  type JsonSearchHit,
} from './jsonViewerLogic';

/**
 * The helper bundle handed to the body script. Property names are
 * load-bearing (the body script calls them by name). Cross-helper calls go
 * through `this` so the bundle survives serialization.
 */
export const jsonHelpers = {
  stripJsonComments,
  stripTrailingCommas,
  appendJsonPath,
  appendJsonIndex,
  jsonTypeName,
  jsonChildCount,
  scalarPreview,
  collapseDepthFromConfig,
  flagFromConfig,
  isJsoncSyntaxIssue,
  validateStrictJson,

  /**
   * Tolerant parse (comments + trailing commas → strict position-tracked
   * parse). NOTE: written as a function EXPRESSION (not method shorthand)
   * on purpose — method shorthand serializes via toString() without the
   * `function` keyword, which breaks the serialized bundle's
   * `name: <source>` assembly.
   */
  parseJsonc: function (source: string): { ok: true; value: unknown } | { ok: false; error: { code: string; message: string; line: number; column: number } } {
    const stripped = this.stripTrailingCommas(this.stripJsonComments(source));
    try {
      return { ok: true, value: this.validateStrictJson(stripped) };
    } catch (err: unknown) {
      if (this.isJsoncSyntaxIssue(err)) {
        return { ok: false, error: { code: 'JSON_PARSE_ERROR', message: err.message, line: err.line, column: err.column } };
      }
      return { ok: false, error: { code: 'JSON_PARSE_ERROR', message: String(err), line: 1, column: 1 } };
    }
  },

  /** Canonical `$`-rooted path from key/index segments (function expression: see parseJsonc note). */
  buildJsonPath: function (segments: Array<string | number>): string {
    let path = '$';
    for (const segment of segments) {
      if (typeof segment === 'number') path = this.appendJsonIndex(path, segment);
      else path = this.appendJsonPath(path, segment);
    }
    return path;
  },

  /** Depth-first document-order search (function expression: see parseJsonc note). */
  matchJsonNodes: function (root: unknown, query: string): JsonSearchHit[] {
    const q = query.toLowerCase();
    const hits: JsonSearchHit[] = [];
    const self = this;
    function walk(value: unknown, segments: Array<string | number>): void {
      if (Array.isArray(value)) {
        value.forEach(function (child, index) {
          walk(child, segments.concat([index]));
        });
        return;
      }
      if (typeof value === 'object' && value !== null) {
        const keys = Object.keys(value as Record<string, unknown>);
        for (const key of keys) {
          if (q !== '' && key.toLowerCase().includes(q)) {
            const segs = segments.concat([key]);
            hits.push({ path: self.buildJsonPath(segs), segments: segs });
          }
          walk((value as Record<string, unknown>)[key], segments.concat([key]));
        }
        return;
      }
      const display = typeof value === 'string' ? value : String(value);
      if (q !== '' && display.toLowerCase().includes(q)) {
        hits.push({ path: self.buildJsonPath(segments), segments: segments.slice() });
      }
    }
    walk(root, []);
    return hits;
  },
};

type JsonHelpers = typeof jsonHelpers;

function jsonViewerBodyScript(J: JsonHelpers): void {
  'use strict';
  var canopy = (window as unknown as { canopy?: Record<string, unknown> }).canopy;
  var boot = ((canopy && canopy.__bootstrap) || {}) as { config?: Record<string, unknown> };
  var config = boot.config || {};
  var viewerApi = ((canopy && canopy.viewer) || {}) as Record<string, (...args: unknown[]) => unknown>;

  /** Stable map key for a segment chain (avoids `$.a` vs `$["a"]` collisions). */
  function segmentsKey(segments: Array<string | number>): string {
    return JSON.stringify(segments);
  }

  function emit(event: string, payload: Record<string, unknown>): void {
    var handlersMap = (canopy && (canopy as { __handlers?: Record<string, Array<(p: unknown) => void>> }).__handlers) || {};
    var list = handlersMap[event];
    if (Array.isArray(list)) {
      for (var i = 0; i < list.length; i += 1) {
        try {
          list[i](payload);
        } catch (_handlerError) {
          /* a broken handler must not kill the viewer */
        }
      }
    }
  }

  // ── Scaffold ──────────────────────────────────────────────
  var rootEl = document.getElementById('root') || document.body;
  rootEl.innerHTML = '';
  rootEl.setAttribute(
    'style',
    'margin:0;min-height:100vh;overflow:auto;background:#1e1e1e;color:#d4d4d4;' +
      "font:13px/1.55 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;padding:8px 12px;",
  );

  var toolbar = document.createElement('div');
  toolbar.setAttribute('data-json-toolbar', '');
  toolbar.setAttribute('style', 'position:sticky;top:0;background:#1e1e1e;padding:4px 0;z-index:5;');
  var stats = document.createElement('span');
  stats.setAttribute('data-json-stats', '');
  stats.setAttribute('style', 'opacity:0.7;font-size:12px;');
  toolbar.appendChild(stats);
  rootEl.appendChild(toolbar);

  var treeHost = document.createElement('div');
  treeHost.setAttribute('data-json-tree', '');
  rootEl.appendChild(treeHost);

  var errorBox = document.createElement('div');
  errorBox.setAttribute('data-json-error', '');
  errorBox.setAttribute(
    'style',
    'display:none;margin:12px 0;padding:12px 16px;background:#3a1d1d;color:#ffb4b4;' +
      'border:1px solid #7a2e2e;border-radius:8px;white-space:pre-wrap;',
  );
  rootEl.appendChild(errorBox);

  function showError(code: string, message: string, line?: number, column?: number): void {
    var loc = typeof line === 'number' ? ' (line ' + line + (typeof column === 'number' ? ', column ' + column : '') + ')' : '';
    errorBox.textContent = 'json_error [' + code + ']' + loc + '\n' + message;
    errorBox.style.display = 'block';
    emit('json_error', { code: code, message: message, line: line, column: column });
  }

  function copyText(text: string, what: string): void {
    var done = function () {
      emit('json_copied', { what: what, length: text.length });
    };
    var fail = function () {
      // Fallback: hidden textarea + execCommand (clipboard API can be
      // denied inside sandboxed frames).
      try {
        var ta = document.createElement('textarea');
        ta.value = text;
        ta.setAttribute('style', 'position:fixed;left:-9999px;top:0;');
        document.body.appendChild(ta);
        ta.select();
        document.execCommand('copy');
        document.body.removeChild(ta);
        done();
      } catch (_fallbackError) {
        emit('json_copy_failed', { what: what });
      }
    };
    if (navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
      navigator.clipboard.writeText(text).then(done, fail);
    } else {
      fail();
    }
  }

  // ── Config ────────────────────────────────────────────────
  var collapseDepth = J.collapseDepthFromConfig(config.jsonCollapseDepth, 3);
  var searchEnabled = J.flagFromConfig(config.enableSearch, true);
  var copyEnabled = J.flagFromConfig(config.enableCopy, true);

  // ── Tree state ────────────────────────────────────────────
  var rootValue: unknown;
  var expanded = new Map<string, boolean>();
  var rowByPath = new Map<string, HTMLElement>();
  var COLORS: Record<string, string> = {
    string: '#ce9178',
    number: '#b5cea8',
    boolean: '#569cd6',
    null: '#569cd6',
    object: '#9cdcfe',
    array: '#4ec9b0',
  };

  function isExpanded(segments: Array<string | number>, depth: number): boolean {
    var key = segmentsKey(segments);
    var explicit = expanded.get(key);
    if (explicit !== undefined) return explicit;
    return depth < collapseDepth; // default policy: collapse at configured depth
  }

  function makeButton(title: string, onClick: () => void): HTMLButtonElement {
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.textContent = title === 'path' ? '⧉' : '{}';
    btn.title = title === 'path' ? 'Copy path' : 'Copy value';
    btn.setAttribute('data-copy', title);
    btn.setAttribute(
      'style',
      'margin-left:6px;background:transparent;color:#888;border:1px solid #444;border-radius:4px;' +
        'font-size:10px;padding:0 4px;cursor:pointer;visibility:hidden;',
    );
    btn.addEventListener('click', function (event: Event) {
      event.stopPropagation();
      onClick();
    });
    return btn;
  }

  function renderNode(value: unknown, key: string | number | null, segments: Array<string | number>, depth: number): HTMLElement {
    var row = document.createElement('div');
    row.className = 'json-row';
    var pathStr = J.buildJsonPath(segments);
    row.setAttribute('data-path', pathStr);
    row.setAttribute('data-type', J.jsonTypeName(value));
    row.setAttribute('style', 'padding-left:' + Math.min(depth, 24) * 14 + 'px;');

    var isContainer = J.jsonChildCount(value) !== null;
    var open = isContainer && isExpanded(segments, depth);

    // Caret (containers only)
    if (isContainer) {
      var caret = document.createElement('span');
      caret.className = 'json-caret';
      caret.textContent = open ? '▾ ' : '▸ ';
      caret.setAttribute('style', 'cursor:pointer;user-select:none;color:#888;');
      caret.addEventListener('click', function (event: Event) {
        event.stopPropagation();
        toggleNode(segments, depth);
      });
      row.appendChild(caret);
    } else {
      var spacer = document.createElement('span');
      spacer.textContent = '  ';
      row.appendChild(spacer);
    }

    // Key
    if (key !== null) {
      var keySpan = document.createElement('span');
      keySpan.className = 'json-key';
      keySpan.textContent = typeof key === 'number' ? key + ': ' : '"' + key + '": ';
      keySpan.setAttribute('style', 'color:#9cdcfe;');
      row.appendChild(keySpan);
    }

    if (isContainer) {
      var count = J.jsonChildCount(value) ?? 0;
      var openChar = Array.isArray(value) ? '[' : '{';
      var closeChar = Array.isArray(value) ? ']' : '}';
      var head = document.createElement('span');
      head.textContent = openChar;
      head.setAttribute('style', 'color:' + (Array.isArray(value) ? COLORS.array : COLORS.object) + ';');
      row.appendChild(head);
      if (!open) {
        var badge = document.createElement('span');
        badge.className = 'json-badge';
        badge.textContent = String(count);
        badge.title = count + (count === 1 ? ' entry' : ' entries');
        badge.setAttribute('style', 'color:#c586c0;font-size:11px;margin:0 4px;');
        row.appendChild(badge);
        var tail = document.createElement('span');
        tail.textContent = closeChar;
        tail.setAttribute('style', 'color:' + (Array.isArray(value) ? COLORS.array : COLORS.object) + ';');
        row.appendChild(tail);
      }
      if (copyEnabled) {
        row.appendChild(makeButton('path', function () {
          copyText(pathStr, 'path');
        }));
      }
    } else {
      var valueSpan = document.createElement('span');
      valueSpan.className = 'json-value';
      valueSpan.textContent = J.scalarPreview(value, 120);
      valueSpan.setAttribute('style', 'color:' + (COLORS[J.jsonTypeName(value)] || '#d4d4d4') + ';');
      row.appendChild(valueSpan);
      if (copyEnabled) {
        row.appendChild(makeButton('path', function () {
          copyText(pathStr, 'path');
        }));
        row.appendChild(makeButton('value', function () {
          copyText(typeof value === 'string' ? value : JSON.stringify(value), 'value');
        }));
      }
    }

    row.addEventListener('mouseenter', function () {
      var buttons = row.querySelectorAll('button[data-copy]');
      buttons.forEach(function (b) {
        (b as HTMLElement).style.visibility = 'visible';
      });
    });
    row.addEventListener('mouseleave', function () {
      var buttons = row.querySelectorAll('button[data-copy]');
      buttons.forEach(function (b) {
        (b as HTMLElement).style.visibility = 'hidden';
      });
    });

    rowByPath.set(segmentsKey(segments), row);

    if (isContainer && open) {
      var children = document.createElement('div');
      children.className = 'json-children';
      var entries: Array<[string | number, unknown]> = Array.isArray(value)
        ? (value as unknown[]).map(function (child, index) {
            return [index, child] as [string | number, unknown];
          })
        : Object.keys(value as Record<string, unknown>).map(function (k) {
            return [k, (value as Record<string, unknown>)[k] ] as [string | number, unknown];
          });
      for (var i = 0; i < entries.length; i += 1) {
        children.appendChild(renderNode(entries[i][1], entries[i][0], segments.concat([entries[i][0]]), depth + 1));
      }
      row.appendChild(children);
    }
    return row;
  }

  function toggleNode(segments: Array<string | number>, depth: number): void {
    var key = segmentsKey(segments);
    var nowOpen = !isExpanded(segments, depth);
    expanded.set(key, nowOpen);
    var pathStr = J.buildJsonPath(segments);
    if (rootValue === undefined) return;
    // Re-render the affected subtree: find the row, replace from parent chain.
    treeHost.innerHTML = '';
    treeHost.appendChild(renderNode(rootValue, null, [], 0));
    emit(nowOpen ? 'json_node_expanded' : 'json_node_collapsed', { path: pathStr, depth: depth });
  }

  function buildTree(value: unknown): void {
    rootValue = value;
    treeHost.innerHTML = '';
    rowByPath.clear();
    treeHost.appendChild(renderNode(value, null, [], 0));
    var rootType = J.jsonTypeName(value);
    var keyCount = J.jsonChildCount(value) ?? 0;
    stats.textContent = rootType + ' · ' + keyCount + (keyCount === 1 ? ' entry' : ' entries') + ' · collapse depth ' + collapseDepth;
    emit('json_parsed', { rootType: rootType, keyCount: keyCount, depth: maxDepth(value, 0) });
  }

  function maxDepth(value: unknown, depth: number): number {
    if (Array.isArray(value)) {
      var deepest = depth;
      for (var i = 0; i < value.length; i += 1) {
        var d = maxDepth(value[i], depth + 1);
        if (d > deepest) deepest = d;
      }
      return deepest;
    }
    if (typeof value === 'object' && value !== null) {
      var deepestObj = depth;
      var keys = Object.keys(value as Record<string, unknown>);
      for (var j = 0; j < keys.length; j += 1) {
        var dObj = maxDepth((value as Record<string, unknown>)[keys[j]], depth + 1);
        if (dObj > deepestObj) deepestObj = dObj;
      }
      return deepestObj;
    }
    return depth;
  }

  function renderFromText(text: string): void {
    var parsed = J.parseJsonc(text);
    if (!parsed.ok) {
      showError(parsed.error.code, parsed.error.message, parsed.error.line, parsed.error.column);
      return;
    }
    expanded.clear(); // fresh document → default collapse policy, no stale user state
    errorBox.style.display = 'none';
    buildTree(parsed.value);
  }

  // ── Content load: getTextContent authoritative ────────────
  if (viewerApi && typeof viewerApi.getTextContent === 'function') {
    Promise.resolve(viewerApi.getTextContent())
      .then(function (text) {
        renderFromText(typeof text === 'string' ? text : String(text));
      })
      .catch(function (err: unknown) {
        showError('TEXT_CONTENT_ERROR', (err as Error)?.message || String(err));
      });
  } else {
    showError('SHIM_MISSING', 'canopy.viewer.getTextContent is not available in this frame.');
  }

  // ── Search ────────────────────────────────────────────────
  var searchOpen = false;
  var hits: JsonSearchHit[] = [];
  var hitIndex = -1;

  var searchBar = document.createElement('div');
  searchBar.setAttribute('data-json-search', '');
  searchBar.setAttribute(
    'style',
    'display:none;position:fixed;top:0;right:12px;background:#252526;border:1px solid #444;' +
      'border-radius:6px;padding:6px;z-index:20;',
  );
  var searchInput = document.createElement('input');
  searchInput.type = 'text';
  searchInput.placeholder = 'Search keys and values…';
  searchInput.setAttribute('data-json-search-input', '');
  searchInput.setAttribute(
    'style',
    'background:#1e1e1e;color:#d4d4d4;border:1px solid #555;border-radius:4px;padding:3px 6px;width:220px;',
  );
  var searchCount = document.createElement('span');
  searchCount.setAttribute('data-json-search-count', '');
  searchCount.setAttribute('style', 'margin-left:8px;font-size:12px;opacity:0.8;');
  searchBar.appendChild(searchInput);
  searchBar.appendChild(searchCount);
  rootEl.appendChild(searchBar);

  function expandAncestors(segments: Array<string | number>): void {
    for (var len = 0; len < segments.length; len += 1) {
      expanded.set(segmentsKey(segments.slice(0, len)), true);
    }
  }

  function openSearch(): void {
    searchOpen = true;
    searchBar.style.display = 'block';
    searchInput.focus();
    emit('json_search_active', { query: searchInput.value, matchCount: hits.length });
  }
  function closeSearch(): void {
    searchOpen = false;
    searchBar.style.display = 'none';
    searchInput.value = '';
    hits = [];
    hitIndex = -1;
    searchCount.textContent = '';
  }

  function runSearch(): void {
    var query = searchInput.value;
    if (!searchOpen || query === '' || rootValue === undefined) {
      hits = [];
      hitIndex = -1;
      searchCount.textContent = '';
      return;
    }
    hits = J.matchJsonNodes(rootValue, query);
    hitIndex = hits.length > 0 ? 0 : -1;
    searchCount.textContent = hits.length + (hits.length === 1 ? ' match' : ' matches');
    emit('json_search_active', { query: query, matchCount: hits.length });
    if (hitIndex >= 0) goToHit(0);
  }

  function goToHit(index: number): void {
    if (index < 0 || index >= hits.length) return;
    hitIndex = index;
    var hit = hits[index];
    expandAncestors(hit.segments);
    treeHost.innerHTML = '';
    rowByPath.clear();
    treeHost.appendChild(renderNode(rootValue, null, [], 0));
    const row = rowByPath.get(segmentsKey(hit.segments));
    if (row) {
      if (typeof row.scrollIntoView === 'function') row.scrollIntoView({ block: 'center' });
      var highlight = ';outline:2px solid #dcdcaa;background:rgba(220,220,170,0.12);';
      row.setAttribute('style', row.getAttribute('style') + highlight);
      window.setTimeout(function () {
        if (!row.isConnected) return;
        row.setAttribute(
          'style',
          (row.getAttribute('style') ?? '').replace(/;outline:2px solid #dcdcaa;background:rgba\(220,220,170,0\.12\);/, ''),
        );
      }, 1200);
    }
    searchCount.textContent = hitIndex + 1 + '/' + hits.length;
  }

  searchInput.addEventListener('input', runSearch);
  searchInput.addEventListener('keydown', function (event: KeyboardEvent) {
    if (event.key === 'Enter') {
      event.preventDefault();
      if (hits.length === 0) {
        runSearch();
        return;
      }
      var next = event.shiftKey ? (hitIndex - 1 + hits.length) % hits.length : (hitIndex + 1) % hits.length;
      goToHit(next);
    } else if (event.key === 'Escape') {
      event.preventDefault();
      closeSearch();
    }
  });

  window.addEventListener('keydown', function (event: KeyboardEvent) {
    if ((event.ctrlKey || event.metaKey) && !event.shiftKey && !event.altKey && event.key.toLowerCase() === 'f') {
      if (!searchEnabled) return;
      event.preventDefault();
      openSearch();
    } else if (event.key === 'Escape' && searchOpen) {
      closeSearch();
    }
  });
}

/** The full in-iframe script source (IIFE) injected after the shim. */
export const jsonViewerBody: string =
  '(' +
  jsonViewerBodyScript.toString() +
  ')(' +
  '{' +
  Object.entries(jsonHelpers)
    .map(function ([name, fn]) {
      return name + ': ' + fn.toString();
    })
    .join(', ') +
  '});';
