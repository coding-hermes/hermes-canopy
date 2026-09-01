import { useCallback, useEffect, useState } from 'react';
import { AlertCircle, Copy, KeyRound, Link2, RefreshCw, ShieldX, X } from 'lucide-react';
import {
  createFederationLink,
  handshakeFederationPeer,
  listFederationPeers,
  revokeFederationPeer,
  federationBadgeState,
  type FederationPeer,
  type FederationPeerState,
  type FederationRole,
} from '../lib/federationApi';

const STATE_STYLES: Record<'pending' | 'connected' | 'revoked', string> = {
  pending: 'border-amber-400/30 bg-amber-400/10 text-amber-300',
  connected: 'border-emerald-400/30 bg-emerald-400/10 text-emerald-300',
  revoked: 'border-zinc-400/30 bg-zinc-400/10 text-zinc-300',
};

function StateBadge({ state }: { state: FederationPeerState }) {
  const mapped = federationBadgeState(state);
  return (
    <span data-testid={`state-badge-${mapped}`} className={`rounded-full border px-2 py-0.5 text-[11px] font-medium ${STATE_STYLES[mapped]}`}>
      {mapped}
    </span>
  );
}

function formatDate(value: string | null | undefined): string {
  if (!value) return 'Never';
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString();
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = '';
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

async function generateECDHPublicKey(): Promise<string> {
  const pair = (await crypto.subtle.generateKey({ name: 'X25519' }, true, ['deriveBits'])) as CryptoKeyPair;
  const raw = await crypto.subtle.exportKey('raw', pair.publicKey);
  return bytesToBase64(new Uint8Array(raw));
}

interface LinkDialogProps {
  onClose: () => void;
  onCreated: (peer: FederationPeer, token: string | null) => void;
}

function LinkPeerDialog({ onClose, onCreated }: LinkDialogProps) {
  const [serverUrl, setServerUrl] = useState('');
  const [role, setRole] = useState<FederationRole>('initiator');
  const [profileId, setProfileId] = useState('');
  const [treeId, setTreeId] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const result = await createFederationLink({
        remote_server_url: serverUrl.trim(),
        local_profile_id: profileId.trim(),
        tree_id: treeId.trim(),
        role,
      });
      onCreated(result.peer, result.token);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to link peer');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center pt-[10vh]" role="dialog" aria-modal="true" aria-labelledby="link-peer-title">
      <button className="absolute inset-0 bg-black/60" aria-label="Close link dialog" onClick={onClose} />
      <form onSubmit={submit} className="glass-raised relative mx-4 w-full max-w-md rounded-xl">
        <div className="flex items-center border-b border-line-subtle px-5 py-4">
          <h2 id="link-peer-title" className="text-sm font-medium text-content-primary">Link peer</h2>
          <button type="button" onClick={onClose} aria-label="Close" className="ml-auto rounded p-1 text-content-muted hover:bg-surface-hover"><X className="h-4 w-4" /></button>
        </div>
        <div className="space-y-3 px-5 py-4">
          {error && <div role="alert" className="flex gap-2 rounded border border-rose-500/30 bg-rose-500/10 p-2 text-xs text-status-danger"><AlertCircle className="h-4 w-4" />{error}</div>}
          <label className="block text-xs text-content-muted">Server URL
            <input required type="url" value={serverUrl} onChange={(e) => setServerUrl(e.target.value)} placeholder="https://canopy.example.com" className="mt-1 w-full rounded-lg border border-line-subtle bg-surface-input px-3 py-2 text-sm text-content-primary" />
          </label>
          <label className="block text-xs text-content-muted">Role
            <select value={role} onChange={(e) => setRole(e.target.value as FederationRole)} className="mt-1 w-full rounded-lg border border-line-subtle bg-surface-input px-3 py-2 text-sm text-content-primary">
              <option value="initiator">Initiator</option><option value="acceptor">Acceptor</option>
            </select>
          </label>
          <label className="block text-xs text-content-muted">Local profile ID
            <input required value={profileId} onChange={(e) => setProfileId(e.target.value)} className="mt-1 w-full rounded-lg border border-line-subtle bg-surface-input px-3 py-2 text-sm text-content-primary" />
          </label>
          <label className="block text-xs text-content-muted">Tree ID
            <input required value={treeId} onChange={(e) => setTreeId(e.target.value)} className="mt-1 w-full rounded-lg border border-line-subtle bg-surface-input px-3 py-2 text-sm text-content-primary" />
          </label>
        </div>
        <div className="flex justify-end gap-2 border-t border-line-subtle px-5 py-3">
          <button type="button" onClick={onClose} className="rounded-lg px-3 py-2 text-sm text-content-muted hover:bg-surface-hover">Cancel</button>
          <button disabled={busy} className="rounded-lg bg-accent-2-600 px-3 py-2 text-sm font-medium text-white disabled:opacity-50">{busy ? 'Linking…' : 'Create link'}</button>
        </div>
      </form>
    </div>
  );
}

