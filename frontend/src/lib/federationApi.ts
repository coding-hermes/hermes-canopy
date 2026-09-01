import { apiDelete, apiGet, apiPost } from './api';

export type FederationRole = 'initiator' | 'acceptor';
export type FederationPeerState =
  | 'pending'
  | 'connected'
  | 'revoked'
  | 'disconnected'
  | 'connecting'
  | 'reconnecting'
  | 'quarantined';

export interface FederationPeer {
  id: string;
  name?: string;
  server_url: string;
  signing_key_fp?: string;
  role?: FederationRole | number;
  state: FederationPeerState;
  tree_id: string;
  connected_at: string | null;
  queue_depth?: number;
  outbound_queue_size?: number;
  last_delivery?: string | null;
}

interface FederationLinkWire {
  peer_id: string;
  tree_id: string;
  remote_server_url: string;
  state: FederationPeerState;
  connected_at: string | null;
  token?: string;
  federation_token?: string;
  name?: string;
  signing_key_fp?: string;
  role?: FederationRole | number;
  queue_depth?: number;
  outbound_queue_size?: number;
  last_delivery?: string | null;
}

export interface CreateFederationLinkRequest {
  remote_server_url: string;
  local_profile_id: string;
  tree_id: string;
  role: FederationRole;
}

export interface CreateFederationLinkResult {
  peer: FederationPeer;
  token: string | null;
}

export interface FederationHandshakeRequest {
  token: string;
  server_url: string;
  ecdhe_public_key: string;
}

export interface FederationHandshakeResponse {
  peer_id: string;
  server_url: string;
  ecdhe_public_key: string;
  signing_public_key?: string;
}

export function federationBadgeState(
  state: FederationPeerState,
): 'pending' | 'connected' | 'revoked' {
  if (state === 'connected') return 'connected';
  if (state === 'revoked') return 'revoked';
  return 'pending';
}

function fromWire(link: FederationLinkWire): FederationPeer {
  return {
    id: link.peer_id,
    name: link.name,
    server_url: link.remote_server_url,
    signing_key_fp: link.signing_key_fp,
    role: link.role,
    state: link.state,
    tree_id: link.tree_id,
    connected_at: link.connected_at,
    queue_depth: link.queue_depth,
    outbound_queue_size: link.outbound_queue_size,
    last_delivery: link.last_delivery,
  };
}

export async function listFederationPeers(): Promise<FederationPeer[]> {
  const response = await apiGet<{ links: FederationLinkWire[] }>('/federation/link');
  return (response.links ?? []).map(fromWire);
}

export async function createFederationLink(
  request: CreateFederationLinkRequest,
): Promise<CreateFederationLinkResult> {
  const { role: _role, ...backendRequest } = request;
  const response = await apiPost<FederationLinkWire>('/federation/link', backendRequest);
  return {
    peer: { ...fromWire(response), role: request.role },
    token: response.token ?? response.federation_token ?? null,
  };
}

export async function handshakeFederationPeer(
  request: FederationHandshakeRequest,
): Promise<FederationHandshakeResponse> {
  return apiPost<FederationHandshakeResponse>('/federation/handshake', request);
}

export async function revokeFederationPeer(peerId: string): Promise<void> {
  return apiDelete(`/federation/link/${encodeURIComponent(peerId)}`);
}
