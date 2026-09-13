/**
 * Hermes Canopy — file viewer types & validation (SPEC-PL-02 §5).
 *
 * The TypeScript interfaces are the spec §5.1 definitions verbatim. The zod
 * schemas validate the JSON as actually served by internal/fileviewer
 * (PL02-P1, commit 4ac2f15), which differs from the spec's illustrative
 * §5.2 snippets in three live-backend ways, tolerated here on purpose:
 *   1. nil Go slices marshal as `null` (supportsViewerHint,
 *      requiredCapabilities) — normalized to [].
 *   2. nil Go maps / []byte marshal as `null` (metadata, clientInfo,
 *      config) — normalized to {}.
 *   3. bundleSha256 is seeded empty in phase 1 — '' is accepted alongside
 *      a SHA-256 hex digest.
 * Timestamps use .datetime({ offset: true }) because Go time.Time marshals
 * RFC 3339 with a numeric UTC offset (RFC 3339Nano), which plain
 * .datetime() (Z-only) would reject.
 */

import { z } from 'zod';

// ── Storage & Source ────────────────────────────────────────

export type FileStorageKind = 'hermes_kb' | 'hermes_fs_ref' | 'external';
export type FileSourceKind = 'upload' | 'reference' | 'agent_message' | 'import';

// ── File Metadata (spec §5.1) ───────────────────────────────

export interface FileMetadata {
  id: string; // UUIDv7
  profileId: string;
  sha256: string; // hex SHA-256
  byteSize: number; // bytes
  mimeType: string;
  declaredMime: string;
  filename: string;
  extension: string;
  storageKind: FileStorageKind;
  sourceKind: FileSourceKind;
  sourceMessageId?: string;
  sourceExternalUrl?: string;
  isText: boolean;
  isBinary: boolean;
  isViewable: boolean;
  viewerHint: string;
  thumbnailPath?: string;
  thumbnailSha256?: string;
  previewText?: string;
  metadata: Record<string, unknown>; // type-specific
  referenceCount: number;
  lastAccessedAt?: string;
  accessCount: number;
  quarantined: boolean;
  createdAt: string;
  updatedAt: string;
  deletedAt?: string;
}

export interface FileMetadataSlim {
  id: string;
  profileId: string;
  sha256: string;
  byteSize: number;
  mimeType: string;
  filename: string;
  extension: string;
  storageKind: FileStorageKind;
  isText: boolean;
  isViewable: boolean;
  viewerHint: string;
  thumbnailPath?: string;
  referenceCount: number;
  lastAccessedAt?: string;
  createdAt: string;
}

// ── Viewer Registry (spec §5.1) ─────────────────────────────

export type ViewerRenderType = 'fullscreen' | 'embed' | 'card';

export interface ViewerRegistration {
  id: string;
  viewerSlug: string;
  version: string;
  canopydVersion: string;
  displayName: string;
  description: string;
  iconUrl: string;
  renderType: ViewerRenderType;
  supportsMime: string[];
  supportsExtensions: string[];
  supportsViewerHint: string[];
  requiredCapabilities: string[];
  bundlePath: string;
  bundleByteSize: number;
  bundleSha256: string;
  minCanopydVersion: string;
  deprecationNotice?: string;
  isActive: boolean;
  installedAt: string;
}

// ── Viewer Config Override (spec §5.1) ──────────────────────

export interface ViewerConfigOverride {
  id: string;
  profileId?: string;
  treeId?: string;
  mimePattern: string;
  extensionPattern: string;
  overrideViewer: string; // '' disables
  priority: number;
  createdBy: string;
  createdAt: string;
  expiresAt?: string;
}

// ── File Access (spec §5.1) ─────────────────────────────────

export type FileAccessAction =
  | 'open'
  | 'download'
  | 'thumbnail_fetch'
  | 'preview_text'
  | 'stream_start'
  | 'stream_end'
  | 'error';

