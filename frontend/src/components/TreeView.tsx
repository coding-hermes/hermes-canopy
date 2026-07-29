/**
 * Hermes Canopy — Tree View Page
 *
 * Full tree visualization page. Manages:
 *   - Yjs document lifecycle (create/load)
 *   - SSE sync provider
 *   - IndexedDB persistence
 *   - React Flow canvas
 *   - Navigation (search, breadcrumbs, node focus)
 *   - Multi-user presence (PresenceBar, CollaborativeCursors)
 *   - Share dialog with permission management
 */

import { useEffect, useRef, useState, useCallback } from 'react';
import { useParams } from 'react-router-dom';
import { Share2 } from 'lucide-react';
import TreeCanvas from '../components/TreeCanvas.tsx';
import NavigationBar from '../components/NavigationBar.tsx';
import MessageComposer, {
  type PinnedNode,
} from '../components/MessageComposer.tsx';
import PresenceBar from '../components/PresenceBar.tsx';
import CollaborativeCursors from '../components/CollaborativeCursors.tsx';
import ShareDialog from '../components/ShareDialog.tsx';
import {
  createTreeDoc,
  bindIndexedDB,
  seedDemoTree,
  type TreeYDoc,
} from '../stores/treeStore.ts';
import { SSESyncProvider } from '../stores/yjsProvider.ts';
import { useYjsTree } from '../stores/useYjsTree.ts';
import { usePresence } from '../hooks/usePresence.ts';
import type {
  PermissionLevel,
  ShareInvitePayload,
} from '../types/multiUser.ts';
import { getColorForUser } from '../types/multiUser.ts';
// ─── Mock membership ───────────────────────────────────────────────────

interface Member {
  userId: string;
  userName: string;
  email: string;
  permission: PermissionLevel;
  avatarColor: string;
}

function buildInitialMembers(): Member[] {
  // In production, these would be fetched from the backend.
  // For now, we seed with a demo member.
  return [
    {
      userId: 'demo_user_1',
      userName: 'Demo User',
      email: 'demo@example.com',
      permission: 'editor',
      avatarColor: getColorForUser('demo_user_1'),
    },
  ];
}

// ─── Component ─────────────────────────────────────────────────────────

export default function TreeView() {
  const { treeId } = useParams<{ treeId: string }>();
  const [error, setError] = useState<string | null>(null);
  const docRef = useRef<TreeYDoc | null>(null);
  const providerRef = useRef<SSESyncProvider | null>(null);
  const [doc, setDoc] = useState<TreeYDoc | null>(null);

  // Navigation state
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);
  const [focusNodeId, setFocusNodeId] = useState<string | null>(null);

  // Share dialog state
  const [showShareDialog, setShowShareDialog] = useState(false);
  const [members, setMembers] = useState<Member[]>(buildInitialMembers);

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

      // Expose Y.Doc and seed function for E2E tests
      if (typeof window !== 'undefined') {
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        (window as any).__canopyTreeDoc = treeDoc;
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        (window as any).__canopySeedDemoTree = () => {
          seedDemoTree(treeDoc);
        };
      }
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

  // ── Multi-user presence ──────────────────────────────────────────
  const {
    remotePresence,
    userId: localUserId,
    permission: currentPermission,
  } = usePresence(providerRef.current, 'You');

  // ── Handlers ──────────────────────────────────────────────────────

  // Handle selection change from canvas
  const handleSelectionChange = useCallback((nodeId: string | null) => {
    setSelectedNodeId(nodeId);
  }, []);

  // Handle navigate-to-node from NavigationBar
  const handleNavigateToNode = useCallback((nodeId: string) => {
    setSelectedNodeId(nodeId);
    setFocusNodeId(null);
    setTimeout(() => setFocusNodeId(nodeId), 0);
  }, []);

  // Handle message send from MessageComposer
  const handleSendMessage = useCallback(
    (_message: string, _files: File[], _pinnedNodes: PinnedNode[]) => {
      console.log('[TreeView] Message sent:', {
        text: _message.slice(0, 100),
        fileCount: _files.length,
        pinnedCount: _pinnedNodes.length,
      });
    },
    [],
  );

  // ── Share dialog handlers ─────────────────────────────────────────

  const handleInvite = useCallback(
    (payload: ShareInvitePayload) => {
      console.log('[TreeView] Invite sent:', payload);
      // Add the invited user to local members (placeholder)
      const mockUserId = `mock_${Date.now()}`;
      const newMember: Member = {
        userId: mockUserId,
        userName: payload.email.split('@')[0] ?? payload.email,
        email: payload.email,
        permission: payload.permission,
        avatarColor: getColorForUser(mockUserId),
      };
      setMembers((prev) => [...prev, newMember]);
    },
    [],
  );

  const handlePermissionChange = useCallback(
    (userId: string, permission: PermissionLevel) => {
      setMembers((prev) =>
        prev.map((m) => (m.userId === userId ? { ...m, permission } : m)),
      );
    },
    [],
  );

  const handleRemoveMember = useCallback((userId: string) => {
    setMembers((prev) => prev.filter((m) => m.userId !== userId));
  }, []);

  // ── Derive viewer mode ────────────────────────────────────────────
  const isViewer = currentPermission === 'viewer';

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
      {/* TreeView page heading for screen readers */}
      <h1 className="sr-only">
        Tree View: {tree.treeTitle || 'Untitled Tree'}
      </h1>
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

        {/* Share button (non-viewers only) */}
        {!isViewer && (
          <button
            onClick={() => setShowShareDialog(true)}
            className="flex items-center gap-1.5 px-2.5 py-1 rounded-lg text-xs font-medium transition-colors ml-auto"
            style={{
              backgroundColor: '#7c3aed18',
              color: '#a78bfa',
              border: '1px solid #7c3aed33',
            }}
            onMouseEnter={(e) => {
              e.currentTarget.style.backgroundColor = '#7c3aed28';
            }}
            onMouseLeave={(e) => {
              e.currentTarget.style.backgroundColor = '#7c3aed18';
            }}
            aria-label="Share tree"
          >
            <Share2 className="w-3.5 h-3.5" />
            Share
          </button>
        )}
      </div>

      {/* Presence bar — shows online avatars */}
      <PresenceBar
        remotePresence={remotePresence}
        localUserId={localUserId}
        onlineCount={(remotePresence.size || 0) + 1}
      />

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
      <div className="flex-1 min-h-0 relative">
        <TreeCanvas
          tree={tree}
          onSelectionChange={handleSelectionChange}
          focusNodeId={focusNodeId}
          nodesDraggable={!isViewer}
          collaborativeCursors={
            <CollaborativeCursors
              remotePresence={remotePresence}
              localUserId={localUserId}
            />
          }
        />
      </div>

      {/* Message composer — bottom-docked, disabled for viewers */}
      <MessageComposer
        onSend={handleSendMessage}
        disabled={!tree.isReady}
        readOnly={isViewer}
        placeholder={
          isViewer
            ? 'View-only mode — request edit access to contribute'
            : 'Type a message...'
        }
      />

      {/* Share dialog */}
      <ShareDialog
        open={showShareDialog}
        onClose={() => setShowShareDialog(false)}
        treeId={treeId ?? ''}
        members={members}
        onPermissionChange={handlePermissionChange}
        onRemoveMember={handleRemoveMember}
        onInvite={handleInvite}
      />
    </div>
  );
}
