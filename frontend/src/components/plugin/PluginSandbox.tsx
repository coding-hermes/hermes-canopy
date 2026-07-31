/**
 * Hermes Canopy — Plugin Sandbox
 *
 * Renders a plugin in a sandboxed iframe (sandbox="allow-scripts" + CSP +
 * per-session nonce) with a postMessage-based `canopy` API shim — the MVP
 * "Sandboxed iframes + CSP + capability-scoped APIs" promise from AGENTS.md.
 *
 * The srcDoc is built client-side as a mirror of the backend BuildSrcDoc
 * (internal/plugin/sandbox.go); the injected shim is the same JS from
 * SPEC-PL-01 §7.3. Real data APIs are post-MVP: allowed api_calls resolve
 * with a stub result and a console.debug.
 *
 * Spec: SPEC-IMPL-GAP-002 §4.4, SPEC-PL-01 §7.
 */

import { useEffect, useMemo, useRef } from 'react';
import type { Permission, Plugin, PluginMessage } from '../../types/plugin.ts';
import { methodToPermission } from '../../types/plugin.ts';

// ─── Props ──────────────────────────────────────────────────────────────

export interface PluginSandboxProps {
  plugin: Plugin;
  instanceId: string;
  onError?: (e: { code: string; message: string }) => void;
}

// ─── Host-injected canopy API shim (SPEC-PL-01 §7.3, mirror of sandbox.go) ─

const CANOPY_SHIM = `(function() {
  const PLUGIN_ID = '__PLUGIN_ID__';
  const INSTANCE_ID = '__INSTANCE_ID__';
  const NONCE = '__NONCE__';
  const PARENT_ORIGIN = '__PARENT_ORIGIN__';

  let nextId = 1;
  const pendingCalls = new Map();

  window.addEventListener('message', (event) => {
    if (event.origin !== PARENT_ORIGIN) return;
    const msg = event.data;
    if (msg.target !== 'plugin:' + PLUGIN_ID + ':' + INSTANCE_ID) return;
    if (msg.nonce !== NONCE) return;

    if (msg.type === 'api_response' && pendingCalls.has(msg.id)) {
      const { resolve, reject } = pendingCalls.get(msg.id);
      pendingCalls.delete(msg.id);
      if (msg.error) reject(new CanopyAPIError(msg.error.code, msg.error.message));
      else resolve(msg.result);
    }
  });

  class CanopyAPIError extends Error {
    constructor(code, message) {
      super(message);
      this.code = code;
    }
  }

  function callAPI(method, params) {
    return new Promise((resolve, reject) => {
      const id = 'call-' + nextId++;
      pendingCalls.set(id, { resolve, reject });
      window.parent.postMessage({
        type: 'api_call',
        id,
        target: 'host',
        payload: { method, params },
        nonce: NONCE,
        timestamp: Date.now(),
      }, PARENT_ORIGIN);

      // Timeout after 30s
      setTimeout(() => {
        if (pendingCalls.has(id)) {
          pendingCalls.delete(id);
          reject(new CanopyAPIError('TIMEOUT', 'API call ' + method + ' timed out'));
        }
      }, 30000);
    });
  }

  // Public API
  window.canopy = {
    version: '1.0.0',
    pluginId: PLUGIN_ID,
    instanceId: INSTANCE_ID,

    data: {
      query: (params) => callAPI('data.query', params),
      mutate: (params) => callAPI('data.mutate', params),
    },
    notify: (params) => callAPI('notify', params),
    calendar: {
      query: (params) => callAPI('calendar.query', params),
      create: (params) => callAPI('calendar.create', params),
    },
    network: {
      fetch: (params) => callAPI('network.fetch', params),
    },

    on: (event, handler) => { /* register host event listener */ },
    emit: (event, data) => { /* postMessage('event', {event, data}) to host */ },

    error: CanopyAPIError,
  };

  // Tell host we're ready
  window.parent.postMessage({
    type: 'ready',
    id: 'ready-' + Date.now(),
    target: 'host',
    payload: { entryPoint: '__ENTRY_POINT__' },
    nonce: NONCE,
    timestamp: Date.now(),
  }, PARENT_ORIGIN);
})();`;

// ─── srcDoc builder (client-side mirror of BuildSrcDoc) ─────────────────

function buildSrcDoc(
  plugin: Plugin,
  instanceId: string,
  nonce: string,
  parentOrigin: string,
): string {
  const shim = CANOPY_SHIM
    .replaceAll('__PLUGIN_ID__', plugin.id)
    .replaceAll('__INSTANCE_ID__', instanceId)
    .replaceAll('__NONCE__', nonce)
    .replaceAll('__PARENT_ORIGIN__', parentOrigin)
    .replaceAll('__ENTRY_POINT__', plugin.manifest?.entry_point ?? 'main');

  return `<!doctype html>
<html>
<head>
  <meta charset="utf-8" />
  <meta http-equiv="Content-Security-Policy"
    content="default-src 'none';
             script-src 'unsafe-inline';
             style-src 'unsafe-inline';
             connect-src none;
             img-src data: https:;
             font-src data:;" />
  <title>${plugin.name}</title>
</head>
<body>
  <div id="root"></div>
  <script>
${indent(shim, 4)}
    (function() {
      // Plugin source is evaluated in this scope
${indent(plugin.source ?? '', 6)}
    })();
  </script>
</body>
</html>`;
}

