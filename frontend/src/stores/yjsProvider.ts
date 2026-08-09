/**
 * Hermes Canopy — SSE-Backed Yjs Sync Provider
 *
 * Bridges Yjs CRDT updates between the local Y.Doc and the backend via:
 *   - SSE (EventSource) for server→client real-time updates
 *   - HTTP POST for client→server local changes
 *
 * Also manages multi-user presence/awareness state:
 *   - Local user presence (cursor, viewport, permission)
 *   - Remote user presence from SSE events
 *
 * The SSE endpoint is /api/v1/trees/{tree_id}/events (tree-scoped, auth-gated).
 * One provider instance per tree.
 */

import * as Y from 'yjs';
import { IndexeddbPersistence } from 'y-indexeddb';
import type { TreeYDoc } from './treeStore.ts';
import type {
  UserPresence,
  LocalPresence,
  CursorPosition,
  ViewportState,
  PresenceChangeHandler,
} from '../types/multiUser.ts';

// ─── Types ─────────────────────────────────────────────────────────────

export interface YjsProviderOptions {
  treeId: string;
  apiBase?: string;
  /** Enable y-indexeddb persistence (local backup of Yjs data) */
  enablePersistence?: boolean;
  /** Called when the provider connects successfully */
  onConnected?: () => void;
  /** Called when the provider disconnects */
  onDisconnected?: (reason: string) => void;
  /** Called on sync error */
  onError?: (error: Error) => void;
  /** Called when a server update is applied */
  onSynced?: () => void;
  /** Called when remote presence state changes */
  onPresenceChange?: PresenceChangeHandler;
}

// ─── SSE Event Envelope ─────────────────────────────────────────────────

interface SSEEnvelope {
  event_type: string;
  tree_id: string;
  data: unknown;
  sequence_num?: number;
  actor_id?: string;
  timestamp?: string;
}

// ─── Provider ─────────────────────────────────────────────────────────

export class SSESyncProvider {
  private doc: TreeYDoc;
  private treeId: string;
  private apiBase: string;
  private eventSource: EventSource | null = null;
  private options: YjsProviderOptions;
  private _connected = false;
  private updateHandler: ((update: Uint8Array, origin: unknown) => void) | null =
    null;
  /** y-indexeddb persistence instance (set up if enablePersistence=true) */
  private persistence: IndexeddbPersistence | null = null;

  // ─── Awareness / Presence state ────────────────────────────────
  /** Map of userId → UserPresence for all remote users */
  private remotePresence = new Map<string, UserPresence>();
  /** Local user presence state */
  private localPresence: LocalPresence | null = null;
  /** Debounce timer for cursor position updates */
  private cursorDebounceTimer: ReturnType<typeof setTimeout> | null = null;

  constructor(doc: TreeYDoc, options: YjsProviderOptions) {
    this.doc = doc;
    this.treeId = options.treeId;
    this.apiBase = options.apiBase ?? '';
    this.options = options;

    // Set up IndexedDB persistence for offline support
    if (options.enablePersistence) {
      this.initPersistence();
    }
  }

  /**
   * Initialize y-indexeddb persistence for this tree's Y.Doc.
   * The database name is derived from the treeId so each tree
   * gets its own IndexedDB database.
   */
  private initPersistence(): void {
    const dbName = `canopy-tree-${this.treeId}`;
    try {
      this.persistence = new IndexeddbPersistence(dbName, this.doc.ydoc);
      this.persistence.on('error', (err: unknown) => {
        console.warn(`[IndexedDB] Persistence error for ${dbName}:`, err);
      });
    } catch (err) {
      console.warn(`[IndexedDB] Failed to initialize persistence for ${dbName}:`, err);
    }
  }

  get connected(): boolean {
    return this._connected;
  }

  // ─── Presence / Awareness public API ─────────────────────────────

  /** Get all remote user presence states (read-only snapshot). */
  getRemotePresence(): ReadonlyMap<string, UserPresence> {
    return this.remotePresence;
  }

  /** Set local user presence (who we are, our permission, cursor, etc.). */
  setLocalPresence(presence: LocalPresence): void {
    this.localPresence = presence;
    // Broadcast to server (debounced)
    void this.pushPresence();
  }

