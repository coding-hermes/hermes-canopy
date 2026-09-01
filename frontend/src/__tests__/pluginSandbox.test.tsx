import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import PluginHost from '../components/PluginHost';
import { PluginApiHost } from '../lib/pluginApi';
import { buildSandboxDoc, PluginSandboxHost, SANDBOX_CSP, type SandboxPlugin } from '../lib/pluginSandbox';
import { PluginManifestSchema, type PluginManifest, type PluginMessage } from '../lib/pluginTypes';

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
});
