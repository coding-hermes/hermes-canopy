/**
 * Hermes Canopy — Tree View Page
 *
 * Full tree visualization page. Manages:
 *   - Yjs document lifecycle (create/load)
 *   - SSE sync provider
 *   - IndexedDB persistence
 *   - React Flow canvas
 *   - Navigation (search, breadcrumbs, node focus)
 */

import { useEffect, useRef, useState, useCallback } from 'react';
import { useParams } from 'react-router-dom';
import TreeCanvas from '../components/TreeCanvas.tsx';
import NavigationBar from '../components/NavigationBar.tsx';
import MessageComposer, {
  type PinnedNode,
} from '../components/MessageComposer.tsx';
import {
  createTreeDoc,
  bindIndexedDB,
  type TreeYDoc,
} from '../stores/treeStore.ts';
import { SSESyncProvider } from '../stores/yjsProvider.ts';
import { useYjsTree } from '../stores/useYjsTree.ts';

export default function TreeView() {
  const { treeId } = useParams<{ treeId: string }>();
  const [error, setError] = useState<string | null>(null);
  const docRef = useRef<TreeYDoc | null>(null);
  const providerRef = useRef<SSESyncProvider | null>(null);
  const [doc, setDoc] = useState<TreeYDoc | null>(null);

  // Navigation state
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);
  const [focusNodeId, setFocusNodeId] = useState<string | null>(null);

  // Initialize Yjs document + providers
  useEffect(() => {
    if (!treeId) {
      setError('No tree ID provided');
      return;
    }

    try {
      // Create Y.Doc
      const treeDoc = createTreeDoc(treeId);
      docRef.current = treeDoc;

      // Bind IndexedDB persistence
      bindIndexedDB(treeId, treeDoc.ydoc);

      // Connect SSE sync provider
      const provider = new SSESyncProvider(treeDoc, {
        treeId,
        onConnected: () => {
          console.log(`[TreeView] SSE connected for tree ${treeId}`);
        },
        onDisconnected: (reason) => {
          console.warn(`[TreeView] SSE disconnected: ${reason}`);
        },
        onError: (err) => {
          console.error(`[TreeView] SSE error:`, err);
        },
        onSynced: () => {
          // Re-render will be triggered by Yjs observer in useYjsTree
        },
      });
      provider.connect();
      providerRef.current = provider;

      setDoc(treeDoc);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to initialize tree');
    }

    return () => {
      providerRef.current?.disconnect();
      docRef.current?.ydoc.destroy();
      docRef.current = null;
      providerRef.current = null;
    };
  }, [treeId]);

  const tree = useYjsTree(doc);

  // Handle selection change from canvas
  const handleSelectionChange = useCallback((nodeId: string | null) => {
    setSelectedNodeId(nodeId);
  }, []);

  // Handle navigate-to-node from NavigationBar
  const handleNavigateToNode = useCallback((nodeId: string) => {
    setSelectedNodeId(nodeId);
    // Toggle focusNodeId to trigger animation even if same node
    setFocusNodeId(null);
    // Use setTimeout to ensure React processes the null first
    setTimeout(() => setFocusNodeId(nodeId), 0);
  }, []);

  // Handle message send from MessageComposer
  const handleSendMessage = useCallback(
    (_message: string, _files: File[], _pinnedNodes: PinnedNode[]) => {
      // TODO: Wire to actual message-sending API (backend integration)
      console.log('[TreeView] Message sent:', {
        text: _message.slice(0, 100),
        fileCount: _files.length,
        pinnedCount: _pinnedNodes.length,
      });
    },
    [],
  );

  if (error) {
    return (
      <div className="flex items-center justify-center h-full">
        <div className="text-center p-8">
          <p className="text-red-500 dark:text-red-400 text-lg mb-2">
            Error loading tree
          </p>
          <p className="text-gray-500 dark:text-gray-400 text-sm">{error}</p>
        </div>
      </div>
    );
  }

  return (
    <div className="h-full w-full flex flex-col">
      {/* Tree header bar */}
      <div
        className="h-10 flex items-center px-4 gap-3 border-b shrink-0"
        style={{
          backgroundColor: '#0f0f1a',
          borderColor: '#2d2d4a',
        }}
      >
        <span className="text-sm font-medium" style={{ color: '#e2e8f0' }}>
          🌳 {tree.treeTitle || 'Tree View'}
        </span>
        <span className="text-xs" style={{ color: '#94a3b8' }}>
          {tree.nodes.length} nodes · {tree.edges.length} edges
        </span>
        {!tree.isReady && (
          <span className="text-xs text-amber-500 animate-pulse ml-auto">
            Connecting...
          </span>
        )}
      </div>

      {/* Navigation bar (search + breadcrumbs) */}
      <div className="shrink-0">
        <NavigationBar
          nodes={tree.nodes}
          edges={tree.edges}
          selectedNodeId={selectedNodeId}
          onNavigateToNode={handleNavigateToNode}
        />
      </div>

      {/* Canvas fills remaining space */}
      <div className="flex-1 min-h-0">
        <TreeCanvas
          tree={tree}
          onSelectionChange={handleSelectionChange}
          focusNodeId={focusNodeId}
        />
      </div>

      {/* Message composer — bottom-docked */}
      <MessageComposer
        onSend={handleSendMessage}
        disabled={!tree.isReady}
      />
    </div>
  );
}
