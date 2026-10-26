/**
 * Hermes Canopy — ViewerHost srcDoc assembly (SPEC-PL-02 §8.2).
 *
 * The sandboxed iframe document and its inside-the-frame shim live in their
 * own module rather than beside the ViewerHost component: a .tsx file that
 * also exports plain functions breaks React Fast Refresh (only-export-
 * components), and these builders are pure string work with no React in
 * them — they are unit-tested directly.
 */

import { PDFJS_ASSETS } from '../lib/viewers/pdfAssets';
import { viewerBodyForSlug } from '../lib/viewerBodies';
import type { FileMetadata, ViewerRegistration } from '../types/fileviewer';

// SPEC-PL-02 §9.1 phase 9: the one worker rule the local pdf.js worker asset
// needs. blob: covers pdf.js's module-worker fallback wrapper; no data:,
// wildcard, or remote script sources are admitted.
export const VIEWER_SANDBOX_CSP = [
  "default-src 'none'",
  "script-src 'self' 'unsafe-inline'",
  "style-src 'self' 'unsafe-inline'",
  "img-src 'self' data: blob: https:",
  "media-src 'self' blob:",
  "font-src 'self' data:",
  "connect-src 'self'",
  "worker-src 'self' blob:",
].join('; ');

function js(value: string): string {
  return JSON.stringify(value).replaceAll('<', '\\u003c');
}

const shimTemplate = `(function() {
  'use strict';
  var PARENT_ORIGIN = __PARENT_ORIGIN__;
  var NONCE = __NONCE__;
  var VIEWER_SLUG = __VIEWER_SLUG__;
  var FILE_ID = __FILE_ID__;
  var FILE_META = __FILE_META_JSON__;
  var STREAM_URL = __STREAM_URL__;
  var VIEWER_CONFIG = __VIEWER_CONFIG_JSON__;
  var pendingCalls = new Map();
  var nextId = 1;
  function post(type, id, payload) {
    window.parent.postMessage({ type: type, id: id, target: 'host', payload: payload, nonce: NONCE, timestamp: Date.now() }, PARENT_ORIGIN);
  }
  function callAPI(method, params) {
    return new Promise(function(resolve, reject) {
      var id = 'viewer-call-' + nextId++;
      pendingCalls.set(id, { resolve: resolve, reject: reject });
      post('viewer_api_call', id, { method: method, params: params });
      setTimeout(function() {
        if (pendingCalls.delete(id)) reject(Object.assign(new Error('API call timed out'), { code: 'VIEWER_API_TIMEOUT' }));
      }, 30000);
    });
  }
  window.addEventListener('message', function(event) {
    if (event.origin !== PARENT_ORIGIN) return;
    var msg = event.data;
    if (!msg || msg.target !== 'viewer:' + VIEWER_SLUG + ':' + FILE_ID || msg.nonce !== NONCE) return;
    if (msg.type === 'viewer_api_response' && pendingCalls.has(msg.id)) {
      var pending = pendingCalls.get(msg.id);
      pendingCalls.delete(msg.id);
      if (msg.error) pending.reject(Object.assign(new Error(msg.error.message), { code: msg.error.code }));
      else pending.resolve(msg.result);
    }
  });
  window.canopy = {
    version: '1.0.0',
    viewerSlug: VIEWER_SLUG,
    fileId: FILE_ID,
    viewer: {
      getFileMetadata: function() { return callAPI('viewer.get_file_metadata', {}); },
      getTextContent: function() { return callAPI('viewer.get_text_content', {}); },
      getBinaryContent: function() { return callAPI('viewer.get_binary_content', {}); },
      getStreamUrl: function(range) { return callAPI('viewer.get_stream_url', { range: range || null }); },
      logAccess: function(action, metadata) { return callAPI('viewer.log_access', { action: action, metadata: metadata }); },
      getConfig: function() { return callAPI('viewer.get_config', {}); },
      ready: function() { return callAPI('viewer.ready', {}); },
      on: function(event, handler) {
        (window.canopy.__handlers[event] = window.canopy.__handlers[event] || []).push(handler);
        return function() {
          window.canopy.__handlers[event] = window.canopy.__handlers[event].filter(function(h) { return h !== handler; });
        };
      },
    },
    __handlers: {},
    error: function ViewerError(code, message) {
      this.code = code;
      this.message = message;
    },
    __bootstrap: {
      fileMeta: FILE_META,
      streamUrl: STREAM_URL,
      config: VIEWER_CONFIG,
    },
  };
  post('viewer_ready', 'ready-' + Date.now(), { viewerSlug: VIEWER_SLUG, fileId: FILE_ID });
})();`;

export function buildViewerDoc(input: {
  file: FileMetadata;
  viewer: ViewerRegistration;
  nonce: string;
  parentOrigin: string;
  streamUrl: string;
  config: unknown;
}): string {
  const title = `${input.viewer.displayName} — ${input.file.filename}`
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;');
  const inlinedJson = (value: unknown): string => JSON.stringify(value).replaceAll('<', '\\u003c');
  const shim = shimTemplate
    .replaceAll('__PARENT_ORIGIN__', js(input.parentOrigin))
    .replaceAll('__NONCE__', js(input.nonce))
    .replaceAll('__VIEWER_SLUG__', js(input.viewer.viewerSlug))
    .replaceAll('__FILE_ID__', js(input.file.id))
    .replaceAll('__FILE_META_JSON__', inlinedJson(input.file))
    .replaceAll('__STREAM_URL__', js(input.streamUrl))
    .replaceAll('__VIEWER_CONFIG_JSON__', inlinedJson(input.config));
  // SPEC-PL-02 §9.1 phase 9: the pdf slug's bootstrap additionally carries
  // the host-resolved LOCAL pdf.js asset URLs so the sandboxed body can load
  // the bundled module/worker from same-origin URLs (no CDN, no network).
  // Every other slug gets an empty object — the URLs never leave the host
  // for them. The full JSON object literal is the second Object.assign
  // argument ({"pdfjs":{...}} or {}), keeping the script valid JS.
  const bootstrapExtras = inlinedJson(
    input.viewer.viewerSlug === 'pdf' ? { pdfjs: PDFJS_ASSETS } : {},
  );
  // Phase 3: built-in viewer bodies run inside the frame AFTER the shim,
  // against the canopy.viewer surface. Unknown / not-yet-shipped slugs keep
  // the phase-2 shim-only doc (no body <script>).
  const bodySource = viewerBodyForSlug(input.viewer.viewerSlug);
  const bodyScript = bodySource === null ? '' : `\n  <script>\n    ${bodySource}\n  </script>`;
  return `<!doctype html>
<html>
<head>
  <meta charset="utf-8" />
  <meta http-equiv="Content-Security-Policy" content="${VIEWER_SANDBOX_CSP}" />
  <title>${title}</title>
</head>
<body>
  <div id="root"></div>
  <script>
    ${shim}
  </script>
  <script>
    window.canopy.__bootstrap = Object.assign(window.canopy.__bootstrap, ${bootstrapExtras});
  </script>${bodyScript}
</body>
</html>`;
}

/** Builds the iframe name per spec §8.2: canopy-viewer-{slug}-{fileId}. */
export function viewerIframeName(viewerSlug: string, fileId: string): string {
  return `canopy-viewer-${viewerSlug}-${fileId}`;
}
