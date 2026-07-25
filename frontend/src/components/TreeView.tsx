/**
 * Hermes Canopy — Tree View Page
 *
 * Full tree visualization page. Manages:
 *   - Yjs document lifecycle (create/load)
 *   - SSE sync provider
 *   - IndexedDB persistence
 *   - React Flow canvas
 */

import { useEffect, useRef, useState } from 'react';
import { useParams } from 'react-router-dom';
import TreeCanvas from '../components/TreeCanvas.tsx';
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
    <div className="h-full w-full">
      {/* Tree header bar */}
      <div className="h-10 bg-white dark:bg-gray-900 border-b border-gray-200 dark:border-gray-800 flex items-center px-4 gap-3">
        <span className="text-sm font-medium text-gray-700 dark:text-gray-300">
          🌳 {tree.treeTitle || 'Tree View'}
        </span>
        <span className="text-xs text-gray-400 dark:text-gray-500">
          {tree.nodes.length} nodes · {tree.edges.length} edges
        </span>
        {!tree.isReady && (
          <span className="text-xs text-amber-500 animate-pulse ml-auto">
            Connecting...
          </span>
        )}
      </div>

      {/* Canvas fills remaining space */}
      <div className="h-[calc(100%-2.5rem)]">
        <TreeCanvas tree={tree} />
      </div>
    </div>
  );
}