export interface FileAccessEntry {
  id: string;
  fileId: string;
  profileId: string;
  treeId?: string;
  nodeId?: string;
  viewerSlug: string;
  action: FileAccessAction;
  durationMs?: number;
  byteOffset?: number;
  clientInfo: Record<string, unknown>;
  errorCode?: string;
  createdAt: string;
}

// ── File Node (spec §5.1) ───────────────────────────────────

export interface FileNode {
  id: string; // Node ID (FK to nodes table)
  treeId: string;
  parentNodeId?: string;
  fileId: string; // FK to file_metadata.id
  filename: string;
  displayName?: string;
  viewerHint?: string; // override of auto-detected viewer
  position: number;
  createdAt: string;
  createdBy: string;
  attachmentKind: 'reference' | 'upload';
}

// ── File Resolver I/O (spec §5.1) ───────────────────────────

export interface HashRef {
  profileId: string;
  sha256: string;
}

export interface FileUpload {
  profileId: string;
  filename: string;
  declaredMime?: string;
  sourceMessageId?: string;
}

export type ResolveFileInput =
  | { hashRef: HashRef; upload?: never }
  | { hashRef?: never; upload: FileUpload };

/** Spec camelCase view of ResolveFileOutput; the wire format is snake_case (see schema below). */
export interface ResolveFileOutput {
  file: FileMetadata;
  wasNewUpload: boolean;
  wasDeduped: boolean;
  streamUrl: string;
  thumbnailUrl?: string;
  expiresAt: string;
}

export interface ViewerDispatchResult {
  viewerSlug: string;
  renderType: ViewerRenderType;
  bundlePath: string;
  bundleSha256: string;
  displayName: string;
  iconUrl: string;
  config: Record<string, unknown>;
  isBuiltIn: true;
}

export interface FileViewerConfig {
  theme: 'light' | 'dark' | 'auto';
  fontSize: number; // 12-20
  lineHeight: number; // 1.2-2.0
  tabSize: number; // 2, 4, 8
  wordWrap: boolean;
  renderMath: boolean;
  renderTaskLists: boolean;
  imageZoomSensitivity: number; // 0.5-3.0
  audioVolume: number; // 0-1
  playbackRate: number; // 0.25-2.0
  autoPlayMedia: boolean;
  loopMedia: boolean;
  preloadMedia: 'none' | 'metadata' | 'auto';
  showLineNumbers: boolean;
  minimapEnabled: boolean;
  pdfSinglePageMode: boolean;
  csvFirstRowIsHeader: boolean;
  jsonCollapseDepth: number; // 1-8
}

export const DEFAULT_FILE_VIEWER_CONFIG: FileViewerConfig = {
  theme: 'auto',
  fontSize: 14,
  lineHeight: 1.5,
  tabSize: 4,
  wordWrap: false,
  renderMath: true,
  renderTaskLists: true,
  imageZoomSensitivity: 1.0,
  audioVolume: 1.0,
  playbackRate: 1.0,
  autoPlayMedia: false,
  loopMedia: false,
  preloadMedia: 'metadata',
  showLineNumbers: true,
  minimapEnabled: false,
  pdfSinglePageMode: false,
  csvFirstRowIsHeader: true,
  jsonCollapseDepth: 3,
};

// ── Zod schemas (spec §5.2, tolerant of the live Go JSON) ──

const SHA256_RE = /^[a-f0-9]{64}$/;

export const FileStorageKindSchema = z.enum(['hermes_kb', 'hermes_fs_ref', 'external']);
export const FileSourceKindSchema = z.enum(['upload', 'reference', 'agent_message', 'import']);

/** Go time.Time → RFC 3339 (Z or numeric offset, RFC 3339Nano fractions). */
const timestampSchema = z.string().datetime({ offset: true });

function nullToEmptyRecord(value: unknown): unknown {
  return value == null ? {} : value;
}
function nullToEmptyArray(value: unknown): unknown {
  return value == null ? [] : value;
}

