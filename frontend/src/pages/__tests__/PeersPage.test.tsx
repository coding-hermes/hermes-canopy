import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import PeersPage from '../PeersPage';
import { federationBadgeState } from '../../lib/federationApi';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const links = [
  { peer_id: 'peer-connected', tree_id: 'tree-1', remote_server_url: 'https://alpha.test', name: 'Alpha', state: 'connected', signing_key_fp: 'sha256:alpha', connected_at: '2026-09-01T12:00:00Z', queue_depth: 3, last_delivery: '2026-09-01T12:05:00Z' },
  { peer_id: 'peer-pending', tree_id: 'tree-2', remote_server_url: 'https://beta.test', state: 'connecting', connected_at: null },
  { peer_id: 'peer-revoked', tree_id: 'tree-3', remote_server_url: 'https://old.test', state: 'revoked', connected_at: null },
];

function response(body: unknown, status = 200): Response {
  return { ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body), text: () => Promise.resolve(JSON.stringify(body)) } as Response;
}

let container: HTMLDivElement;
let root: Root;
let fetchMock: ReturnType<typeof vi.fn>;
let clipboardWrite: ReturnType<typeof vi.fn>;

async function settle(): Promise<void> {
  await act(async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); });
}

function button(name: string): HTMLButtonElement {
  const found = Array.from(container.querySelectorAll('button')).find((item) => item.textContent?.trim() === name || item.getAttribute('aria-label') === name);
  if (!found) throw new Error(`button not found: ${name}`);
  return found;
}

function input(label: string): HTMLInputElement | HTMLSelectElement {
  const labels = Array.from(container.querySelectorAll('label'));
  const target = labels.find((item) => item.textContent?.includes(label));
  const field = target?.querySelector('input,select');
  if (!field) throw new Error(`field not found: ${label}`);
  return field as HTMLInputElement | HTMLSelectElement;
}

async function change(field: HTMLInputElement | HTMLSelectElement, value: string) {
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(field), 'value')?.set;
    setter?.call(field, value);
    field.dispatchEvent(new Event('change', { bubbles: true }));
  });
}

beforeEach(() => {
  container = document.createElement('div'); document.body.appendChild(container); root = createRoot(container);
  fetchMock = vi.fn((url: string, init?: RequestInit) => {
    if (url === '/api/v1/federation/link' && !init?.method) return Promise.resolve(response({ links }));
    if (url === '/api/v1/federation/link' && init?.method === 'POST') return Promise.resolve(response({ peer_id: 'new-peer', tree_id: 'tree-new', remote_server_url: 'https://new.test', state: 'connecting', connected_at: null, token: 'one-time-secret' }, 201));
    if (url === '/api/v1/federation/link/peer-connected' && init?.method === 'DELETE') return Promise.resolve(response(null, 204));
    if (url === '/api/v1/federation/handshake' && init?.method === 'POST') return Promise.resolve(response({ peer_id: 'new-peer', server_url: 'https://new.test', ecdhe_public_key: 'remote-key' }));
    return Promise.reject(new Error(`unexpected fetch: ${url}`));
  });
  clipboardWrite = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal('fetch', fetchMock);
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: clipboardWrite } });
  vi.stubGlobal('confirm', vi.fn(() => true));
  act(() => root.render(createElement(MemoryRouter, null, createElement(PeersPage))));
});

afterEach(() => { act(() => root.unmount()); container.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe('PeersPage', () => {
  it('renders peers, identity details, relay health, and all state badge mappings', async () => {
    await settle();
    expect(container.textContent).toContain('Alpha');
    expect(container.textContent).toContain('sha256:alpha');
    expect(container.textContent).toContain('Queue: 3');
    expect(container.querySelectorAll('[data-testid="state-badge-connected"]')).toHaveLength(1);
    expect(container.querySelectorAll('[data-testid="state-badge-pending"]')).toHaveLength(1);
    expect(container.querySelectorAll('[data-testid="state-badge-revoked"]')).toHaveLength(1);
    expect(federationBadgeState('disconnected')).toBe('pending');
  });

  it('completes the link dialog flow, shows the token once, copies, and dismisses it', async () => {
    await settle();
    await act(async () => button('Link peer').click());
    await change(input('Server URL'), 'https://new.test');
    await change(input('Role'), 'acceptor');
    await change(input('Local profile ID'), 'profile-new');
    await change(input('Tree ID'), 'tree-new');
    await act(async () => { button('Create link').click(); await Promise.resolve(); });
    await settle();
    const post = fetchMock.mock.calls.find((call) => call[1]?.method === 'POST' && call[0] === '/api/v1/federation/link');
    expect(JSON.parse(post?.[1]?.body as string)).toEqual({ remote_server_url: 'https://new.test', local_profile_id: 'profile-new', tree_id: 'tree-new' });
    expect(container.querySelector('[data-testid="token-once"]')?.textContent).toContain('one-time-secret');
    await act(async () => { button('Copy').click(); await Promise.resolve(); });
    expect(clipboardWrite).toHaveBeenCalledWith('one-time-secret');
    await act(async () => button('Dismiss').click());
    expect(container.querySelector('[data-testid="token-once"]')).toBeNull();
  });

  it('asks for confirmation before revoking and maps the row to revoked', async () => {
    await settle();
    await act(async () => { button('Revoke Alpha').click(); await Promise.resolve(); });
    await settle();
    expect(confirm).toHaveBeenCalledWith('Revoke federation link to Alpha?');
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/federation/link/peer-connected', { method: 'DELETE' });
    expect(container.querySelectorAll('[data-testid="state-badge-revoked"]')).toHaveLength(2);
  });

  it('does not revoke when confirmation is declined', async () => {
    vi.mocked(confirm).mockReturnValue(false);
    await settle();
    await act(async () => button('Revoke Alpha').click());
    expect(fetchMock.mock.calls.some((call) => call[1]?.method === 'DELETE')).toBe(false);
  });

  it('runs a WebCrypto handshake for a newly linked peer', async () => {
    const generateKey = vi.fn().mockResolvedValue({ publicKey: {}, privateKey: {} });
    const exportKey = vi.fn().mockResolvedValue(new Uint8Array([1, 2, 3]).buffer);
    Object.defineProperty(globalThis.crypto, 'subtle', { configurable: true, value: { generateKey, exportKey } });
    await settle();
    await act(async () => button('Link peer').click());
    await change(input('Server URL'), 'https://new.test'); await change(input('Local profile ID'), 'profile-new'); await change(input('Tree ID'), 'tree-new');
    await act(async () => { button('Create link').click(); await Promise.resolve(); }); await settle();
    await act(async () => { button('Handshake https://new.test').click(); await Promise.resolve(); await Promise.resolve(); }); await settle();
    expect(generateKey).toHaveBeenCalledWith({ name: 'X25519' }, true, ['deriveBits']);
    const request = fetchMock.mock.calls.find((call) => call[0] === '/api/v1/federation/handshake');
    expect(JSON.parse(request?.[1]?.body as string)).toMatchObject({ token: 'one-time-secret', server_url: 'https://new.test', ecdhe_public_key: 'AQID' });
  });
});
