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
import { resolveApiToken } from '../lib/api';
import { fetchFileRange, postFileAccess, releaseStreamUrl, resolveStreamUrl, streamUrl } from '../lib/fileApi';
import { PDFJS_ASSETS } from '../lib/viewers/pdfAssets';
import { viewerBodyForSlug } from '../lib/viewerBodies';
import { DEFAULT_FILE_VIEWER_CONFIG, FileViewerConfigSchema, type FileMetadata, type ViewerRegistration } from '../types/fileviewer';

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
  postAccess?: typeof postFileAccess;
  /**
   * Resolves the DOM-usable stream URL (DF-HERMES-CANOPY-14). The default is
   * the real `resolveStreamUrl`: a `blob:` object URL when a bearer token
   * resolves, or the bare stream URL when none does (the `vite dev` proxy
   * injects the JWT, so the browser-issued load is authenticated for us).
   */
  resolveStream?: typeof resolveStreamUrl;
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
  'viewer.log_access',
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

/** Reads text content out of a fetched Blob. */
async function blobToText(blob: Blob): Promise<string> {
  return blob.text();
}

type PostAccessParams = Parameters<typeof postFileAccess>[0];
type AccessMetadata = Pick<PostAccessParams, 'treeId' | 'nodeId' | 'durationMs' | 'byteOffset' | 'errorCode'>;

const ACCESS_ACTIONS = new Set<PostAccessParams['action']>([
  'open',
  'download',
  'thumbnail_fetch',
  'preview_text',
  'stream_start',
  'stream_end',
  'error',
]);
const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

function viewerValidationError(code: string, message: string): Error & { code: string } {
  return Object.assign(new Error(message), { code });
}

function mapAccessMetadata(raw: unknown): AccessMetadata {
  if (raw === undefined) return {};
  if (raw === null || typeof raw !== 'object' || Array.isArray(raw)) {
    throw viewerValidationError('VIEWER_INVALID_ACCESS_METADATA', 'access metadata must be an object');
  }
  const metadata = raw as Record<string, unknown>;
  const mapped: AccessMetadata = {};

  for (const key of ['treeId', 'nodeId'] as const) {
    const value = metadata[key];
    if (value === undefined) continue;
    if (typeof value !== 'string' || !UUID_PATTERN.test(value)) {
      throw viewerValidationError('VIEWER_INVALID_ACCESS_METADATA', `${key} must be a UUID`);
    }
    mapped[key] = value;
  }
  for (const key of ['durationMs', 'byteOffset'] as const) {
    const value = metadata[key];
    if (value === undefined) continue;
    if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) {
      throw viewerValidationError('VIEWER_INVALID_ACCESS_METADATA', `${key} must be a non-negative safe integer`);
    }
    mapped[key] = value;
  }
  if (metadata.errorCode !== undefined) {
    if (typeof metadata.errorCode !== 'string') {
      throw viewerValidationError('VIEWER_INVALID_ACCESS_METADATA', 'errorCode must be a string');
    }
    mapped.errorCode = metadata.errorCode;
  }
  return mapped;
}

// ── Component ───────────────────────────────────────────────