export const FileMetadataSchema = z.object({
  id: z.string().uuid(),
  profileId: z.string().uuid(),
  sha256: z.string().regex(SHA256_RE, 'Must be SHA-256 hex digest'),
  byteSize: z.number().int().positive().max(536870912), // 500 MB hard limit (spec §2)
  mimeType: z.string().min(1).max(200),
  declaredMime: z.string().max(200).default(''),
  filename: z.string().min(1).max(1000),
  extension: z.string().max(50).default(''),
  storageKind: FileStorageKindSchema,
  sourceKind: FileSourceKindSchema,
  sourceMessageId: z.string().uuid().optional(),
  sourceExternalUrl: z.string().url().optional(),
  isText: z.boolean(),
  isBinary: z.boolean(),
  isViewable: z.boolean(),
  viewerHint: z.string().max(100).default(''),
  thumbnailPath: z.string().default(''),
  thumbnailSha256: z.string().regex(SHA256_RE).optional(),
  previewText: z.string().max(8192).default(''),
  // nil Go []byte marshals as null → normalize to {}.
  metadata: z.preprocess(nullToEmptyRecord, z.record(z.unknown())),
  referenceCount: z.number().int().nonnegative(),
  lastAccessedAt: timestampSchema.optional(),
  accessCount: z.number().int().nonnegative(),
  quarantined: z.boolean(),
  createdAt: timestampSchema,
  updatedAt: timestampSchema,
  deletedAt: timestampSchema.optional(),
});

export const FileMetadataSlimSchema = FileMetadataSchema.pick({
  id: true,
  profileId: true,
  sha256: true,
  byteSize: true,
  mimeType: true,
  filename: true,
  extension: true,
  storageKind: true,
  isText: true,
  isViewable: true,
  viewerHint: true,
  thumbnailPath: true,
  referenceCount: true,
  lastAccessedAt: true,
  createdAt: true,
});

/** GET /files envelope: { files: FileMetadataSlim[], pagination: { count } }. */
export const FileListPageSchema = z
  .object({
    files: z.array(FileMetadataSlimSchema),
    pagination: z.preprocess(
      nullToEmptyRecord,
      z.object({ count: z.number().int().nonnegative() }).passthrough(),
    ),
  })
  .passthrough();

export interface FileListPage {
  files: FileMetadataSlim[];
  pagination: { count: number };
}

// ── Viewer Registry ─────────────────────────────────────────

export const ViewerRenderTypeSchema = z.enum(['fullscreen', 'embed', 'card']);

export const ViewerSlugSchema = z.string().regex(
  /^[a-z][a-z0-9_]*$/,
  'Viewer slug must be lowercase, start with a letter, contain only a-z, 0-9, underscores',
);

const sha256OrEmpty = z.union([z.literal(''), z.string().regex(SHA256_RE)]);

export const ViewerRegistrationSchema = z.object({
  id: z.string().uuid(),
  viewerSlug: ViewerSlugSchema,
  version: z.string().regex(/^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$/),
  canopydVersion: z.string().regex(/^[0-9]+\.[0-9]+\.[0-9]+/),
  displayName: z.string().min(1).max(100),
  description: z.string().max(1000).default(''),
  iconUrl: z.string().default(''),
  renderType: ViewerRenderTypeSchema,
  supportsMime: z.preprocess(nullToEmptyArray, z.array(z.string()).min(1)),
  supportsExtensions: z.preprocess(nullToEmptyArray, z.array(z.string())),
  supportsViewerHint: z.preprocess(nullToEmptyArray, z.array(z.string())),
  requiredCapabilities: z.preprocess(nullToEmptyArray, z.array(z.string())),
  bundlePath: z.string().default(''),
  bundleByteSize: z.number().int().nonnegative().default(0),
  bundleSha256: sha256OrEmpty.default(''), // phase-1 seeds carry ''
  minCanopydVersion: z.string(),
  deprecationNotice: z.string().optional(),
  isActive: z.boolean(),
  installedAt: timestampSchema,
});