function indent(text: string, spaces: number): string {
  const pad = ' '.repeat(spaces);
  return text
    .split('\n')
    .map((line) => pad + line)
    .join('\n');
}

// ─── Component ──────────────────────────────────────────────────────────

export default function PluginSandbox({ plugin, instanceId, onError }: PluginSandboxProps) {
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const nonceRef = useRef<string | null>(null);
  const onErrorRef = useRef(onError);
  onErrorRef.current = onError;

  // Per-mount nonce (crypto.randomUUID), baked into the srcDoc AND checked
  // on every inbound message — the host's shared secret with this instance.
  if (nonceRef.current === null) {
    nonceRef.current = crypto.randomUUID();
  }
  const nonce = nonceRef.current;

  const parentOrigin = window.location.origin;
  const grantedPermissions: Permission[] = useMemo(
    () => plugin.manifest?.permissions ?? [],
    [plugin.manifest],
  );

  const doc = useMemo(
    () => buildSrcDoc(plugin, instanceId, nonce, parentOrigin),
    [plugin, instanceId, nonce, parentOrigin],
  );

  useEffect(() => {
    const iframe = iframeRef.current;
    const target = `plugin:${plugin.id}:${instanceId}`;

    const post = (message: PluginMessage) => {
      iframe?.contentWindow?.postMessage(message, parentOrigin);
    };

    const handleMessage = (event: MessageEvent) => {
      // Sender identity: the message must come from THIS iframe. A sandboxed
      // iframe (allow-scripts without allow-same-origin) has an opaque origin,
      // so event.origin is "null" — the source check is the reliable test;
      // the per-session nonce below is the actual shared-secret validation.
      if (event.source !== iframe?.contentWindow) return;

      const msg = event.data as PluginMessage | undefined;
      if (!msg || typeof msg !== 'object') return;
      if (msg.target !== 'host') return;
      if (msg.nonce !== nonceRef.current) return;

      if (msg.type === 'ready') {
        // Plugin loaded: send init with its manifest + granted permissions.
        post({
          type: 'init',
          id: 'init-' + Date.now(),
          target,
          payload: {
            pluginId: plugin.id,
            instanceId,
            manifest: plugin.manifest,
            grantedPermissions,
            theme: 'light',
          },
          nonce: nonceRef.current ?? '',
          timestamp: Date.now(),
        });
        return;
      }

      if (msg.type === 'api_call') {
        const { method, params } = (msg.payload ?? {}) as {
          method?: string;
          params?: unknown;
        };
        const required = methodToPermission(method ?? '');
        const response: PluginMessage = {
          type: 'api_response',
          id: msg.id,
          target,
          payload: {},
          nonce: nonceRef.current ?? '',
          timestamp: Date.now(),
        };
        if (required !== '' && grantedPermissions.includes(required)) {
          // Allowed: real data APIs are post-MVP — resolve with a stub.
          console.debug('[PluginSandbox] api_call allowed', method, params);
          response.payload = { result: { ok: true, stub: true } };
        } else {
          response.payload = {
            error: {
              code: 'PERMISSION_DENIED',
              message: `Plugin has no ${required === '' ? '' : required + ' '}permission for ${method ?? 'unknown method'}`,
            },
          };
        }
        post(response);
        return;
      }

      if (msg.type === 'error') {
        const { code, message } = (msg.payload ?? {}) as {
          code?: string;
          message?: string;
        };
        onErrorRef.current?.({
          code: code ?? 'PLUGIN_ERROR',
          message: message ?? 'Plugin crashed',
        });
      }
    };

    window.addEventListener('message', handleMessage);
    return () => {
      window.removeEventListener('message', handleMessage);
      // Tell the plugin it is being unmounted.
      post({
        type: 'destroy',
        id: 'destroy-' + Date.now(),
        target,
        payload: {},
        nonce: nonceRef.current ?? '',
        timestamp: Date.now(),
      });
    };
  }, [plugin.id, plugin.manifest, instanceId, grantedPermissions, parentOrigin]);

  if (!plugin.source) {
    // Registry responses omit source (json:"-"); the caller must fetch it
    // via GET /api/v1/plugins/{id}/source before mounting.
    onErrorRef.current?.({ code: 'SOURCE_MISSING', message: 'Plugin source not loaded' });
    return null;
  }

  return (
    <iframe
      ref={iframeRef}
      name={`canopy-plugin-${plugin.id}-${instanceId}`}
      sandbox="allow-scripts"
      referrerPolicy="no-referrer"
      srcDoc={doc}
      style={{ width: '100%', height: '100%', border: 0, display: 'block' }}
      title={`${plugin.name} plugin sandbox`}
    />
  );
}
