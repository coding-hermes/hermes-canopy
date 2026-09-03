import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import PluginHost from '../components/PluginHost';
import { PluginApiHost } from '../lib/pluginApi';
import { buildSandboxDoc, PluginSandboxHost, SANDBOX_CSP, sha256Hex, type SandboxPlugin } from '../lib/pluginSandbox';
import { PluginManifestSchema, type PluginManifest, type PluginMessage } from '../lib/pluginTypes';

async function waitForDom(check: () => void, timeoutMs = 2000): Promise<void> {
  const start = Date.now();
  for (;;) {
    try { check(); return; } catch { if (Date.now() - start > timeoutMs) throw new Error('waitForDom timeout'); }
    await new Promise((r) => setTimeout(r, 10));
  }
}

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const manifest: PluginManifest = { name: 'test-plugin', version: '1.0.0', description: 'Test', permissions: ['data_read'], render_type: 'card', entry_point: 'main' };
const sandboxPlugin: SandboxPlugin = { id: 'plugin-1', instanceId: 'instance-1', name: 'Test Plugin', manifest, sourceJS: 'window.pluginLoaded = true;', nonce: 'secret', parentOrigin: window.location.origin };

function envelope(host: PluginSandboxHost, type: string, id: string, payload: unknown, nonce = host.nonce): PluginMessage {
  return { type, id, target: 'host', payload, nonce, timestamp: Date.now() };
}

function mountedHost(options: Partial<ConstructorParameters<typeof PluginSandboxHost>[0]> = {}) {
  const iframe = document.createElement('iframe'); document.body.appendChild(iframe);
  const host = new PluginSandboxHost({ pluginId: 'plugin-1', instanceId: 'instance-1', manifest, grantedPermissions: ['data_read'], ...options });
  host.attach(iframe);
  const post = vi.spyOn(iframe.contentWindow!, 'postMessage');
  const send = (message: PluginMessage) => host.handleMessage(new MessageEvent('message', { data: message, source: iframe.contentWindow }));
  return { iframe, host, post, send };
}

describe('plugin sandbox document', () => {
  it('rejects a manifest with a permission outside the canonical set', () => {
    expect(PluginManifestSchema.safeParse({ ...manifest, permissions: ['filesystem_read'] }).success).toBe(false);
  });

  it('contains every CSP directive verbatim and wraps the plugin source in an IIFE', () => {
    const doc = buildSandboxDoc(sandboxPlugin);
    expect(doc).toContain(SANDBOX_CSP);
    for (const directive of ["default-src 'none';", "script-src 'unsafe-inline';", "style-src 'unsafe-inline';", 'connect-src none;', 'img-src data: https:;', 'font-src data:;']) expect(doc).toContain(directive);
    expect(doc).toContain('(function() {\n      // Plugin source is evaluated in this scope\n      window.pluginLoaded = true;');
  });
});

describe('PluginSandboxHost', () => {
  const cleanups: Array<() => void> = [];
  afterEach(() => { cleanups.splice(0).forEach((cleanup) => cleanup()); vi.restoreAllMocks(); });

  it('ignores forged and missing nonces', async () => {
    const { iframe, host, post, send } = mountedHost(); cleanups.push(() => { host.teardown(); iframe.remove(); });
    await send(envelope(host, 'ready', 'forged', {}, 'wrong'));
    await send({ ...envelope(host, 'ready', 'missing', {}), nonce: '' });
    expect(post).not.toHaveBeenCalled();
  });

  it('correlates a response to the right pending request', async () => {
    const { iframe, host, post, send } = mountedHost(); cleanups.push(() => { host.teardown(); iframe.remove(); });
    const first = host.request('first', {}); const second = host.request('second', {});
    const messages = post.mock.calls.map((call) => call[0] as PluginMessage);
    await send(envelope(host, 'api_response', messages[1].id, { result: 'second-result' }));
    await send(envelope(host, 'api_response', messages[0].id, { result: 'first-result' }));
    await expect(first).resolves.toBe('first-result'); await expect(second).resolves.toBe('second-result');
  });

  it('removes its listener on teardown', async () => {
    const remove = vi.spyOn(window, 'removeEventListener');
    const { iframe, host, post } = mountedHost(); host.teardown();
    expect(remove).toHaveBeenCalledWith('message', expect.any(Function));
    window.dispatchEvent(new MessageEvent('message', { data: envelope(host, 'ready', 'late', {}), source: iframe.contentWindow }));
    expect(post).toHaveBeenCalledTimes(1); // destroy only
    iframe.remove();
  });
});

