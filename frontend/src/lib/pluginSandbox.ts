import { PluginApiError, PluginApiHost } from './pluginApi';
import {
  PluginApiCallSchema, PluginMessageSchema,
  type PluginApiResponsePayload, type PluginEventPayload, type PluginManifest,
  type PluginMessage, type PluginPermission,
} from './pluginTypes';
import { PluginManifestSchema } from './pluginTypes';

export const SANDBOX_CSP = `default-src 'none';
             script-src 'unsafe-inline';
             style-src 'unsafe-inline';
             connect-src none;
             img-src data: https:;
             font-src data:;`;

export interface SandboxPlugin {
  id: string;
  instanceId: string;
  name: string;
  manifest: PluginManifest;
  sourceJS: string;
  nonce: string;
  parentOrigin: string;
}

export function parseEmbeddedManifest(source: string): PluginManifest {
  const match = source.match(/\/\*@@canopy\.manifest@@([\s\S]*?)@@end@@\*\//);
  if (!match) throw new Error('Plugin source has no embedded manifest');
  return PluginManifestSchema.parse(JSON.parse(match[1]));
}

export async function sha256Hex(source: string): Promise<string> {
  const bytes = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(source));
  return [...new Uint8Array(bytes)].map((value) => value.toString(16).padStart(2, '0')).join('');
}

