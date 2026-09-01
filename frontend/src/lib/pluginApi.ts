import { apiGet } from './api';
import type { PluginPermission } from './pluginTypes';

export class PluginApiError extends Error {
  readonly code: string;
  constructor(code: string, message: string) { super(message); this.code = code; this.name = 'PluginApiError'; }
}

export interface PluginDataApi {
  getTree(treeId: string): Promise<unknown>;
  getNode(treeId: string, nodeId: string): Promise<unknown>;
  search(treeId: string, query: string): Promise<unknown>;
}

export const defaultPluginDataApi: PluginDataApi = {
  getTree: (treeId) => apiGet(`/trees/${encodeURIComponent(treeId)}`),
  getNode: (treeId, nodeId) => apiGet(`/trees/${encodeURIComponent(treeId)}/nodes/${encodeURIComponent(nodeId)}`),
  search: (treeId, query) => apiGet(`/trees/${encodeURIComponent(treeId)}/topics/search?q=${encodeURIComponent(query)}`),
};

const REQUIRED: Record<string, PluginPermission> = { getTree: 'data_read', getNode: 'data_read', search: 'data_read', 'data.getTree': 'data_read', 'data.getNode': 'data_read', 'data.search': 'data_read' };

export class PluginApiHost {
  private readonly granted: readonly PluginPermission[];
  private readonly dataApi: PluginDataApi;
  constructor(granted: readonly PluginPermission[], dataApi: PluginDataApi = defaultPluginDataApi) { this.granted = granted; this.dataApi = dataApi; }

  async call(method: string, params: unknown): Promise<unknown> {
    const permission = REQUIRED[method];
    if (!permission) throw new PluginApiError('API_NOT_FOUND', `Unknown API method: ${method}`);
    if (!this.granted.includes(permission)) throw new PluginApiError('PERMISSION_DENIED', `Plugin not granted permission: ${permission}`);
    const p = (params ?? {}) as Record<string, unknown>;
    if (method.endsWith('getTree')) return this.dataApi.getTree(String(p.treeId ?? p.id ?? ''));
    if (method.endsWith('getNode')) return this.dataApi.getNode(String(p.treeId ?? ''), String(p.nodeId ?? p.id ?? ''));
    return this.dataApi.search(String(p.treeId ?? ''), String(p.query ?? p.q ?? ''));
  }
}