  /** Update only the cursor position (debounced to avoid flooding). */
  updateCursor(cursor: CursorPosition): void {
    if (!this.localPresence) return;
    this.localPresence = { ...this.localPresence, cursor };
    if (this.cursorDebounceTimer) clearTimeout(this.cursorDebounceTimer);
    this.cursorDebounceTimer = setTimeout(() => {
      void this.pushPresence();
    }, 50);
  }

  /** Update only the viewport state (debounced). */
  updateViewport(viewport: ViewportState): void {
    if (!this.localPresence) return;
    this.localPresence = { ...this.localPresence, viewport };
    void this.pushPresence();
  }

  /** Remove local presence (cleanup on disconnect). */
  clearLocalPresence(): void {
    this.localPresence = null;
    // TODO(WIRE-004): Backend presence/leave endpoint planned but not yet implemented.
    console.log('[SSESyncProvider] Presence leave (endpoint coming soon — WIRE-004)');
  }

  // ─── Lifecycle ──────────────────────────────────────────────────────

  /** Connect to the SSE endpoint and begin syncing. */
  connect(): void {
    if (this.eventSource) {
      this.disconnect();
    }

    const url = `${this.apiBase}/api/v1/trees/${encodeURIComponent(
      this.treeId,
    )}/events`;

    this.eventSource = new EventSource(url, { withCredentials: true });

    this.eventSource.onopen = (): void => {
      this._connected = true;
      console.debug(`[SSESyncProvider] connected to ${url}`);
      this.options.onConnected?.();
    };

    this.eventSource.onerror = (): void => {
      this._connected = false;
      console.error(`[SSESyncProvider] SSE connection error for tree ${this.treeId}`);
      this.options.onDisconnected?.('SSE connection error');
    };

    const forward = (eventName: string): void => {
      this.eventSource?.addEventListener(
        eventName,
        ((e: MessageEvent) => {
          this._handleSSEMessage(e.data as string);
        }) as EventListener,
      );
    };

    for (const eventName of [
      'node_added',
      'node_updated',
      'node_deleted',
      'edge_added',
      'edge_deleted',
      'tree_updated',
      'yjs_update',
    ]) {
      forward(eventName);
    }

    // Listen for local Yjs changes and push to server
    this.updateHandler = (update: Uint8Array, origin: unknown): void => {
      if (origin === 'sse-provider') return;
      void this.pushUpdate(update);
    };
    this.doc.ydoc.on('update', this.updateHandler!);
  }

  /** Disconnect from SSE. */
  disconnect(): void {
    if (this.updateHandler) {
      this.doc.ydoc.off('update', this.updateHandler);
      this.updateHandler = null;
    }

    if (this.cursorDebounceTimer) {
      clearTimeout(this.cursorDebounceTimer);
      this.cursorDebounceTimer = null;
    }

    this.clearLocalPresence();

    if (this.eventSource) {
      this.eventSource.close();
      this.eventSource = null;
    }

    this._connected = false;
    this.options.onDisconnected?.('manual disconnect');
  }

  // ─── Message handling ───────────────────────────────────────────────

  private _handleSSEMessage(data: string): void {
    let envelope: SSEEnvelope | undefined;
    try {
      envelope = JSON.parse(data) as SSEEnvelope;
    } catch {
      // If JSON parsing fails, try as raw Yjs binary update
      this.applyGenericUpdate(data);
      this.options.onSynced?.();
      return;
    }

    // Only process events for our tree
    if (envelope.tree_id && envelope.tree_id !== this.treeId) return;

    switch (envelope.event_type) {
      case 'node_added':
      case 'node_updated':
        this.applyNodeUpdate(this.normalizeNodePayload(envelope.data));
        break;
      case 'node_deleted':
        this.applyNodeDelete(this.normalizeDeletePayload(envelope.data));
        break;
      case 'edge_added':
        this.applyEdgeUpdate(this.normalizeEdgePayload(envelope.data));
        break;
      case 'edge_deleted':
        this.applyEdgeDelete(this.normalizeDeletePayload(envelope.data));
        break;
      case 'tree_updated':
        this.applyTreeUpdate(envelope.data);
        break;
      case 'yjs_update':
        if (typeof envelope.data === 'string') {
          this.applyGenericUpdate(envelope.data);
        }
        break;
      default:
        if (typeof envelope.data === 'string') {
          this.applyGenericUpdate(envelope.data);
        }
        break;
    }

    this.options.onSynced?.();
  }

