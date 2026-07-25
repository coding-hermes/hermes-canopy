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
 * The SSE endpoint is /api/v1/events (SPEC-API-01).
 * One provider instance per tree.
 */

import * as Y from 'yjs';
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

// ─── SSE Event Types ──────────────────────────────────────────────────

interface SSEMessageEvent {
  type: string;
  treeId: string;
  data: unknown;
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
    // Send a leave message
    void fetch(
      `${this.apiBase}/api/v1/trees/${encodeURIComponent(this.treeId)}/presence/leave`,
      { method: 'POST', headers: { 'Content-Type': 'application/json' } },
    ).catch(() => { /* best-effort */ });
  }

  // ─── Lifecycle ──────────────────────────────────────────────────────

  /** Connect to the SSE endpoint and begin syncing. */
  connect(): void {
    if (this.eventSource) {
      this.disconnect();
    }

    const url = `${this.apiBase}/api/v1/events?tree_id=${encodeURIComponent(this.treeId)}`;

    this.eventSource = new EventSource(url);

    this.eventSource.onopen = (): void => {
      this._connected = true;
      this.options.onConnected?.();
    };

    this.eventSource.onmessage = (event: MessageEvent): void => {
      this.handleSSEMessage(event.data);
    };

    // SSE also supports named events via addEventListener
    this.eventSource.addEventListener('node_added', ((e: MessageEvent) => {
      this.handleSSEMessage(e.data);
    }) as EventListener);

    this.eventSource.addEventListener('node_updated', ((e: MessageEvent) => {
      this.handleSSEMessage(e.data);
    }) as EventListener);

    this.eventSource.addEventListener('node_deleted', ((e: MessageEvent) => {
      this.handleSSEMessage(e.data);
    }) as EventListener);

    this.eventSource.addEventListener('edge_added', ((e: MessageEvent) => {
      this.handleSSEMessage(e.data);
    }) as EventListener);

    this.eventSource.addEventListener('edge_deleted', ((e: MessageEvent) => {
      this.handleSSEMessage(e.data);
    }) as EventListener);

    this.eventSource.addEventListener('tree_updated', ((e: MessageEvent) => {
      this.handleSSEMessage(e.data);
    }) as EventListener);

    // Presence/awareness events
    this.eventSource.addEventListener('presence_update', ((e: MessageEvent) => {
      this.handlePresenceEvent(JSON.parse(e.data) as Record<string, unknown>);
    }) as EventListener);

    this.eventSource.addEventListener('cursor_update', ((e: MessageEvent) => {
      this.handleCursorEvent(JSON.parse(e.data) as Record<string, unknown>);
    }) as EventListener);

    this.eventSource.onerror = (): void => {
      this._connected = false;
      this.options.onDisconnected?.('SSE connection error');
      // EventSource will auto-reconnect after a delay
    };

    // Listen for local Yjs changes and push to server
    this.updateHandler = (update: Uint8Array, origin: unknown): void => {
      // Don't push updates that originated from the server
      if (origin === 'sse-provider') return;
      void this.pushUpdate(update);
    };

    this.doc.ydoc.on('update', this.updateHandler);
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

  private handleSSEMessage(data: string): void {
    try {
      const event: SSEMessageEvent = JSON.parse(data) as SSEMessageEvent;

      // Only process events for our tree
      if (event.treeId && event.treeId !== this.treeId) return;

      switch (event.type) {
        case 'node_added':
        case 'node_updated':
          this.applyNodeUpdate(event.data);
          break;
        case 'node_deleted':
          this.applyNodeDelete(event.data);
          break;
        case 'edge_added':
          this.applyEdgeUpdate(event.data);
          break;
        case 'edge_deleted':
          this.applyEdgeDelete(event.data);
          break;
        case 'tree_updated':
          this.applyTreeUpdate(event.data);
          break;
        default:
          // Unknown event type — attempt generic Yjs update
          this.applyGenericUpdate(data);
          break;
      }
    } catch {
      // If JSON parsing fails, try as raw Yjs binary update
      this.applyGenericUpdate(data);
    }

    this.options.onSynced?.();
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
    try {
      const base64 = btoa(
        String.fromCharCode(...new Uint8Array(update)),
      );

      const response = await fetch(
        `${this.apiBase}/api/v1/trees/${encodeURIComponent(this.treeId)}/sync`,
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ update: base64 }),
        },
      );

      if (!response.ok) {
        console.warn(
          `[SSESyncProvider] Push update failed: ${response.status} ${response.statusText}`,
        );
      }
    } catch (err) {
      this.options.onError?.(
        err instanceof Error ? err : new Error(String(err)),
      );
    }
  }

  // ─── Presence sync ──────────────────────────────────────────────

  /** Push local presence state to server. */
  private async pushPresence(): Promise<void> {
    if (!this.localPresence) return;
    try {
      const response = await fetch(
        `${this.apiBase}/api/v1/trees/${encodeURIComponent(this.treeId)}/presence`,
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(this.localPresence),
        },
      );

      if (!response.ok) {
        console.warn(
          `[SSESyncProvider] Push presence failed: ${response.status}`,
        );
      }
    } catch {
      // Silently ignore — presence is best-effort
    }
  }

  /** Handle incoming presence_update SSE event. */
  private handlePresenceEvent(data: Record<string, unknown>): void {
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
  private handleCursorEvent(data: Record<string, unknown>): void {
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