// ── Viewer Config Override ──────────────────────────────────

export const ViewerConfigOverrideSchema = z.object({
  id: z.string().uuid(),
  profileId: z.string().uuid().optional(),
  treeId: z.string().uuid().optional(),
  mimePattern: z.string().max(200).default(''),
  extensionPattern: z.string().max(50).default(''),
  overrideViewer: z.union([z.literal(''), ViewerSlugSchema]),
  priority: z.number().int().min(1).max(1000),
  createdBy: z.string().uuid(),
  createdAt: timestampSchema,
  expiresAt: timestampSchema.optional(),
});

// ── File Access Log ─────────────────────────────────────────

export const FileAccessActionSchema = z.enum([
  'open',
  'download',
  'thumbnail_fetch',
  'preview_text',
  'stream_start',
  'stream_end',
  'error',
]);

export const FileAccessEntrySchema = z.object({
  id: z.string().uuid(),
  fileId: z.string().uuid(),
  profileId: z.string().uuid(),
  treeId: z.string().uuid().optional(),
  nodeId: z.string().uuid().optional(),
  viewerSlug: ViewerSlugSchema,
  action: FileAccessActionSchema,
  durationMs: z.number().int().nonnegative().optional(),
  byteOffset: z.number().int().nonnegative().optional(),
  // nil Go []byte marshals as null → normalize to {}.
  clientInfo: z.preprocess(nullToEmptyRecord, z.record(z.unknown())),
  errorCode: z.string().optional(),
  createdAt: timestampSchema,
});

// ── File Node ───────────────────────────────────────────────

export const FileNodeSchema = z.object({
  id: z.string().uuid(),
  treeId: z.string().uuid(),
  parentNodeId: z.string().uuid().optional(),
  fileId: z.string().uuid(),
  filename: z.string().min(1).max(1000),
  displayName: z.string().max(200).optional(),
  viewerHint: ViewerSlugSchema.optional(),
  position: z.number().int().nonnegative(),
  createdAt: timestampSchema,
  createdBy: z.string().uuid(),
  attachmentKind: z.enum(['reference', 'upload']),
});

// ── File Resolver ───────────────────────────────────────────

export const HashRefSchema = z.object({
  profileId: z.string().uuid(),
  sha256: z.string().regex(SHA256_RE),
});

export const FileUploadSchema = z.object({
  profileId: z.string().uuid(),
  filename: z.string().min(1).max(1000),
  declaredMime: z.string().max(200).optional(),
  sourceMessageId: z.string().uuid().optional(),
});

export const ResolveFileInputSchema = z.union([
  z.object({ hashRef: HashRefSchema, upload: z.undefined().optional() }),
  z.object({ upload: FileUploadSchema, hashRef: z.undefined().optional() }),
]);

/**
 * POST /files/resolve & POST /files/upload responses. The Go struct
 * (internal/fileviewer/models.go ResolveFileOutput) emits snake_case on the
 * wire; the transform normalizes to the spec §5.1 camelCase interface.
 */
export const ResolveFileOutputSchema = z
  .object({
    file: FileMetadataSchema,
    was_new_upload: z.boolean(),
    was_deduped: z.boolean(),
    stream_url: z.string(),
    thumbnail_url: z.string().optional(),
    expires_at: timestampSchema,
  })
  .transform((raw) => ({
    file: raw.file,
    wasNewUpload: raw.was_new_upload,
    wasDeduped: raw.was_deduped,
    streamUrl: raw.stream_url,
    thumbnailUrl: raw.thumbnail_url,
    expiresAt: raw.expires_at,
  }));

export const ViewerDispatchResultSchema = z.object({
  viewerSlug: ViewerSlugSchema,
  renderType: ViewerRenderTypeSchema,
  bundlePath: z.string(),
  bundleSha256: z.string(),
  displayName: z.string(),
  iconUrl: z.string(),
  // nil Go map marshals as null → normalize to {}.
  config: z.preprocess(nullToEmptyRecord, z.record(z.unknown())),
  isBuiltIn: z.literal(true),
});