  private normalizeNodePayload(data: unknown): Record<string, unknown> {
    const src = data as Record<string, unknown> | null;
    if (!src) return {};
    const out: Record<string, unknown> = {};
    const map = (snake: string, camel: string): void => {
      if (Object.prototype.hasOwnProperty.call(src, snake)) {
        out[camel] = src[snake];
      }
    };
    map('node_id', 'id');
    map('parent_id', 'parentId');
    map('content', 'content');
    map('content_format', 'contentFormat');
    map('node_type', 'nodeType');
    map('actor_id', 'actorId');
    map('timestamp', 'timestamp');
    map('sequence_num', 'sequenceNum');
    map('edge_id', 'edgeId');
    map('edge_type', 'edgeType');
    map('mutation_type', 'mutationType');
    return out;
  }

  private normalizeEdgePayload(data: unknown): Record<string, unknown> {
    const src = data as Record<string, unknown> | null;
    if (!src) return {};
    const out: Record<string, unknown> = {};
    const map = (snake: string, camel: string): void => {
      if (Object.prototype.hasOwnProperty.call(src, snake)) {
        out[camel] = src[snake];
      }
    };
    map('edge_id', 'id');
    map('edge_type', 'edgeType');
    map('source_id', 'sourceId');
    map('target_id', 'targetId');
    map('actor_id', 'actorId');
    map('timestamp', 'timestamp');
    return out;
  }

  private normalizeDeletePayload(data: unknown): { id?: string } {
    const src = data as Record<string, unknown> | null;
    if (!src) return {};
    const id = (src.node_id ?? src.id) as string | undefined;
    return { id };
  }

  private applyNodeUpdate(data: unknown): void {
    const node = data as Record<string, unknown> | null;
    if (!node?.id) return;

    this.doc.ydoc.transact(() => {
      const existing = this.doc.nodes.get(node.id as string);
      const target = existing ?? new Y.Map<unknown>();

      for (const [key, value] of Object.entries(node)) {
        target.set(key, value);
      }

      if (!existing) {
        this.doc.nodes.set(node.id as string, target);
        // Auto-add to rootOrder if root node
        if (node.parentId === null || node.parentId === undefined) {
          const idx = this.doc.rootOrder.toArray().indexOf(node.id as string);
          if (idx === -1) {
            this.doc.rootOrder.push([node.id as string]);
          }
        }
      }
    }, 'sse-provider');
  }

  private applyNodeDelete(data: unknown): void {
    const payload = data as { id?: string } | null;
    if (!payload?.id) return;

    this.doc.ydoc.transact(() => {
      this.doc.nodes.delete(payload.id as string);
      // Remove from rootOrder
      const idx = this.doc.rootOrder.toArray().indexOf(payload.id as string);
      if (idx !== -1) {
        this.doc.rootOrder.delete(idx, 1);
      }
    }, 'sse-provider');
  }

  private applyEdgeUpdate(data: unknown): void {
    const edge = data as Record<string, unknown> | null;
    if (!edge?.id) return;

    this.doc.ydoc.transact(() => {
      const map = new Y.Map<unknown>();
      for (const [key, value] of Object.entries(edge)) {
        map.set(key, value);
      }
      this.doc.edges.set(edge.id as string, map);
    }, 'sse-provider');
  }

  private applyEdgeDelete(data: unknown): void {
    const payload = data as { id?: string } | null;
    if (!payload?.id) return;

    this.doc.ydoc.transact(() => {
      this.doc.edges.delete(payload.id as string);
    }, 'sse-provider');
  }