describe('PluginApiHost', () => {
  it('returns a structured permission denial before dispatch', async () => {
    const getTree = vi.fn();
    const api = new PluginApiHost([], { getTree, getNode: vi.fn(), search: vi.fn() });
    await expect(api.call('getTree', { treeId: 'tree-1' })).rejects.toMatchObject({ code: 'PERMISSION_DENIED', message: 'Plugin not granted permission: data_read' });
    expect(getTree).not.toHaveBeenCalled();
  });

  it('gates mutations and only updates or deletes nodes created by this instance', async () => {
    const dataApi = { getTree: vi.fn(), getNode: vi.fn(), search: vi.fn(), createNode: vi.fn().mockResolvedValue({ node: { id: 'own' } }), updateNode: vi.fn(), deleteNode: vi.fn() };
    await expect(new PluginApiHost([], dataApi).call('data.mutate', { collection: 'nodes', op: 'insert', document: { treeId: 'tree', content: 'x' } })).rejects.toMatchObject({ code: 'PERMISSION_DENIED' });
    const api = new PluginApiHost(['data_write'], dataApi);
    await expect(api.call('data.mutate', { collection: 'nodes', op: 'update', id: 'foreign', document: {} })).rejects.toMatchObject({ code: 'PERMISSION_DENIED' });
    await api.call('data.mutate', { collection: 'nodes', op: 'insert', document: { treeId: 'tree', content: 'x' } });
    await api.call('data.mutate', { collection: 'nodes', op: 'update', id: 'own', document: { content: 'y' } });
    await api.call('data.mutate', { collection: 'nodes', op: 'delete', id: 'own' });
    expect(dataApi.updateNode).toHaveBeenCalledWith('own', { content: 'y' });
    expect(dataApi.deleteNode).toHaveBeenCalledWith('own');
  });

  it('delivers typed notifications with their ttl to the toast bus and desktop API', async () => {
    const onNotify = vi.fn(); const desktop = vi.fn(); Object.assign(desktop, { permission: 'granted' }); vi.stubGlobal('Notification', desktop);
    const api = new PluginApiHost(['notification'], undefined, onNotify);
    await api.call('notify', { title: 'Done', body: 'Finished', level: 'success', ttl_seconds: 2 });
    expect(onNotify).toHaveBeenCalledWith(expect.objectContaining({ level: 'success', ttl_seconds: 2 }));
    expect(desktop).toHaveBeenCalledWith('Done', { body: 'Finished' });
    vi.unstubAllGlobals();
  });

  it('checks calendar permissions before returning the structured MVP error', async () => {
    await expect(new PluginApiHost([]).call('calendar.query', {})).rejects.toMatchObject({ code: 'PERMISSION_DENIED' });
    await expect(new PluginApiHost(['calendar_read']).call('calendar.query', {})).rejects.toMatchObject({ code: 'NOT_IMPLEMENTED', message: 'Calendar subsystem is not part of Canopy MVP' });
    await expect(new PluginApiHost(['calendar_write']).call('calendar.create', {})).rejects.toMatchObject({ code: 'NOT_IMPLEMENTED' });
  });

  it('posts network.fetch through the authenticated proxy and returns JSON', async () => {
    const result = { status: 200, statusText: 'OK', headers: {}, body: 'ok', durationMs: 1 };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(result), { status: 200, headers: { 'Content-Type': 'application/json' } })); vi.stubGlobal('fetch', fetchMock);
    await expect(new PluginApiHost(['network_request']).call('network.fetch', { url: 'https://example.com' })).resolves.toEqual(result);
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/plugins/network-proxy', expect.objectContaining({ method: 'POST', credentials: 'include' }));
    vi.unstubAllGlobals();
  });
});

