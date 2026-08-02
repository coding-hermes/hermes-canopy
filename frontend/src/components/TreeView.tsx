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

import { useEffect, useMemo, useRef, useState, useCallback } from 'react';
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
import { token, palette, alpha } from '../theme.ts';
import { apiPost } from '../lib/api.ts';
import {
  buildCreateNodeBody,
  buildSendMetadata,
  composerPlaceholder,
} from '../lib/composer.ts';
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

  /**
   * Node the composer will reply to, set by activating a ghost slot on the
   * canvas (UI-04). Kept as state rather than written straight to the graph
   * so the affordance never creates an empty node behind the user's back.
   */
  const [replyToNodeId, setReplyToNodeId] = useState<string | null>(null);

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

  /**
   * Ghost-slot activation (UI-04): arm the composer against the chosen
   * parent, select it and pan to it. The reply itself is created when the
   * user actually sends — clicking a placeholder should never write a node.
   */
  const handleCreateReply = useCallback((parentId: string) => {
    setReplyToNodeId(parentId);
    setSelectedNodeId(parentId);
    setFocusNodeId(null);
    setTimeout(() => setFocusNodeId(parentId), 0);
  }, []);

  /**
   * Author display names for canvas avatars.
   *
   * Presence is the only live identity source in MVP; membership fills in
   * the rest. Avatar colours come from `getColorForUser` either way, so a
   * person looks the same here as in the presence bar.
   */
  const authorNames = useMemo(() => {
    const names = new Map<string, string>();
    for (const member of members) names.set(member.userId, member.userName);
    for (const [id, presence] of remotePresence) {
      if (presence.userName) names.set(id, presence.userName);
    }
    if (localUserId) names.set(localUserId, 'You');
    return names;
  }, [members, remotePresence, localUserId]);

  // Handle message send from MessageComposer — creates a real node.
  //
  // Snake_case body per internal/handler/node_handler.go; an armed ghost
  // slot (UI-04) becomes `parent_id`, otherwise the message is a root.
  // Errors are re-thrown so the composer keeps the user's text and shows
  // the server's own message inline.
  const handleSendMessage = useCallback(
    async (message: string, files: File[], pinned: PinnedNode[]) => {
      if (!treeId) throw new Error('No tree selected.');

      const body = buildCreateNodeBody({
        content: message,
        parentId: replyToNodeId,
        metadata:
          buildSendMetadata({
            files,
            pinnedNodeIds: pinned.map((n) => n.id),
          }) ?? undefined,
      });

      await apiPost<{ node: unknown }>(`/trees/${treeId}/nodes`, body);

      // Sending clears the armed reply target (UI-04 ghost slot).
      setReplyToNodeId(null);
    },
    [treeId, replyToNodeId],
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
          <p className="text-status-danger text-lg mb-2">
            Error loading tree
          </p>
          <p className="text-content-muted text-sm">{error}</p>
        </div>
      </div>
    );
  }

  return (
    <div className="h-full w-full flex flex-col bg-surface-base">
      {/* TreeView page heading for screen readers */}
      <h1 className="sr-only">
        Tree View: {tree.treeTitle || 'Untitled Tree'}
      </h1>
      {/* Tree header bar */}
      <div className="h-10 flex items-center px-4 gap-3 border-b border-line-subtle bg-surface-panel shrink-0">
        <span className="text-sm font-medium text-content-primary">
          🌳 {tree.treeTitle || 'Tree View'}
        </span>
        <span className="text-xs text-content-muted">
          {tree.nodes.length} nodes · {tree.edges.length} edges
        </span>
        {!tree.isReady && (
          <span className="text-xs text-status-warning animate-pulse ml-auto">
            Connecting...
          </span>
        )}

        {/* Share button (non-viewers only) */}
        {!isViewer && (
          <button
            onClick={() => setShowShareDialog(true)}
            className="flex items-center gap-1.5 px-2.5 py-1 rounded-lg text-xs font-medium transition-colors ml-auto"
            style={{
              backgroundColor: alpha(palette.accent2, 0.1),
              color: token.accent2,
              border: `1px solid ${alpha(palette.accent2, 0.24)}`,
            }}
            onMouseEnter={(e) => {
              e.currentTarget.style.backgroundColor = alpha(palette.accent2, 0.18);
            }}
            onMouseLeave={(e) => {
              e.currentTarget.style.backgroundColor = alpha(palette.accent2, 0.1);
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
          authorNames={authorNames}
          {...(isViewer ? {} : { onCreateReply: handleCreateReply })}
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
        placeholder={composerPlaceholder({
          readOnly: isViewer,
          isReply: replyToNodeId !== null,
        })}
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