  private applyTreeUpdate(data: unknown): void {
    const tree = data as Record<string, unknown> | null;
    if (!tree) return;

    this.doc.ydoc.transact(() => {
      for (const [key, value] of Object.entries(tree)) {
        this.doc.meta.set(key, value);
      }
    }, 'sse-provider');
  }

  private applyGenericUpdate(data: string): void {
    // Try to decode as base64-encoded Yjs update
    try {
      const binary = Uint8Array.from(atob(data), (c) => c.charCodeAt(0));
      Y.applyUpdate(this.doc.ydoc, binary, 'sse-provider');
    } catch {
      // Not a valid binary update — ignore
    }
  }

  // ─── Push local changes to server ───────────────────────────────────

  private async pushUpdate(update: Uint8Array): Promise<void> {
    const url = `${this.apiBase}/api/v1/trees/${encodeURIComponent(
      this.treeId,
    )}/sync`;
    try {
      const response = await fetch(url, {
        method: 'POST',
        body: update as unknown as BodyInit,
        headers: { 'Content-Type': 'application/octet-stream' },
        credentials: 'include',
      });
      if (!response.ok) {
        const text = await response.text().catch(() => 'unknown');
        const err = new Error(`pushUpdate failed: ${response.status} ${text}`);
        console.error('[SSESyncProvider] pushUpdate error:', err);
        this.options.onError?.(err);
        return;
      }
      console.debug(
        `[SSESyncProvider] pushUpdate succeeded (${update.byteLength} bytes)`,
      );
    } catch (err) {
      const e = err instanceof Error ? err : new Error(String(err));
      console.error('[SSESyncProvider] pushUpdate network error:', e);
      this.options.onError?.(e);
    }
  }

  // ─── Presence sync ──────────────────────────────────────────────

  /** Push local presence state to server (endpoint coming soon). */
  private async pushPresence(): Promise<void> {
    if (!this.localPresence) return;
    // TODO(WIRE-004): Backend presence endpoint planned but not yet implemented.
    console.debug('[SSESyncProvider] Presence push (endpoint coming soon — WIRE-004)');
  }

  /** Handle incoming presence_update SSE event. */
  // @ts-ignore -- TS6133: method kept for future SSE re-enablement (WIRE-004)
  private _handlePresenceEvent(data: Record<string, unknown>): void {
    const userId = data.userId as string | undefined;
    if (!userId) return;

    // Ignore our own presence
    if (this.localPresence && userId === this.localPresence.userId) return;

    const presence: UserPresence = {
      userId,
      userName: (data.userName as string) ?? 'Unknown',
      avatarColor: (data.avatarColor as string) ?? '#6b7280',
      permission: (data.permission as UserPresence['permission']) ?? 'viewer',
      cursor: data.cursor
        ? (data.cursor as CursorPosition)
        : null,
      viewport: data.viewport
        ? (data.viewport as ViewportState)
        : null,
      isActive: (data.isActive as boolean) ?? true,
      lastSeen: (data.lastSeen as string) ?? new Date().toISOString(),
    };

    // Handle leave event
    if (data.type === 'leave') {
      this.remotePresence.delete(userId);
    } else {
      this.remotePresence.set(userId, presence);
    }

    this.options.onPresenceChange?.(this.remotePresence);
  }

  /** Handle incoming cursor_update SSE event (lighter variant). */
  // @ts-ignore -- TS6133: method kept for future SSE re-enablement (WIRE-004)
  private _handleCursorEvent(data: Record<string, unknown>): void {
    const userId = data.userId as string | undefined;
    if (!userId) return;

    if (this.localPresence && userId === this.localPresence.userId) return;

    const existing = this.remotePresence.get(userId);
    if (!existing) return; // Only update cursors for known users

    const cursor = data.cursor as CursorPosition | undefined;
    const viewport = data.viewport as ViewportState | undefined;

    this.remotePresence.set(userId, {
      ...existing,
      ...(cursor ? { cursor } : {}),
      ...(viewport ? { viewport } : {}),
      lastSeen: (data.lastSeen as string) ?? new Date().toISOString(),
    });

    this.options.onPresenceChange?.(this.remotePresence);
  }
}
