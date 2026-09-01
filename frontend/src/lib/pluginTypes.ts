import { z } from 'zod';

export const ALL_PERMISSIONS = [
  'data_read', 'data_write', 'notification', 'calendar_read',
  'calendar_write', 'network_request',
] as const;

export type PluginPermission = (typeof ALL_PERMISSIONS)[number];
export type PluginRenderType = 'card' | 'embed' | 'background';

export interface PluginManifest {
  name: string;
  version: string;
  description: string;
  permissions: PluginPermission[];
  render_type: PluginRenderType;
  entry_point: string;
  icon_url?: string;
  homepage?: string;
  author_name?: string;
  min_canopy_version?: string;
}

export interface PluginMessage<T = unknown> {
  type: string;
  id: string;
  target: 'host' | string;
  payload: T;
  nonce: string;
  timestamp: number;
}

export interface PluginInitPayload {
  pluginId: string;
  instanceId: string;
  manifest: PluginManifest;
  grantedPermissions: PluginPermission[];
  theme: string;
}
export interface PluginApiCallPayload { method: string; params?: unknown }
export interface PluginError { code: string; message: string; stack?: string }
export interface PluginApiResponsePayload { result?: unknown; error?: PluginError }
export interface PluginEventPayload { event: string; data?: unknown }

const SEMVER_RE = /^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?(\+[a-zA-Z0-9.-]+)?$/;

export const PluginPermissionSchema = z.enum(ALL_PERMISSIONS);
const PluginRenderTypeSchema = z.enum(['card', 'embed', 'background']);

export const PluginManifestSchema = z.object({
  name: z.string().min(1).max(100),
  version: z.string().regex(SEMVER_RE, 'Version must be valid semver (e.g. 1.2.3)'),
  description: z.string().min(1).max(1000),
  permissions: z.array(PluginPermissionSchema).max(6).default([]),
  render_type: PluginRenderTypeSchema,
  entry_point: z.string().min(1).max(200),
  icon_url: z.string().url().optional(),
  homepage: z.string().url().optional(),
  author_name: z.string().max(100).optional(),
  min_canopy_version: z.string().regex(SEMVER_RE).optional(),
});

export const PluginMessageSchema = z.object({
  type: z.string().min(1),
  id: z.string().min(1),
  target: z.string().min(1),
  payload: z.unknown(),
  nonce: z.string().min(1),
  timestamp: z.number().finite(),
}) as z.ZodType<PluginMessage>;

export const PluginApiCallSchema = z.object({
  method: z.string().min(1),
  params: z.unknown().optional(),
});
export const PluginApiResponseSchema = z.object({
  result: z.unknown().optional(),
  error: z.object({ code: z.string(), message: z.string(), stack: z.string().optional() }).optional(),
});
export const PluginInitSchema = z.object({
  pluginId: z.string(),
  instanceId: z.string(),
  manifest: PluginManifestSchema,
  grantedPermissions: z.array(PluginPermissionSchema),
  theme: z.string(),
});
export const PluginEventSchema = z.object({
  event: z.string().min(1),
  data: z.unknown().optional(),
});