export default function PeersPage() {
  const [peers, setPeers] = useState<FederationPeer[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [token, setToken] = useState<{ peerId: string; value: string } | null>(null);
  const [copied, setCopied] = useState(false);
  const [busyPeer, setBusyPeer] = useState<string | null>(null);

  const loadPeers = useCallback(async () => {
    setLoading(true); setError(null);
    try { setPeers(await listFederationPeers()); }
    catch (err) { setError(err instanceof Error ? err.message : 'Failed to load peers'); }
    finally { setLoading(false); }
  }, []);

  useEffect(() => { void loadPeers(); }, [loadPeers]);

  const revoke = async (peer: FederationPeer) => {
    if (!window.confirm(`Revoke federation link to ${peer.name ?? peer.server_url}?`)) return;
    setBusyPeer(peer.id); setError(null);
    try {
      await revokeFederationPeer(peer.id);
      setPeers((current) => current.map((item) => item.id === peer.id ? { ...item, state: 'revoked' } : item));
      if (token?.peerId === peer.id) setToken(null);
    } catch (err) { setError(err instanceof Error ? err.message : 'Failed to revoke peer'); }
    finally { setBusyPeer(null); }
  };

  const handshake = async (peer: FederationPeer) => {
    if (!token || token.peerId !== peer.id) { setError('Handshake token is no longer available. Create a new link to handshake.'); return; }
    setBusyPeer(peer.id); setError(null);
    try {
      const publicKey = await generateECDHPublicKey();
      await handshakeFederationPeer({ token: token.value, server_url: peer.server_url, ecdhe_public_key: publicKey });
      setPeers((current) => current.map((item) => item.id === peer.id ? { ...item, state: 'connected' } : item));
    } catch (err) { setError(err instanceof Error ? err.message : 'Handshake failed'); }
    finally { setBusyPeer(null); }
  };

  return (
    <div className="mx-auto max-w-6xl p-6">
      <div className="mb-6 flex items-center gap-3">
        <div><h1 className="text-2xl font-bold text-content-primary">Federation peers</h1><p className="mt-1 text-sm text-content-muted">Manage trusted Canopy server links and relay health.</p></div>
        <button onClick={() => void loadPeers()} aria-label="Refresh peers" className="ml-auto rounded-lg border border-line-subtle p-2 text-content-muted hover:bg-surface-hover"><RefreshCw className="h-4 w-4" /></button>
        <button onClick={() => setDialogOpen(true)} className="flex items-center gap-2 rounded-lg bg-accent-2-600 px-3 py-2 text-sm font-medium text-white"><Link2 className="h-4 w-4" />Link peer</button>
      </div>
      {error && <div role="alert" className="mb-4 flex gap-2 rounded-lg border border-rose-500/30 bg-rose-500/10 p-3 text-sm text-status-danger"><AlertCircle className="h-4 w-4" />{error}</div>}
      {token && <div data-testid="token-once" className="mb-4 rounded-lg border border-amber-400/30 bg-amber-400/10 p-4"><p className="font-medium text-amber-200">Copy this federation token now</p><p className="mt-1 text-xs text-amber-100/80">It is shown once and cannot be recovered after dismissal.</p><div className="mt-3 flex gap-2"><code className="min-w-0 flex-1 overflow-hidden text-ellipsis rounded bg-black/20 p-2 text-xs text-content-primary">{token.value}</code><button onClick={async () => { await navigator.clipboard.writeText(token.value); setCopied(true); }} className="flex items-center gap-1 rounded border border-amber-300/30 px-2 text-xs text-amber-100"><Copy className="h-3.5 w-3.5" />{copied ? 'Copied' : 'Copy'}</button><button onClick={() => setToken(null)} className="rounded px-2 text-xs text-amber-100">Dismiss</button></div></div>}
      <div className="overflow-hidden rounded-xl border border-line-subtle bg-surface-panel">
        {loading ? <p className="p-8 text-center text-sm text-content-muted">Loading peers…</p> : peers.length === 0 ? <p className="p-8 text-center text-sm text-content-muted">No federation peers linked.</p> : <div className="divide-y divide-line-subtle">{peers.map((peer) => <article key={peer.id} data-testid="peer-row" className="grid gap-4 p-4 md:grid-cols-[2fr_1fr_1fr_auto] md:items-center"><div className="min-w-0"><div className="flex items-center gap-2"><h2 className="truncate text-sm font-medium text-content-primary">{peer.name ?? peer.server_url}</h2><StateBadge state={peer.state} /></div>{peer.name && <p className="truncate text-xs text-content-muted">{peer.server_url}</p>}<p className="mt-2 font-mono text-[11px] text-content-faint">Key: {peer.signing_key_fp ?? 'Unavailable'}</p></div><dl className="text-xs"><dt className="text-content-faint">Connected</dt><dd className="mt-1 text-content-secondary">{formatDate(peer.connected_at)}</dd></dl><dl className="text-xs"><dt className="text-content-faint">Relay health</dt><dd className="mt-1 text-content-secondary">Queue: {peer.queue_depth ?? peer.outbound_queue_size ?? 0}</dd><dd className="text-content-muted">Last: {formatDate(peer.last_delivery)}</dd></dl><div className="flex gap-2"><button disabled={busyPeer === peer.id || peer.state === 'revoked'} onClick={() => void handshake(peer)} aria-label={`Handshake ${peer.name ?? peer.server_url}`} className="rounded border border-line-subtle p-2 text-content-muted hover:bg-surface-hover disabled:opacity-40"><KeyRound className="h-4 w-4" /></button><button disabled={busyPeer === peer.id || peer.state === 'revoked'} onClick={() => void revoke(peer)} aria-label={`Revoke ${peer.name ?? peer.server_url}`} className="rounded border border-line-subtle p-2 text-content-muted hover:bg-rose-500/10 hover:text-status-danger disabled:opacity-40"><ShieldX className="h-4 w-4" /></button></div></article>)}</div>}
      </div>
      {dialogOpen && <LinkPeerDialog onClose={() => setDialogOpen(false)} onCreated={(peer, newToken) => { setPeers((current) => [peer, ...current]); setDialogOpen(false); setCopied(false); if (newToken) setToken({ peerId: peer.id, value: newToken }); else setError('The server created the link but did not return its one-time token.'); }} />}
    </div>
  );
}
