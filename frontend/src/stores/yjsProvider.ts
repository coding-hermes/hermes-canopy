/**
 * Hermes Canopy — SSE-Backed Yjs Sync Provider
 *
 * Bridges Yjs CRDT updates between the local Y.Doc and the backend via:
 *   - SSE (EventSource) for server→client real-time updates
 *   - HTTP POST for client→server local changes
 *
 * The SSE endpoint is /api/v1/events (SPEC-API-01).
 * One provider instance per tree.
 */

import * as Y from 'yjs';
import type { TreeYDoc } from './treeStore.ts';

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

  constructor(doc: TreeYDoc, options: YjsProviderOptions) {
    this.doc = doc;
    this.treeId = options.treeId;
    this.apiBase = options.apiBase ?? '';
    this.options = options;
  }

  get connected(): boolean {
    return this._connected;
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
}
