/**
 * Hermes Canopy — ViewerHost (SPEC-PL-02 §8).
 *
 * Right-panel shell for a rendered file: builds the §8.2 sandboxed srcDoc
 * iframe (viewer CSP, per-mount nonce, host-injected canopy.viewer shim)
 * and answers the read-only canopy.viewer.* methods over the validated
 * postMessage bridge (origin + nonce checked on every message; responses
 * are stamped with the nonce and re-checked in the frame's shim).
 *
 * The in-frame shim's method set mirrors spec §8.3. This phase implements
 * the read-only set the backend can serve today (metadata, text/binary
 * content, stream URL, config, ready) and refuses the rest.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { fetchFileRange, streamUrl } from '../lib/fileApi';
import { viewerBodyForSlug } from '../lib/viewerBodies';
import { DEFAULT_FILE_VIEWER_CONFIG, FileViewerConfigSchema, type FileMetadata, type ViewerRegistration } from '../types/fileviewer';

export const VIEWER_SANDBOX_CSP = [
  "default-src 'none'",
  "script-src 'self' 'unsafe-inline'",
  "style-src 'self' 'unsafe-inline'",
  "img-src 'self' data: blob: https:",
  "media-src 'self' blob:",
  "font-src 'self' data:",
  "connect-src 'self'",
].join('; ');

export interface ViewerHostProps {
  file: FileMetadata;
  /**
   * The resolved viewer registration, or null when selectViewer found no
   * match (quarantined, not viewable, unsupported type) — the host then
   * renders the download-only empty state instead of a sandbox.
   */
  viewer: ViewerRegistration | null;
  config?: unknown; // parsed with FileViewerConfigSchema; falls back to defaults
  className?: string;
  onClose?: () => void;
  /**
   * Seams for tests/DI; production defaults hit the real fileApi.
   * Range fetch is the one content path — the sandbox CSP has no
   * connect-src for the API surface, so content moves through the host.
   */
  fetchRange?: typeof fetchFileRange;
}

export interface ViewerMessage {
  type: string;
  id: string;
  target: 'host' | string;
  payload: unknown;
  nonce: string;
  timestamp: number;
}

/** The read-only canopy.viewer method set served by this host phase. */
const SERVED_METHODS = new Set([
  'viewer.get_file_metadata',
  'viewer.get_text_content',
  'viewer.get_binary_content',
  'viewer.get_stream_url',
  'viewer.get_config',
  'viewer.ready',
]);

// ── srcDoc assembly (§8.2) ──────────────────────────────────

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
  </script>${bodyScript}
