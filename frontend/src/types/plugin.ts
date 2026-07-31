/**
 * Hermes Canopy — Plugin Sandbox Types
 *
 * Mirrors the backend `internal/plugin` Go structs (SPEC-IMPL-GAP-002 §2).
 * `source` is optional client-side: the registry API omits it (json:"-" on
 * the backend); fetch it via GET /api/v1/plugins/{id}/source before mounting
 * a PluginSandbox.
 */

// ─── Enums ────────────────────────────────────────────────────────────

export type Permission =
  | 'data_read'
  | 'data_write'
  | 'notification'
  | 'calendar_read'
  | 'calendar_write'
  | 'network_request';

export type PluginStatus = 'active' | 'disabled' | 'archived';

export type PluginRenderType = 'card' | 'embed' | 'background';

export type InstanceStatus = 'active' | 'paused' | 'uninstalled';

// ─── Core Entities ────────────────────────────────────────────────────

/** Parsed @canopy-manifest block from the plugin source header. */
export interface PluginManifest {
  name: string;
  version: string; // semver
  description: string;
  permissions: Permission[];
  render_type: PluginRenderType;
  entry_point: string; // e.g. "main"
  icon_url?: string;
  author_name?: string;
  min_canopy_version?: string;
}

/** A row in plugin_registry. Matches backend `Plugin` struct. */
export interface Plugin {
  id: string; // UUIDv7
  name: string;
  slug: string;
  version: string;
  description: string;
  authorProfileId: string;
  permissions: Permission[];
  manifest: PluginManifest;
  /** Raw plugin JS — NOT included in registry responses; fetch via /source. */
  source?: string;
  sourceSha256: string;
  sourceByteSize: number;
  iconUrl: string;
  status: PluginStatus;
  installCount?: number;
  isRootVersion?: boolean;
  previousVersionId?: string | null;
  createdAt: string; // ISO 8601
  updatedAt: string;
}

/** A per-tree/per-user install of a plugin (plugin_instances row). */
export interface PluginInstance {
  id: string;
  pluginId: string;
  treeId?: string | null; // null = globally available to this user
  profileId: string;
  instanceName: string;
  settings: Record<string, unknown>;
  grantedPermissions: Permission[];
  status: InstanceStatus;
  invokeCount: number;
  createdAt: string;
}

// ─── Sandbox messaging (SPEC-PL-01 §7.2) ──────────────────────────────

/** Envelope for every host↔plugin postMessage. */
export interface PluginMessage {
  type: string; // "api_call" | "api_response" | "init" | "ready" | "error" | ...
  id: string; // matches request/response pairs
  target: 'host' | string; // 'plugin:{pluginId}:{instanceId}' for host→plugin
  payload: Record<string, unknown>;
  nonce: string; // per-session; validated by both sides
  timestamp: number;
}

/** Maps an API method to its required permission (SPEC-PL-01 §7.5). */
export function methodToPermission(method: string): Permission | '' {
  switch (method) {
    case 'data.query':
      return 'data_read';
    case 'data.mutate':
      return 'data_write';
    case 'notify':
      return 'notification';
    case 'calendar.query':
      return 'calendar_read';
    case 'calendar.create':
      return 'calendar_write';
    case 'network.fetch':
      return 'network_request';
    default:
      return '';
  }
}