// ── File Viewer Config (user preferences) ───────────────────

export const FileViewerConfigSchema = z.object({
  theme: z.enum(['light', 'dark', 'auto']).default('auto'),
  fontSize: z.number().int().min(12).max(20).default(14),
  lineHeight: z.number().min(1.2).max(2.0).default(1.5),
  tabSize: z.union([z.literal(2), z.literal(4), z.literal(8)]).default(4),
  wordWrap: z.boolean().default(false),
  renderMath: z.boolean().default(true),
  renderTaskLists: z.boolean().default(true),
  imageZoomSensitivity: z.number().min(0.5).max(3.0).default(1.0),
  audioVolume: z.number().min(0).max(1).default(1.0),
  playbackRate: z.number().min(0.25).max(2.0).default(1.0),
  autoPlayMedia: z.boolean().default(false),
  loopMedia: z.boolean().default(false),
  preloadMedia: z.enum(['none', 'metadata', 'auto']).default('metadata'),
  showLineNumbers: z.boolean().default(true),
  minimapEnabled: z.boolean().default(false),
  pdfSinglePageMode: z.boolean().default(false),
  csvFirstRowIsHeader: z.boolean().default(true),
  jsonCollapseDepth: z.number().int().min(1).max(8).default(3),
});

// ── API Request Schemas (spec §5.2) ─────────────────────────

export const UploadFileRequestSchema = z.object({
  filename: z.string().min(1).max(1000),
  declaredMime: z.string().max(200).optional(),
  sourceMessageId: z.string().uuid().optional(),
});

export const ResolveByHashRequestSchema = z.object({
  profileId: z.string().uuid(),
  sha256: z.string().regex(SHA256_RE),
});

export const CreateViewerOverrideRequestSchema = z.object({
  profileId: z.string().uuid().optional(),
  treeId: z.string().uuid().optional(),
  mimePattern: z.string().max(200).default(''),
  extensionPattern: z.string().max(50).default(''),
  overrideViewer: z.union([z.literal(''), ViewerSlugSchema]),
  priority: z.number().int().min(1).max(1000).default(100),
  expiresAt: timestampSchema.optional(),
});

export const ListFilesRequestSchema = z.object({
  cursor: z.string().uuid().optional(),
  limit: z.number().int().min(1).max(200).default(50),
  sort: z
    .enum(['created_desc', 'created_asc', 'name_asc', 'size_desc', 'last_accessed_desc'])
    .default('created_desc'),
  mimeFilter: z.string().optional(),
  extensionFilter: z.string().optional(),
  viewableOnly: z.boolean().default(true),
});

// ── Parse helpers ───────────────────────────────────────────

export function parseFileMetadata(raw: unknown): FileMetadata {
  return FileMetadataSchema.parse(raw);
}

export function parseFileMetadataSlim(raw: unknown): FileMetadataSlim {
  return FileMetadataSlimSchema.parse(raw);
}

export function parseFileListPage(raw: unknown): FileListPage {
  return FileListPageSchema.parse(raw);
}

export function parseViewerRegistration(raw: unknown): ViewerRegistration {
  return ViewerRegistrationSchema.parse(raw);
}

/** Parses a GET /viewers response body (a bare ViewerRegistration[] array). */
export function parseViewersResponse(raw: unknown): ViewerRegistration[] {
  return z.array(ViewerRegistrationSchema).parse(raw);
}

export function parseViewerDispatchResult(raw: unknown): ViewerDispatchResult {
  return ViewerDispatchResultSchema.parse(raw);
}

export function parseResolveFileOutput(raw: unknown): ResolveFileOutput {
  return ResolveFileOutputSchema.parse(raw);
}

export function parseFileAccessEntry(raw: unknown): FileAccessEntry {
  return FileAccessEntrySchema.parse(raw);
}