function js(value: string): string { return JSON.stringify(value).replaceAll('<', '\\u003c'); }
function html(value: string): string { return value.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;'); }

const shim = (plugin: SandboxPlugin) => `(function() {
  const PLUGIN_ID = ${js(plugin.id)};
  const INSTANCE_ID = ${js(plugin.instanceId)};
  const NONCE = ${js(plugin.nonce)};
  const PARENT_ORIGIN = ${js(plugin.parentOrigin)};
  let nextId = 1;
  const pendingCalls = new Map();
  const eventHandlers = new Map();
  class CanopyAPIError extends Error { constructor(code, message) { super(message); this.code = code; } }
  function post(type, id, payload) { window.parent.postMessage({ type, id, target: 'host', payload, nonce: NONCE, timestamp: Date.now() }, PARENT_ORIGIN); }
  function callAPI(method, params) {
    return new Promise((resolve, reject) => {
      const id = 'call-' + nextId++;
      pendingCalls.set(id, { resolve, reject });
      post('api_call', id, { method, params });
      setTimeout(() => { if (pendingCalls.delete(id)) reject(new CanopyAPIError('TIMEOUT', 'API call ' + method + ' timed out')); }, 30000);
    });
  }
  window.addEventListener('message', (event) => {
    if (event.origin !== PARENT_ORIGIN) return;
    const msg = event.data;
    if (!msg || msg.target !== 'plugin:' + PLUGIN_ID + ':' + INSTANCE_ID || msg.nonce !== NONCE) return;
    if (msg.type === 'api_response' && pendingCalls.has(msg.id)) {
      const pending = pendingCalls.get(msg.id); pendingCalls.delete(msg.id);
      if (msg.payload && msg.payload.error) pending.reject(new CanopyAPIError(msg.payload.error.code, msg.payload.error.message));
      else pending.resolve(msg.payload && msg.payload.result);
    }
    if (msg.type === 'event' && msg.payload) (eventHandlers.get(msg.payload.event) || []).forEach((handler) => handler(msg.payload.data));
    if (msg.type === 'destroy') { pendingCalls.forEach(({ reject }) => reject(new CanopyAPIError('DESTROYED', 'Plugin sandbox destroyed'))); pendingCalls.clear(); eventHandlers.clear(); }
  });
  window.canopy = {
    version: '1.0.0', pluginId: PLUGIN_ID, instanceId: INSTANCE_ID,
    data: { getTree: (params) => callAPI('getTree', params), getNode: (params) => callAPI('getNode', params), search: (params) => callAPI('search', params), mutate: (params) => callAPI('data.mutate', params) },
    notify: (params) => callAPI('notify', params),
    calendar: { query: (params) => callAPI('calendar.query', params), create: (params) => callAPI('calendar.create', params) },
    network: { fetch: (params) => callAPI('network.fetch', params) },
    on: (event, handler) => { const handlers = eventHandlers.get(event) || []; handlers.push(handler); eventHandlers.set(event, handlers); return () => eventHandlers.set(event, handlers.filter((item) => item !== handler)); },
    emit: (event, data) => post('event', 'event-' + nextId++, { event, data }), error: CanopyAPIError,
  };
  post('ready', 'ready-' + Date.now(), { entryPoint: ${js(plugin.manifest.entry_point)} });
})();`;

export function buildSandboxDoc(plugin: SandboxPlugin): string {
  return `<!doctype html>
<html>
<head>
  <meta charset="utf-8" />
  <meta http-equiv="Content-Security-Policy"
    content="${SANDBOX_CSP}" />
  <title>${html(plugin.name)}</title>
</head>
<body>
  <div id="root"></div>
  <script>
    ${shim(plugin)}
    (function() {
      // Plugin source is evaluated in this scope
      ${plugin.sourceJS}
    })();
  </script>
</body>
</html>`;
}

interface Pending { resolve(value: unknown): void; reject(reason: Error): void; timeout: ReturnType<typeof setTimeout> }
export interface PluginSandboxHostOptions {
  pluginId: string;
  instanceId: string;
  manifest: PluginManifest;
  grantedPermissions: readonly PluginPermission[];
  api?: PluginApiHost;
  timeoutMs?: number;
  theme?: string;
  onEvent?: (event: PluginEventPayload) => void;
  onError?: (error: { code: string; message: string }) => void;
}

export class PluginSandboxHost {
  readonly nonce: string = crypto.randomUUID();
  private iframe: HTMLIFrameElement | null = null;
  private readonly pending = new Map<string, Pending>();
  private nextId = 1;
  private destroyed = false;
  private initialized = false;
  private pendingInit = false;
  private readonly api: PluginApiHost;
  private readonly options: PluginSandboxHostOptions;
  private readonly listener = (event: MessageEvent) => { void this.handleMessage(event); };

  constructor(options: PluginSandboxHostOptions) {
    this.options = options;
    this.api = options.api ?? new PluginApiHost(options.grantedPermissions);
    window.addEventListener('message', this.listener);
  }

  attach(iframe: HTMLIFrameElement): void {
    this.iframe = iframe;
    // A load event may fire before the attach effect commits; run the deferred init now.
    if (this.pendingInit) { this.pendingInit = false; this.initialize(); }
  }

  initialize(): void {
    if (this.initialized || this.destroyed) return;
    if (!this.iframe) { this.pendingInit = true; return; }
    this.initialized = true;
    this.pendingInit = false;
	this.iframe.dataset.initManifestVersion = this.options.manifest.version;
    this.post('init', `init-${this.nextId++}`, {
      pluginId: this.options.pluginId, instanceId: this.options.instanceId,
      manifest: this.options.manifest, grantedPermissions: [...this.options.grantedPermissions],
      theme: this.options.theme ?? 'light',
    });
  }

  async handleMessage(event: MessageEvent): Promise<void> {
    if (this.destroyed || !this.iframe || event.source !== this.iframe.contentWindow) return;
    const parsed = PluginMessageSchema.safeParse(event.data);
    if (!parsed.success) return;
    const message = parsed.data;
    if (message.target !== 'host' || message.nonce !== this.nonce) return;
    if (message.type === 'ready') { this.initialize(); return; }
    if (message.type === 'api_response') { this.resolvePending(message); return; }
    if (message.type === 'event') { this.options.onEvent?.(message.payload as PluginEventPayload); return; }
    if (message.type === 'error') { this.options.onError?.(message.payload as { code: string; message: string }); return; }
    if (message.type === 'api_call') await this.answerApiCall(message);
  }

  request(type: string, payload: unknown): Promise<unknown> {
    if (this.destroyed) return Promise.reject(new PluginApiError('DESTROYED', 'Plugin sandbox destroyed'));
    const id = `host-${this.nextId++}`;
    return new Promise((resolve, reject) => {
      const timeout = setTimeout(() => { this.pending.delete(id); reject(new PluginApiError('TIMEOUT', `Plugin request ${type} timed out`)); }, this.options.timeoutMs ?? 30000);
      this.pending.set(id, { resolve, reject, timeout });
      this.post(type, id, payload);
    });
  }

  emit(event: string, data?: unknown): void { this.post('event', `event-${this.nextId++}`, { event, data }); }

  teardown(): void {
    if (this.destroyed) return;
    this.post('destroy', `destroy-${this.nextId++}`, {});
    this.destroyed = true;
    window.removeEventListener('message', this.listener);
    for (const pending of this.pending.values()) { clearTimeout(pending.timeout); pending.reject(new PluginApiError('DESTROYED', 'Plugin sandbox destroyed')); }
    this.pending.clear(); this.iframe = null;
  }

  private post(type: string, id: string, payload: unknown): void {
    const message: PluginMessage = { type, id, target: `plugin:${this.options.pluginId}:${this.options.instanceId}`, payload, nonce: this.nonce, timestamp: Date.now() };
    try { this.iframe?.contentWindow?.postMessage(message, window.location.origin); }
    catch { /* The browsing context may already be gone during React unmount. */ }
  }

  private resolvePending(message: PluginMessage): void {
    const pending = this.pending.get(message.id); if (!pending) return;
    this.pending.delete(message.id); clearTimeout(pending.timeout);
    const payload = message.payload as PluginApiResponsePayload;
    if (payload.error) pending.reject(new PluginApiError(payload.error.code, payload.error.message)); else pending.resolve(payload.result);
  }

  private async answerApiCall(message: PluginMessage): Promise<void> {
    const parsed = PluginApiCallSchema.safeParse(message.payload);
    if (!parsed.success) { this.post('api_response', message.id, { error: { code: 'INVALID_REQUEST', message: parsed.error.message } }); return; }
    try { this.post('api_response', message.id, { result: await this.api.call(parsed.data.method, parsed.data.params) }); }
    catch (error) {
      const apiError = error instanceof PluginApiError ? error : new PluginApiError('INTERNAL_ERROR', error instanceof Error ? error.message : 'API call failed');
      this.post('api_response', message.id, { error: { code: apiError.code, message: apiError.message } });
    }
  }
}
