import { apiDelete, apiGet, apiPatch, apiPost, apiUrl } from './api';
import type { PluginPermission } from './pluginTypes';

export class PluginApiError extends Error {
  readonly code: string;
  constructor(code: string, message: string) { super(message); this.code = code; this.name = 'PluginApiError'; }
}

export interface PluginDataApi {
  getTree(treeId: string): Promise<unknown>;
  getNode(treeId: string, nodeId: string): Promise<unknown>;
  search(treeId: string, query: string): Promise<unknown>;
  createNode?(treeId: string, document: Record<string, unknown>): Promise<unknown>;
  updateNode?(nodeId: string, document: Record<string, unknown>): Promise<unknown>;
  deleteNode?(nodeId: string): Promise<unknown>;
}

export interface PluginNotification {
  title: string; body: string; level: 'info' | 'success' | 'warning' | 'error';
  actions?: Array<{ label: string; type: 'primary' | 'secondary' | 'destructive'; payload: Record<string, unknown> }>;
  ttl_seconds: number; persistent?: boolean;
}

export const defaultPluginDataApi: PluginDataApi = {
  getTree: (treeId) => apiGet(`/trees/${encodeURIComponent(treeId)}`),
  getNode: (treeId, nodeId) => apiGet(`/trees/${encodeURIComponent(treeId)}/nodes/${encodeURIComponent(nodeId)}`),
  search: (treeId, query) => apiGet(`/trees/${encodeURIComponent(treeId)}/topics/search?q=${encodeURIComponent(query)}`),
  createNode: (treeId, document) => apiPost(`/trees/${encodeURIComponent(treeId)}/nodes`, document),
  updateNode: (nodeId, document) => apiPatch(`/nodes/${encodeURIComponent(nodeId)}`, document),
  deleteNode: (nodeId) => apiDelete(`/nodes/${encodeURIComponent(nodeId)}`),
};

const REQUIRED: Record<string, PluginPermission> = { getTree: 'data_read', getNode: 'data_read', search: 'data_read', 'data.getTree': 'data_read', 'data.getNode': 'data_read', 'data.search': 'data_read', 'data.mutate': 'data_write', notify: 'notification', 'calendar.query': 'calendar_read', 'calendar.create': 'calendar_write', 'network.fetch': 'network_request' };

export class PluginApiHost {
  private readonly createdNodeIds = new Set<string>();
  private readonly granted: readonly PluginPermission[];
  private readonly dataApi: PluginDataApi;
  private readonly onNotify?: (notification: PluginNotification) => void;
  constructor(granted: readonly PluginPermission[], dataApi: PluginDataApi = defaultPluginDataApi, onNotify?: (notification: PluginNotification) => void) {
    this.granted = granted; this.dataApi = dataApi; this.onNotify = onNotify;
  }

  async call(method: string, params: unknown): Promise<unknown> {
    const permission = REQUIRED[method];
    if (!permission) throw new PluginApiError('API_NOT_FOUND', `Unknown API method: ${method}`);
    if (!this.granted.includes(permission)) throw new PluginApiError('PERMISSION_DENIED', `Plugin not granted permission: ${permission}`);
    const p = (params ?? {}) as Record<string, unknown>;
    if (method === 'data.mutate') return this.mutate(p);
    if (method === 'notify') return this.notify(p);
    if (method.startsWith('calendar.')) throw new PluginApiError('NOT_IMPLEMENTED', 'Calendar subsystem is not part of Canopy MVP');
    if (method === 'network.fetch') return this.networkFetch(p);
    if (method.endsWith('getTree')) return this.dataApi.getTree(String(p.treeId ?? p.id ?? ''));
    if (method.endsWith('getNode')) return this.dataApi.getNode(String(p.treeId ?? ''), String(p.nodeId ?? p.id ?? ''));
    return this.dataApi.search(String(p.treeId ?? ''), String(p.query ?? p.q ?? ''));
  }

  private async mutate(p: Record<string, unknown>): Promise<unknown> {
    const collection = String(p.collection ?? '');
    if (collection !== 'nodes') throw new PluginApiError('NOT_IMPLEMENTED', `Mutation collection is not implemented: ${collection}`);
    const op = String(p.op ?? '');
    const document = (p.document ?? {}) as Record<string, unknown>;
    if (op === 'insert') {
      const treeId = String(document.treeId ?? document.tree_id ?? p.treeId ?? '');
      const wireDocument = { ...document }; delete wireDocument.treeId; delete wireDocument.tree_id;
      const response = await this.dataApi.createNode?.(treeId, wireDocument) as { id?: string; node?: { id?: string } } | undefined;
      const id = String(response?.node?.id ?? response?.id ?? '');
      if (id) this.createdNodeIds.add(id);
      return { id, op, collection, timestamp: new Date().toISOString() };
    }
    const id = String(p.id ?? document.id ?? '');
    // PL-01 v2 known gap: server-side enforcement requires plugin instance identity.
    if (!this.createdNodeIds.has(id)) throw new PluginApiError('PERMISSION_DENIED', 'Plugins may only update or delete nodes they created');
    if (op === 'update') { const wireDocument = { ...document }; delete wireDocument.id; await this.dataApi.updateNode?.(id, wireDocument); }
    else if (op === 'delete') await this.dataApi.deleteNode?.(id);
    else throw new PluginApiError('INVALID_REQUEST', `Unknown mutation operation: ${op}`);
    return { id, op, collection, timestamp: new Date().toISOString() };
  }

  private notify(p: Record<string, unknown>): { delivered: true } {
    const level = String(p.level ?? '');
    if (!['info', 'success', 'warning', 'error'].includes(level)) throw new PluginApiError('INVALID_REQUEST', 'Notification level is invalid');
    const actions = p.actions as PluginNotification['actions'];
    if (actions && (!Array.isArray(actions) || actions.length > 3)) throw new PluginApiError('INVALID_REQUEST', 'Notifications support at most 3 actions');
    const notification: PluginNotification = { title: String(p.title ?? ''), body: String(p.body ?? ''), level: level as PluginNotification['level'], actions, ttl_seconds: Number(p.ttl_seconds ?? 10), persistent: Boolean(p.persistent) };
    this.onNotify?.(notification);
    if (typeof window !== 'undefined' && 'Notification' in window && window.Notification.permission === 'granted') new window.Notification(notification.title, { body: notification.body });
    return { delivered: true };
  }

  private async networkFetch(p: Record<string, unknown>): Promise<unknown> {
    const response = await fetch(apiUrl('/plugins/network-proxy'), { method: 'POST', credentials: 'include', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(p) });
    const value = await response.json();
    if (!response.ok) throw new PluginApiError((value as { error?: { code?: string } }).error?.code ?? 'NETWORK_ERROR', (value as { error?: { message?: string } }).error?.message ?? `HTTP ${response.status}`);
    return value;
  }
}