export default function ViewerHost({
  file,
  viewer,
  config,
  className,
  onClose,
  fetchRange = fetchFileRange,
  postAccess = postFileAccess,
  resolveStream = resolveStreamUrl,
}: ViewerHostProps) {
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const [nonce] = useState(() => crypto.randomUUID());
  const [parentOrigin] = useState(() => window.location.origin);
  const [ready, setReady] = useState(false);
  // Live ref so the message handler never goes stale on file/viewer change.
  const ctxRef = useRef({ file, viewer, nonce, parentOrigin });
  ctxRef.current = { file, viewer, nonce, parentOrigin };

  // ── DF-HERMES-CANOPY-14: the DOM never receives a bare stream URL ──────
  //
  // `<a href download>`, the sandboxed iframe's `canopy.__bootstrap.streamUrl`
  // (img/video/pdf.js source) and `viewer.get_stream_url` are all loaded by the
  // BROWSER, which cannot attach an Authorization header — in a token-only
  // build (VITE_API_TOKEN / localStorage['canopy.token']) every one of them
  // 401s with TOKEN_MISSING. So when a token resolves we fetch the bytes
  // through the auth'd path and hand the DOM a `blob:` object URL instead
  // (`resolveStreamUrl`), releasing it when the file changes or we unmount.
  //
  // With NO token — the `vite dev` proxy injects the JWT for us — the bare URL
  // is used synchronously, byte-identical to the pre-fix behaviour.
  const noToken = resolveApiToken() === null;
  const [resolvedUrl, setResolvedUrl] = useState<string | null>(null);
  const [resolveError, setResolveError] = useState<string | null>(null);
  /** The URL the DOM is allowed to see: blob: when a token resolved. */
  const streamUrlFor = noToken ? streamUrl(file.id) : resolvedUrl;

  /** The one object URL this mount holds (per file), revoked on cleanup. */
  const heldUrlRef = useRef<{ fileId: string; url: string } | null>(null);
  /** In-flight resolution, shared by the mount effect and the bridge method. */
  const pendingRef = useRef<{ fileId: string; promise: Promise<string> } | null>(null);
  /** Bumped on every mount/file change; a stale resolution must not adopt. */
  const generationRef = useRef(0);
  /** The file the host is mounted for right now (see the adoption guard). */
  const mountedFileIdRef = useRef(file.id);

  /**
   * Resolve (once) and cache the DOM-usable URL for `fileId`. A second caller
   * — typically `viewer.get_stream_url` arriving before the first paint —
   * shares the in-flight promise instead of issuing a second fetch.
   */
  const ensureResolved = useCallback(
    async (fileId: string): Promise<string> => {
      const held = heldUrlRef.current;
      if (held !== null && held.fileId === fileId) return held.url;
      const pending = pendingRef.current;
      if (pending !== null && pending.fileId === fileId) return pending.promise;

      const generation = generationRef.current;
      const promise = resolveStream(fileId).catch((err: unknown) => {
        setResolveError(err instanceof Error ? err.message : String(err));
        throw err;
      });
      pendingRef.current = { fileId, promise };
      let url: string;
      try {
        url = await promise;
      } finally {
        if (pendingRef.current?.fileId === fileId) pendingRef.current = null;
      }
      if (generation !== generationRef.current || mountedFileIdRef.current !== fileId) {
        // The host moved to another file (or unmounted) while this fetch was
        // in flight. The late URL never reaches the DOM, and the effect
        // cleanup cannot see it — release it here so nothing leaks.
        releaseStreamUrl(url);
        throw Object.assign(new Error('stream URL resolution superseded by a newer file'), {
          code: 'VIEWER_STREAM_SUPERSEDED',
        });
      }
      heldUrlRef.current = { fileId, url };
      setResolvedUrl(url);
      return url;
    },
    [resolveStream],
  );

  // Resolve on mount / file change; revoke the previous object URL on the way
  // out (file switch AND unmount share this one cleanup — no leak either way).
  useEffect(() => {
    mountedFileIdRef.current = file.id;
    if (noToken) return undefined; // dev-proxy build: bare URL, nothing to release
    generationRef.current += 1;
    void ensureResolved(file.id).catch(() => {
      // Surfaced through resolveError (and, for the bridge, in the response).
    });
    return () => {
      generationRef.current += 1; // invalidate in-flight resolutions
      pendingRef.current = null;
      const held = heldUrlRef.current;
      if (held !== null) {
        releaseStreamUrl(held.url);
        heldUrlRef.current = null;
      }
      setResolvedUrl(null);
    };
  }, [file.id, noToken, ensureResolved]);

  const parsedConfig = useMemo(() => {
    const result = FileViewerConfigSchema.safeParse(config ?? {});
    return result.success ? result.data : DEFAULT_FILE_VIEWER_CONFIG;
  }, [config]);

  const srcDoc = useMemo(
    () =>
      viewer === null || streamUrlFor === null
        ? ''
        : buildViewerDoc({
            file,
            viewer,
            nonce,
            parentOrigin,
            streamUrl: streamUrlFor,
            config: parsedConfig,
          }),
    [file, viewer, nonce, parentOrigin, parsedConfig, streamUrlFor],
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
          case 'viewer.get_stream_url': {
            // The RESOLVED URL (blob: when a token resolves). Resolved on
            // demand when the frame asks before the host's own resolution
            // settled — same cache and same revoke discipline as the
            // bootstrap hand-off.
            const url = await ensureResolved(currentFile.id);
            apiResponse(id, { result: { url } });
            break;
          }
          case 'viewer.log_access': {
            if (p.params === null || typeof p.params !== 'object' || Array.isArray(p.params)) {
              throw viewerValidationError('VIEWER_INVALID_ACCESS_PARAMS', 'access-log params must be an object');
            }
            const params = p.params as Record<string, unknown>;
            const action = params.action;
            if (typeof action !== 'string' || !ACCESS_ACTIONS.has(action as PostAccessParams['action'])) {
              throw viewerValidationError('VIEWER_INVALID_ACCESS_ACTION', `unsupported access action: ${String(action)}`);
            }
            const metadata = mapAccessMetadata(params.metadata);
            const result = await postAccess({
              fileId: currentFile.id,
              action: action as PostAccessParams['action'],
              viewerSlug: currentViewer.viewerSlug,
              ...metadata,
            });
            apiResponse(id, { result });
            break;
          }
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
    [apiResponse, ensureResolved, fetchRange, parsedConfig, postAccess],
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
          <a
            href={streamUrlFor ?? undefined}
            download={file.filename}
            // While the token'd resolution is in flight the anchor carries NO
            // href at all — never the bare API path, which would 401. A click
            // then kicks the resolution instead of navigating; once the blob
            // URL lands the href is a normal authenticated download.
            aria-disabled={streamUrlFor === null ? true : undefined}
            onClick={
              streamUrlFor === null
                ? (event) => {
                    event.preventDefault();
                    void ensureResolved(file.id).catch(() => {
                      /* reported through the error line below */
                    });
                  }
                : undefined
            }
          >
            Download original
          </a>
          {resolveError !== null && (
            <p role="alert" style={{ margin: 0, opacity: 0.7 }}>
              Could not prepare an authenticated download: {resolveError}
            </p>
          )}
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
          {(streamUrlFor === null || !ready) && (
            <div role="status" style={{ padding: '0.5rem' }}>
              {streamUrlFor === null && resolveError !== null
                ? `Could not load the file: ${resolveError}`
                : 'Loading viewer…'}
            </div>
          )}
          {streamUrlFor !== null && (
            <iframe
              ref={iframeRef}
              name={viewerIframeName(viewer.viewerSlug, file.id)}
              sandbox="allow-scripts allow-same-origin"
              referrerPolicy="no-referrer"
              srcDoc={srcDoc}
              title={`${viewer.displayName}: ${file.filename}`}
              style={{ width: '100%', height: '100%', border: 0, display: 'block', flex: 1 }}
            />
          )}
        </>
      )}
    </section>
  );
}