describe('PluginHost iframe', () => {
  let container: HTMLDivElement; let root: Root;
  beforeEach(() => { container = document.createElement('div'); document.body.appendChild(container); root = createRoot(container); });
  afterEach(() => { act(() => root.unmount()); container.remove(); });
  it('uses only allow-scripts and a no-referrer policy', async () => {
    await act(async () => { root.render(<PluginHost plugin={{ id: 'plugin-1', name: 'Test Plugin', manifest, sourceJS: 'void 0;' }} instanceId="instance-1" />); await Promise.resolve(); });
    const iframe = container.querySelector('iframe');
    expect(iframe?.getAttribute('sandbox')).toBe('allow-scripts');
    expect(iframe?.getAttribute('sandbox')).not.toContain('allow-same-origin');
    expect(iframe?.getAttribute('referrerpolicy')).toBe('no-referrer');
    expect(iframe?.getAttribute('name')).toBe('canopy-plugin-plugin-1-instance-1');
  });
  it('renders and expires an in-app toast from the notify API', async () => {
    vi.useFakeTimers();
    const notificationManifest: PluginManifest = { ...manifest, permissions: ['notification'] };
    await act(async () => { root.render(<PluginHost plugin={{ id: 'plugin-1', name: 'Test Plugin', manifest: notificationManifest, sourceJS: 'void 0;' }} instanceId="instance-1" />); await Promise.resolve(); });
    const iframe = container.querySelector('iframe')!;
    const nonce = iframe.srcdoc.match(/const NONCE = "([^"]+)"/)?.[1];
    await act(async () => { window.dispatchEvent(new MessageEvent('message', { source: iframe.contentWindow, data: { type: 'api_call', id: 'notify-1', target: 'host', nonce, timestamp: Date.now(), payload: { method: 'notify', params: { title: 'Saved', body: 'Complete', level: 'success', ttl_seconds: 2 } } } })); await Promise.resolve(); });
    expect(container.querySelector('[role="alert"]')?.textContent).toContain('Saved');
    act(() => vi.advanceTimersByTime(2000));
    expect(container.querySelector('[role="alert"]')).toBeNull();
    vi.useRealTimers();
  });
  it('hot reloads an updated source and initializes the new manifest', async () => {
    const stream = new EventTarget() as EventSource;
    const source = '/*@@canopy.manifest@@ {"name":"test-plugin","version":"2.0.0","description":"Test","permissions":["data_read"],"render_type":"card","entry_point":"main"} @@end@@*/ void 0;';
    const digest = await sha256Hex(source);
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(source, { status: 200 })));
    await act(async () => { root.render(<PluginHost plugin={{ id: 'plugin-1', name: 'Test Plugin', manifest, sourceJS: 'void 0;' }} instanceId="instance-1" eventSource={stream} />); await Promise.resolve(); });
    const oldIframe = container.querySelector('iframe');
    await act(async () => { stream.dispatchEvent(new MessageEvent('plugin_updated', { data: JSON.stringify({ plugin_id:'plugin-1',version:'2.0.0',source_sha256:digest }) })); });
    let nextIframe: HTMLIFrameElement | null = null;
    await waitForDom(() => {
      nextIframe = container.querySelector('iframe');
      if (!nextIframe || nextIframe === oldIframe) throw new Error('iframe not swapped yet');
      if (!nextIframe.srcdoc.includes('"version":"2.0.0"')) throw new Error('new manifest not in srcdoc yet');
    });
    // Effects (host re-attach) and the load handler are act-deferred; wait for the
    // observable outcome (initialize stamping the dataset) instead of a fixed instant.
    await act(async () => { nextIframe!.dispatchEvent(new Event('load')); });
    await waitForDom(() => { expect(nextIframe!.dataset.initManifestVersion).toBe('2.0.0'); });
    vi.unstubAllGlobals();
  });
});