</body>
</html>`;
}

/** Builds the iframe name per spec §8.2: canopy-viewer-{slug}-{fileId}. */
export function viewerIframeName(viewerSlug: string, fileId: string): string {
  return `canopy-viewer-${viewerSlug}-${fileId}`;
}

/** Reads text content out of a fetched Blob. */
async function blobToText(blob: Blob): Promise<string> {
  return blob.text();
}

// ── Component ───────────────────────────────────────────────

export default function ViewerHost({
  file,
  viewer,
  config,
  className,
  onClose,
  fetchRange = fetchFileRange,
}: ViewerHostProps) {
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const [nonce] = useState(() => crypto.randomUUID());
  const [parentOrigin] = useState(() => window.location.origin);
  const [ready, setReady] = useState(false);
  // Live ref so the message handler never goes stale on file/viewer change.
  const ctxRef = useRef({ file, viewer, nonce, parentOrigin });
  ctxRef.current = { file, viewer, nonce, parentOrigin };

  const parsedConfig = useMemo(() => {
    const result = FileViewerConfigSchema.safeParse(config ?? {});
    return result.success ? result.data : DEFAULT_FILE_VIEWER_CONFIG;
  }, [config]);

  const srcDoc = useMemo(
    () =>
      viewer === null
        ? ''
        : buildViewerDoc({
            file,
            viewer,
            nonce,
            parentOrigin,
            streamUrl: streamUrl(file.id),
            config: parsedConfig,
          }),
    [file, viewer, nonce, parentOrigin, parsedConfig],
  );

  const apiResponse = useCallback((id: string, body: { result?: unknown; error?: { code: string; message: string } }) => {
    const { nonce: n, parentOrigin: origin, viewer: v, file: f } = ctxRef.current;
    if (v === null) return; // no frame mounted; nothing to answer
    iframeRef.current?.contentWindow?.postMessage(
      {
        type: 'viewer_api_response',
        id,
        target: `viewer:${v.viewerSlug}:${f.id}`,
        nonce: n,
        timestamp: Date.now(),
        ...body,
      },
      origin,
    );
  }, []);

  const handleApiCall = useCallback(
    async (id: string, payload: unknown) => {
      const { file: currentFile, viewer: currentViewer } = ctxRef.current;
      if (currentViewer === null) return;
      const p = (payload ?? {}) as { method?: unknown; params?: unknown };
      const method = typeof p.method === 'string' ? p.method : '';
      if (!SERVED_METHODS.has(method)) {
        apiResponse(id, { error: { code: 'VIEWER_NOT_IMPLEMENTED', message: `method not served by this viewer host: ${method || '(none)'}` } });
        return;
      }
      try {
        switch (method) {
          case 'viewer.get_file_metadata':
            apiResponse(id, { result: currentFile });
            break;
          case 'viewer.get_text_content': {
            if (!currentFile.isText) {
              apiResponse(id, { error: { code: 'FILE_NOT_TEXT', message: 'file is not a text file' } });
              break;
            }
            const { blob } = await fetchRange(currentFile.id, null);
            apiResponse(id, { result: await blobToText(blob) });
            break;
          }
          case 'viewer.get_binary_content': {
            const { blob } = await fetchRange(currentFile.id, null);
            apiResponse(id, { result: await blob.arrayBuffer() });
            break;
          }
          case 'viewer.get_stream_url':
            apiResponse(id, { result: { url: streamUrl(currentFile.id) } });
            break;
          case 'viewer.get_config':
            apiResponse(id, { result: parsedConfig });
            break;
          case 'viewer.ready':
            apiResponse(id, { result: { viewerSlug: currentViewer.viewerSlug, fileId: currentFile.id } });
            break;
        }
      } catch (err: unknown) {
        const code = (err as { code?: string }).code ?? 'INTERNAL_ERROR';
        const message = err instanceof Error ? err.message : String(err);
        apiResponse(id, { error: { code, message } });
      }
    },
    [apiResponse, fetchRange, parsedConfig],
  );

  useEffect(() => {
    function handleMessage(event: MessageEvent) {
      // 1. Origin gate: only messages from our own window's origin.
      if (event.origin !== ctxRef.current.parentOrigin) return;
      const msg = event.data as Partial<ViewerMessage> | null;
      // 2. Shape gate: envelope + addressed to THIS host.
      if (!msg || typeof msg !== 'object') return;
      if (msg.target !== 'host') return;
      // 3. Nonce gate: only OUR mount's frames can talk to us.
      if (msg.nonce !== ctxRef.current.nonce) return;

      if (msg.type === 'viewer_ready') {
        setReady(true);
        const current = ctxRef.current;
        apiResponse(
          typeof msg.id === 'string' ? msg.id : 'ready',
          current.viewer === null
            ? { error: { code: 'VIEWER_NOT_MOUNTED', message: 'no viewer is mounted' } }
            : { result: { viewerSlug: current.viewer.viewerSlug, fileId: current.file.id } },
        );
        return;
      }
      if (msg.type === 'viewer_api_call') {
        void handleApiCall(typeof msg.id === 'string' ? msg.id : '', msg.payload);
        return;
      }
      // Everything else (incl. 'viewer_error', 'viewer_event') is ignored this phase.
    }
    window.addEventListener('message', handleMessage);
    return () => window.removeEventListener('message', handleMessage);
  }, [apiResponse, handleApiCall]);

  return (
    <section
      data-file-viewer
      data-file-id={file.id}
      className={className}
      style={{ position: 'relative', width: '100%', height: '100%', display: 'flex', flexDirection: 'column' }}
    >
      {viewer === null ? (
        // spec §2/§6.4 tier 7: no viewer matched → download-only empty state.
        // Never silently fail: name the file and offer the raw bytes.
        <div
          data-empty-state
          role="status"
          style={{ flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: '0.75rem', padding: '1rem' }}
        >
          <p style={{ margin: 0 }}>No viewer available for this file</p>
          <p style={{ margin: 0, opacity: 0.7 }}>
            {file.filename} · {file.mimeType}
          </p>
          <a href={streamUrl(file.id)} download={file.filename}>
            Download original
          </a>
          {onClose && (
            <button type="button" onClick={onClose}>
              Close
            </button>
          )}
        </div>
      ) : (
        <>
          <div
            data-viewer-header
            style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '0.25rem 0.5rem' }}
          >
            <span>{viewer.displayName}</span>
            {onClose && (
              <button type="button" onClick={onClose} aria-label="Close viewer">
                Close
              </button>
            )}
          </div>
          {!ready && (
            <div role="status" style={{ padding: '0.5rem' }}>
              Loading viewer…
            </div>
          )}
          <iframe
            ref={iframeRef}
            name={viewerIframeName(viewer.viewerSlug, file.id)}
            sandbox="allow-scripts allow-same-origin"
            referrerPolicy="no-referrer"
            srcDoc={srcDoc}
            title={`${viewer.displayName}: ${file.filename}`}
            style={{ width: '100%', height: '100%', border: 0, display: 'block', flex: 1 }}
          />
        </>
      )}
    </section>
  );
}
