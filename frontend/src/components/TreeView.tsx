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
import { useParams, useSearchParams } from 'react-router-dom';
import { Share2 } from 'lucide-react';
import TreeCanvas from '../components/TreeCanvas.tsx';
import NavigationBar from '../components/NavigationBar.tsx';
import MessageComposer, {
  type PinnedNode,
} from '../components/MessageComposer.tsx';
import PresenceBar from '../components/PresenceBar.tsx';
import CollaborativeCursors from '../components/CollaborativeCursors.tsx';
import ShareDialog from '../components/ShareDialog.tsx';
import ContextManifestPanel from '../components/ContextManifestPanel.tsx';
import ContextRunIndicator from '../components/ContextRunIndicator.tsx';
import ContextAuditDialog from '../components/ContextAuditDialog.tsx';
import { useGatewayRuns } from '../hooks/useGatewayRuns.ts';
import {
  createTreeDoc,
  bindIndexedDB,
  seedDemoTree,
  mergeBackendNodes,
  mergeBackendEdges,
  type BackendNodePayload,
  type TreeYDoc,
} from '../stores/treeStore.ts';
import {
  rootNodeIdOf,
  subtreeRequestPath,
  toEdgePayloads,
  toGraphNodePayloads,
  type RawSubtree,
} from '../lib/treeGraph.ts';
import {
  flowEdgeIsReference,
  normaliseMultiReferenceMetadata,
  type MultiReferenceMetadata,
  type ReferenceHighlight,
  type ReferenceSourceNodeInfo,
} from '../lib/multiReference.ts';
import ReferenceInspectorPanel from '../components/ReferenceInspectorPanel.tsx';
import { SSESyncProvider } from '../stores/yjsProvider.ts';
import { resolveDemoAliasSync, storeTreeId } from '../lib/activeTree';
import { useYjsTree } from '../stores/useYjsTree.ts';
import { usePresence } from '../hooks/usePresence.ts';
import type {
  PermissionLevel,
  ShareInvitePayload,
} from '../types/multiUser.ts';
import { getColorForUser } from '../types/multiUser.ts';
import { token, palette, alpha } from '../theme.ts';
import { apiGet, apiPost } from '../lib/api.ts';
import { getContextPreview } from '../lib/contextApi.ts';
import {
  referencePreflight,
  createMultiReferenceReply,
} from '../lib/referenceApi.ts';
import {
  buildCreateNodeBody,
  buildSendMetadata,
  composerPlaceholder,
} from '../lib/composer.ts';
import { GitMerge } from 'lucide-react';

/**
 * Minimum sources a multi-reference selection needs (§5.1). The backend
 * rejects below this (REFERENCE_SOURCE_COUNT_TOO_LOW); the affordance
 * stays disabled here so that rejection is unreachable from the UI.
 */
const MIN_SYNTHESIS_SOURCES = 2;
/**
 * Profile context budget sent with the §9.1 preflight. 16384 is the value
 * the recorded contract exercises; the server quotes the actual allocation
 * in the envelope's `context_budget` (§9.1).
 */
const SYNTHESIS_PROFILE_BUDGET = 16384;
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

  /**
   * /tree/demo alias resolution — E2E-ONLY TEST FIXTURE path (GAP-051).
   * The E2E battery navigates to /tree/demo directly; the backend has no
   * 'demo' tree, so the raw id 400s and the canvas stays an empty
   * "Untitled Tree". Resolve the alias to the seeded demo tree by label
   * (stable UUID fallback), and persist the resolved UUID so no component
   * ever sends tree_id=demo. Product nav no longer points here (the Tree
   * View sidebar item was removed) — normal users reach trees via /trees.
   */
  const [resolvedTreeId, setResolvedTreeId] = useState<string | null>(null);
  useEffect(() => {
    if (!treeId) return;
    const id = resolveDemoAliasSync(treeId);
    setResolvedTreeId(id);
    if (treeId === 'demo') storeTreeId(id);
  }, [treeId]);

  // Initialize Yjs document + providers
  useEffect(() => {
    if (!resolvedTreeId) {
      setError('No tree ID provided');
      return;
    }

    try {
      // Create Y.Doc
      const treeDoc = createTreeDoc(resolvedTreeId);
      docRef.current = treeDoc;

      // Bind IndexedDB persistence
      bindIndexedDB(resolvedTreeId, treeDoc.ydoc);

      // Connect SSE sync provider
      const provider = new SSESyncProvider(treeDoc, {
        treeId: resolvedTreeId,
        onConnected: () => {
          console.log(`[TreeView] SSE connected for tree ${resolvedTreeId}`);
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

      // Hydrate the local replica from the authoritative backend (BUG-032).
      // The Yjs doc starts empty; without this bridge an existing tree's
      // nodes never reach the React Flow canvas. Idempotent merge — safe
      // against IndexedDB restoring a partially-populated doc.
      void (async () => {
        try {
          const [treeRes, nodesRes] = await Promise.all([
            apiGet<{ title?: string; description?: string }>(
              `/trees/${resolvedTreeId}`,
            ),
            apiGet<{ nodes: BackendNodePayload[] }>(
              `/trees/${resolvedTreeId}/nodes`,
            ),
          ]);
          treeDoc.ydoc.transact(() => {
            if (!treeDoc.meta.get('title') && treeRes?.title) {
              treeDoc.meta.set('title', treeRes.title);
            }
            if (!treeDoc.meta.get('description') && treeRes?.description) {
              treeDoc.meta.set('description', treeRes.description);
            }
          });
          const nodes = nodesRes?.nodes ?? [];

          /*
           * SPEC-PL-06 §7.1: convergence edges only exist in the graph read.
           * The REST node list carries no edges, and a multi-reference reply
           * must render its N `reference` edges with their PERSISTED ids —
           * never a synthetic edge inferred from parent_id.
           *
           * The root is derived from the node list already in hand (no new
           * route); `max_depth=0` is the whole tree, and the subtree walk
           * traverses edges, so reference edges are included.
           *
           * Edges are merged BEFORE the node list so every edge the database
           * already holds keeps its own id: the lineage synthesis below then
           * finds nothing left to invent. Best-effort — a graph read that
           * fails must not cost the user the node hydration.
           */
          const rootId = rootNodeIdOf(nodes);
          let graph: RawSubtree | null = null;
          if (rootId) {
            try {
              graph = await apiGet<RawSubtree>(
                subtreeRequestPath(resolvedTreeId, rootId),
              );
              const edgesAdded = mergeBackendEdges(treeDoc, toEdgePayloads(graph?.edges));
              if (edgesAdded > 0) {
                console.log(
                  `[TreeView] hydrated ${edgesAdded} edges for tree ${resolvedTreeId}`,
                );
              }
            } catch (err) {
              console.warn('[TreeView] graph edge hydration failed', err);
            }
          }

          const added = mergeBackendNodes(treeDoc, nodes);
          if (added > 0) {
            console.log(
              `[TreeView] hydrated ${added} nodes for tree ${resolvedTreeId}`,
            );
          }

          // Nodes the graph knows and the node list did not (metadata-free
          // fallback; existing nodes are skipped, so the richer REST payload
          // above always wins).
          if (graph) {
            mergeBackendNodes(treeDoc, toGraphNodePayloads(graph.nodes));
          }
        } catch (err) {
          console.warn('[TreeView] hydration failed', err);
        }
      })();

      // Expose Y.Doc and seed function for E2E tests ONLY (GAP-051).
      // __canopySeedDemoTree is the deterministic local-doc fixture the
      // E2E battery uses (tree-rendering/visual-regression); it is never
      // called by product code and does not render anything on its own.
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
  }, [resolvedTreeId]);

  const tree = useYjsTree(doc);

  /*
   * Deep-link focus (UI-08). The Nodes page links a node's id here —
   * `/tree/{treeId}?node={nodeId}` — because TreeView IS the node detail
   * context in this product: it is where a node's parents, replies and
   * content actually live, so there is no separate `/nodes/:id` route to
   * invent.
   *
   * Read-only on purpose. Writing the param back would feed the router
   * update into this effect and spin a render loop (BUG hermes-canopy
   * UI-02). For the same reason the dependency is the param VALUE, not
   * the `searchParams` object — its identity changes every render.
   *
   * `hasNode` gates on the node existing in the replica: Yjs hydrates
   * asynchronously, so on a cold load the param arrives before the graph
   * does and an ungated effect would focus nothing and never retry.
   */
  const [searchParams] = useSearchParams();
  const deepLinkNodeId = searchParams.get('node');
  const hasNode = deepLinkNodeId
    ? tree.nodes.some((n) => n.id === deepLinkNodeId)
    : false;

  useEffect(() => {
    if (!deepLinkNodeId || !hasNode) return;
    setSelectedNodeId(deepLinkNodeId);
    setFocusNodeId(deepLinkNodeId);
  }, [deepLinkNodeId, hasNode]);

  /*
   * Manifest deep-link (GAP-094). The dashboard's "Recent trees" resume
   * list links here with `?manifest=1` — arriving from a resume click means
   * the reader wants the context manifest surfaced, not just the canvas.
   * The panel itself always renders when a node is selected; what this
   * actually drives is the SELECTION: a tree whose replica has hydrated
   * gets its newest live node selected on arrival, which is what makes the
   * inspector show a manifest instead of nothing. Read-only on the params
   * object and gated on the same Yjs-has-hydrated signal as the node
   * deep-link above (identity of `searchParams` changes every render —
   * BUG hermes-canopy UI-02).
   */
  const manifestDeepLink = searchParams.get('manifest') === '1';
  const newestLiveNode = useMemo(() => {
    let newest: { id: string; createdAt: number } | null = null;
    for (const n of tree.nodes) {
      const created = Date.parse(n.data?.createdAt ?? '') || 0;
      if (!newest || created > newest.createdAt) {
        newest = { id: n.id, createdAt: created };
      }
    }
    return newest?.id ?? null;
  }, [tree.nodes]);

  useEffect(() => {
    if (!manifestDeepLink || !newestLiveNode) return;
    // An explicit `?node=` is a deliberate choice and wins; so does a
    // selection the reader already made. Only the bare resume link (no
    // node param, nothing selected yet) picks the newest node.
    if (deepLinkNodeId || selectedNodeId) return;
    setSelectedNodeId(newestLiveNode);
  }, [manifestDeepLink, newestLiveNode, selectedNodeId, deepLinkNodeId]);

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

  // ── SPEC-PL-06 §7: multi-reference replies ──────────────────────────

  /** Whether the source-list inspector below the canvas is expanded. */
  const [referencePanelOpen, setReferencePanelOpen] = useState(false);

  // ── DF-HERMES-CANOPY-42: synthesize from selected nodes ─────────────
  //
  // The two-step write path (§9.1 preflight → §9.2 create) had zero PWA
  // callers. This is the human-reachable flow: pick ≥2 nodes, mint the
  // signed selection token, write the synthesis. The canvas stays
  // single-select (its click contract drives the inspector); the multi-
  // select lives here as an explicit checklist in the header bar, the
  // same place the page already puts page-level actions.

  /** Explicit multi-select for the synthesis flow (selection order kept). */
  const [synthesisSelection, setSynthesisSelection] = useState<string[]>([]);
  /**
   * Flow phase: `idle` shows the affordance; `previewing` holds a live
   * selection token with the composer open; `submitting` is the in-flight
   * create. The token only ever travels preflight → create — it is never
   * parsed or persisted client-side.
   */
  const [synthesisPhase, setSynthesisPhase] = useState<
    'idle' | 'previewing' | 'submitting'
  >('idle');
  const [synthesisError, setSynthesisError] = useState<string | null>(null);
  const [synthesisContent, setSynthesisContent] = useState('');

  /** Toggle one node in the synthesis selection (insertion order = §5.1 R#). */
  const toggleSynthesisSelection = useCallback((nodeId: string) => {
    setSynthesisError(null);
    setSynthesisSelection((prev) =>
      prev.includes(nodeId)
        ? prev.filter((id) => id !== nodeId)
        : [...prev, nodeId],
    );
  }, []);

  /**
   * The signed selection token from the last successful preflight (§9.1).
   * Held in a ref rather than state: it is a write-path credential, not
   * something any render consumes, and it must be readable from
   * `submitSynthesis` without joining a callback dependency chain.
   */
  const synthesisTokenRef = useRef<string | null>(null);

  /** Step 1 — preflight the selection; on 200 open the composer (§9.1). */
  const startSynthesis = useCallback(async () => {
    if (!treeId) return;
    if (synthesisSelection.length < MIN_SYNTHESIS_SOURCES) return;
    setSynthesisError(null);
    try {
      const envelope = await referencePreflight(treeId, synthesisSelection, {
        profileContextBudget: SYNTHESIS_PROFILE_BUDGET,
      });
      // Opaque token handoff (§9.2): stored verbatim, spent once in step 2.
      synthesisTokenRef.current = envelope.selection_token;
      setSynthesisContent('');
      setSynthesisPhase('previewing');
    } catch (err) {
      synthesisTokenRef.current = null;
      setSynthesisError(
        err instanceof Error ? err.message : String(err),
      );
    }
  }, [treeId, synthesisSelection]);

  /** Step 2 — spend the token, then mirror the 201 node into the replica. */
  const submitSynthesis = useCallback(async () => {
    if (!treeId || synthesisPhase !== 'previewing') return;
    const content = synthesisContent.trim();
    if (!content) return;
    setSynthesisError(null);
    setSynthesisPhase('submitting');
    try {
      const created = await createMultiReferenceReply(treeId, {
        selection_token: synthesisTokenRef.current ?? '',
        content,
        content_format: 'markdown',
      });
      // BUG-032 pattern: the canvas renders from the Yjs doc, not the REST
      // response — merge the created node + its PERSISTED reference edges
      // (§7.1: never inferred from parent_id) so it appears immediately and
      // the inspector badge/edge styling work like any hydrated reply.
      if (docRef.current) {
        const createdNode = created?.node;
        const createdEdges = created?.edges ?? [];
        if (createdNode?.id) {
          mergeBackendEdges(docRef.current, [
            ...toEdgePayloads(
              createdEdges.map((e) => ({
                id: e.id,
                source_id: e.source_node_id,
                target_id: e.target_node_id,
                edge_type: e.edge_type,
                metadata: e.metadata ?? {},
              })),
            ),
            // §7.1 display anchor: the created node's parent_id is the
            // PRIMARY SOURCE, and one of the reference edges already
            // carries that pair — without this guard the replica would
            // double-wire R1. addEdges dedupes by (source, target, type),
            // so the guard only fires when the anchor is edge-less.
            ...(createdNode.parent_id &&
            !createdEdges.some((e) => e.source_node_id === createdNode.parent_id)
              ? [
                  {
                    id: `${createdNode.parent_id}->${createdNode.id}:anchor`,
                    sourceId: createdNode.parent_id,
                    targetId: createdNode.id,
                    edgeType: 'reply',
                    metadata: {},
                  },
                ]
              : []),
          ]);
          mergeBackendNodes(docRef.current, [
            {
              id: createdNode.id,
              parentId: createdNode.parent_id,
              content: createdNode.content,
              contentFormat: createdNode.content_format,
              nodeType: createdNode.node_type,
              authorId: createdNode.author_id,
              metadata: createdNode.metadata,
              createdAt: createdNode.created_at,
            },
          ]);
        }
      }
      setSynthesisPhase('idle');
      setSynthesisContent('');
      setSynthesisSelection([]);
    } catch (err) {
      setSynthesisError(err instanceof Error ? err.message : String(err));
      setSynthesisPhase('idle');
    }
  }, [treeId, synthesisPhase, synthesisContent]);

  /** Cancel / dismiss: drop the composer and any error, keep the selection. */
  const cancelSynthesis = useCallback(() => {
    setSynthesisPhase('idle');
    setSynthesisContent('');
    setSynthesisError(null);
  }, []);


  // ── GAP-084: node-scoped gateway runs ───────────────────────────────

  /**
   * The live run registry. TreeView is the ONLY surface where a node is
   * selected, so it is the only place a run can honestly be started
   * against a compiled context — the composer here posts `node_id`, and
   * the registry is where the manifest that came back is read from.
   */
  const { runs: gatewayRuns, startRun } = useGatewayRuns();

  /** Run id of the last context-aware run started from this page. */
  const [contextRunId, setContextRunId] = useState<string | null>(null);

  /**
   * Failure of the last context run, in the server's own wording. The
   * composer already shows it inline (it re-throws so the text is kept);
   * this copy lives beside the indicator so the provenance surface does
   * not silently disagree with what the user just saw.
   */
  const [contextRunError, setContextRunError] = useState<string | null>(null);

  /**
   * The run record for a run started from here, once the registry has it.
   *
   * `useGatewayRuns` polls `/gateway/runs`, so between the POST returning
   * the new id and the next poll the record does not exist yet. That
   * window is the honest reason the indicator renders nothing: a run whose
   * provenance has not been read back yet must not be described.
   */
  const latestContextRun = useMemo(() => {
    if (!contextRunId) return null;
    return gatewayRuns.find((run) => run.run_id === contextRunId) ?? null;
  }, [gatewayRuns, contextRunId]);

  /*
   * GAP-096: audit-before-send gate (GAP-080 phase 5b).
   *
   * `pendingRun` is armed the moment the user hits "Run with context".
   * While it is non-null NO run request has been made — the POST only
   * ever happens from the audit dialog's Send. The dialog itself previews
   * the compile (GET /context/{node_id}) and requires an explicit
   * Send / Adjust / Cancel before anything is sent.
   */
  const [pendingRun, setPendingRun] = useState<{ message: string } | null>(
    null,
  );

  /**
   * Start a run against the selected node's compiled context — THROUGH
   * the audit gate (GAP-096).
   *
   * With a node selected this no longer POSTs immediately: it arms the
   * pending run and opens the audit dialog. The composer's promise
   * contract is preserved — the returned promise resolves only when the
   * user confirms Send (so the textarea clears then), and rejects on
   * Cancel or failure (so the user's text is kept). The actual POST and
   * its error handling live in `confirmPendingRun`.
   *
   * With NO node selected the pre-GAP-096 behaviour is byte-identical:
   * reject with the same message, make no request of any kind.
   */
  const handleRunWithContext = useCallback(
    (message: string) => {
      if (!selectedNodeId) {
        return Promise.reject(
          new Error('Select a node to run with its compiled context.'),
        );
      }
      setContextRunError(null);
      setPendingRun({ message });
      // Resolve only after Send completes; see `confirmPendingRun`.
      return new Promise<void>((resolve, reject) => {
        pendingRunResolveRef.current = { resolve, reject };
      });
    },
    [selectedNodeId],
  );

  /** Resolve/reject handles for the promise `handleRunWithContext` returned. */
  const pendingRunResolveRef = useRef<{
    resolve: () => void;
    reject: (reason?: unknown) => void;
  } | null>(null);

  /**
   * The dialog's Send: proceed with `token_budget` = the budget the
   * previewed manifest was actually computed with. Server errors keep
   * the composer's text (reject) and surface in the existing inline row.
   */
  const confirmPendingRun = useCallback(
    async (message: string, tokenBudget?: number) => {
      const nodeId = selectedNodeId;
      if (!nodeId) {
        // Safety net: the dialog only opens with a selection.
        pendingRunResolveRef.current?.reject(
          new Error('Select a node to run with its compiled context.'),
        );
        pendingRunResolveRef.current = null;
        setPendingRun(null);
        return;
      }
      try {
        const runId = await startRun(message, undefined, nodeId, tokenBudget);
        setContextRunId(runId);
        pendingRunResolveRef.current?.resolve();
        pendingRunResolveRef.current = null;
        setPendingRun(null);
      } catch (err) {
        setContextRunError(err instanceof Error ? err.message : String(err));
        pendingRunResolveRef.current?.reject(err);
        pendingRunResolveRef.current = null;
        setPendingRun(null);
        throw err;
      }
    },
    [selectedNodeId, startRun],
  );

  /** The dialog's Cancel / dismissal: close with NO request of any kind. */
  const cancelPendingRun = useCallback(() => {
    pendingRunResolveRef.current?.reject(
      new Error('Context run cancelled before send.'),
    );
    pendingRunResolveRef.current = null;
    setPendingRun(null);
  }, []);

  /** Stable transport for the audit dialog's preview fetch. */
  const fetchContextPreview = useCallback(
    (budget: number | null) => getContextPreview(selectedNodeId ?? '', budget),
    [selectedNodeId],
  );

  /**
   * §7.2 hover sync. Two independent pointers feed one highlight: the source
   * list (a row) and the canvas (an edge). The edge id is resolved back to
   * its source so a hover on either side highlights the same pair.
   */
  const [hoveredSourceId, setHoveredSourceId] = useState<string | null>(null);
  const [hoveredEdgeId, setHoveredEdgeId] = useState<string | null>(null);

  const referenceHighlight = useMemo<ReferenceHighlight | null>(() => {
    if (hoveredEdgeId) {
      const source =
        tree.edges.find((edge) => edge.id === hoveredEdgeId)?.source ?? null;
      return { edgeId: hoveredEdgeId, sourceId: source };
    }
    if (hoveredSourceId) return { sourceId: hoveredSourceId };
    return null;
  }, [hoveredEdgeId, hoveredSourceId, tree.edges]);

  /** `metadata.multi_reference` of the selected node, when it has one. */
  const selectedReferenceMetadata = useMemo<MultiReferenceMetadata | null>(() => {
    if (!selectedNodeId) return null;
    const node = tree.nodes.find((n) => n.id === selectedNodeId);
    const metadata = node?.data?.metadata as Record<string, unknown> | undefined;
    return normaliseMultiReferenceMetadata(metadata?.multi_reference);
  }, [selectedNodeId, tree.nodes]);

  /**
   * Source lookup against the replica the canvas is already rendering —
   * author, timestamp and body come from here, never from a second fetch.
   */
  const resolveSourceNode = useCallback(
    (id: string): ReferenceSourceNodeInfo | null => {
      const node = tree.nodes.find((n) => n.id === id);
      if (!node) return null;
      return {
        authorId: String(node.data.authorId ?? ''),
        createdAt: String(node.data.createdAt ?? ''),
        content: String(node.data.content ?? ''),
        label: String(node.data.label ?? ''),
      };
    },
    [tree.nodes],
  );

  /**
   * §5.2 renderer metadata for the selected reply's reference edges, keyed by
   * source node. The edges are in the local replica (§7.1 hydration) — this
   * is where the server-computed `color_key` / `source_label` live.
   */
  const referenceEdgeMeta = useCallback(
    (sourceNodeId: string): { colorKey?: string; sourceLabel?: string } | null => {
      if (!selectedNodeId) return null;
      const edge = tree.edges.find(
        (e) =>
          e.target === selectedNodeId &&
          e.source === sourceNodeId &&
          flowEdgeIsReference(e),
      );
      if (!edge) return null;
      const data = (edge.data ?? {}) as { colorKey?: string; sourceLabel?: string };
      return data;
    },
    [tree.edges, selectedNodeId],
  );

  /**
   * §7.1: the badge on a reply opens its source list. The list lives in the
   * page (not in the node card), so it remains reachable when the canvas is
   * the Canvas 2D fallback.
   */
  const handleOpenReferences = useCallback((nodeId: string) => {
    setSelectedNodeId(nodeId);
    setReferencePanelOpen(true);
  }, []);

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

      const created = await apiPost<{ node: BackendNodePayload }>(
        `/trees/${treeId}/nodes`,
        body,
      );

      // BUG-032: mirror the created node into the local Yjs replica so it
      // appears on the canvas immediately. The canvas renders from the Yjs
      // doc, not the REST response — without this the message lands in the
      // backend but never shows up in the graph. The POST response wraps
      // the node as { node: {...} }.
      if (created?.node?.id && docRef.current) {
        mergeBackendNodes(docRef.current, [created.node]);
      }

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

        {/*
          DF-HERMES-CANOPY-42 — synthesize from selected nodes. The
          two-step multi-reference flow (§9.1 preflight → §9.2 create):
          check ≥2 nodes here, hit "Synthesize", write the synthesis. The
          checklist doubles as the discoverability hint when nothing is
          selected; the canvas click remains single-select (it drives the
          node inspector).
        */}
        <div
          data-testid="multi-reference-bar"
          className={`flex items-center gap-2 px-2 py-0.5 rounded-lg text-xs ${
            synthesisSelection.length > 0 ? 'ml-auto' : ''
          }`}
          style={{
            backgroundColor: alpha(palette.accent3, 0.08),
            border: `1px solid ${alpha(palette.accent3, 0.2)}`,
          }}
        >
          {tree.isReady && tree.nodes.length >= MIN_SYNTHESIS_SOURCES ? (
            <details
              className="relative"
              onSubmit={(e) => e.preventDefault()}
            >
              <summary
                className="cursor-pointer list-none select-none whitespace-nowrap"
                aria-label="Select nodes to synthesize from"
              >
                Select {synthesisSelection.length > 0 ? `(${synthesisSelection.length})` : 'nodes'} ▾
              </summary>
              <div
                className="absolute right-0 mt-1 max-h-64 overflow-y-auto rounded-lg shadow-lg p-1 z-50 w-64"
                style={{
                  backgroundColor: token.surfacePanel,
                  border: `1px solid ${alpha(palette.accent3, 0.25)}`,
                }}
              >
                {tree.nodes.map((n) => (
                  <label
                    key={n.id}
                    className="flex items-center gap-2 px-2 py-1 rounded cursor-pointer hover:bg-white/5"
                  >
                    <input
                      type="checkbox"
                      value={n.id}
                      checked={synthesisSelection.includes(n.id)}
                      onChange={() => toggleSynthesisSelection(n.id)}
                    />
                    <span className="truncate text-content-primary">
                      {String(n.data?.label ?? n.data?.content ?? n.id).slice(0, 80)}
                    </span>
                  </label>
                ))}
              </div>
            </details>
          ) : (
            <span className="text-content-muted whitespace-nowrap">
              Select nodes above to synthesize from them
            </span>
          )}
          <button
            data-testid="multi-reference-synthesize"
            onClick={() => {
              void startSynthesis();
            }}
            disabled={
              tree.isReady === false ||
              isViewer ||
              synthesisPhase !== 'idle' ||
              synthesisSelection.length < MIN_SYNTHESIS_SOURCES
            }
            className="flex items-center gap-1.5 px-2.5 py-1 rounded-lg text-xs font-medium transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
            style={{
              backgroundColor: alpha(palette.accent3, synthesisSelection.length >= MIN_SYNTHESIS_SOURCES ? 0.18 : 0.1),
              color: token.accent3,
              border: `1px solid ${alpha(palette.accent3, 0.24)}`,
            }}
            title={`Synthesize one reply from the selected nodes (pick at least ${MIN_SYNTHESIS_SOURCES}, up to 20)`}
            aria-label="Synthesize from selected nodes"
          >
            <GitMerge className="w-3.5 h-3.5" />
            Synthesize{synthesisSelection.length >= MIN_SYNTHESIS_SOURCES ? ` (${synthesisSelection.length})` : ''}
          </button>
        </div>

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
          onOpenReferences={handleOpenReferences}
          referenceHighlight={referenceHighlight}
          onReferenceHighlightChange={(highlight) =>
            setHoveredEdgeId(highlight?.edgeId ?? null)
          }
          collaborativeCursors={
            <CollaborativeCursors
              remotePresence={remotePresence}
              localUserId={localUserId}
            />
          }
        />
      </div>

      {/*
        SPEC-PL-06 §7.3 reference inspector — canonical source list, branch
        grouping, manifest hash and token allocation for a multi-reference
        reply. Renders nothing when the selection is not one (§7.1: the
        source list must not require the graph canvas).
      */}
      <ReferenceInspectorPanel
        nodeId={selectedNodeId}
        metadata={selectedReferenceMetadata}
        open={referencePanelOpen}
        onOpenChange={setReferencePanelOpen}
        resolveNode={resolveSourceNode}
        edgeMeta={referenceEdgeMeta}
        authorNames={authorNames}
        highlightedNodeId={referenceHighlight?.sourceId ?? null}
        onHighlightChange={(sourceId) => setHoveredSourceId(sourceId)}
      />

      {/*
        Context manifest inspector (WIRE-002) — what the compiler would
        actually send for the selected node, its token cost against the
        budget, and what it dropped. Sits between the canvas and the
        composer: it describes the node you are about to reply to.
        Renders nothing when there is no selection.
      */}
      {/* BEFORE — what the compiler WOULD send for the selected node. */}
      <ContextManifestPanel nodeId={selectedNodeId} />

      {/*
        AFTER (GAP-084) — the manifest of the run that ACTUALLY happened,
        read back from the run registry. Same promise, other side: the
        panel above previews a compile, this reports one. Renders nothing
        for a context-free run, and nothing while the run it was started
        from has not been polled back into the registry yet.
      */}
      <ContextRunIndicator run={latestContextRun} />

      {contextRunError && (
        <p
          data-testid="context-run-error"
          className="shrink-0 px-3 py-1 text-[11px] text-status-danger"
        >
          Context run failed: {contextRunError}
        </p>
      )}

      {/*
        DF-HERMES-CANOPY-42 step 2 — the synthesis composer. Rendered only
        while a preflight selection token is live (`previewing` /
        `submitting`); Send spends the token (§9.2) and merges the created
        node into the replica, Cancel just drops it.
      */}
      {(synthesisPhase === 'previewing' || synthesisPhase === 'submitting') && (
        <div
          data-testid="multi-reference-composer"
          className="shrink-0 border-t border-line-subtle bg-surface-panel px-3 py-2"
        >
          <textarea
            data-testid="multi-reference-content"
            value={synthesisContent}
            onChange={(e) => setSynthesisContent(e.target.value)}
            placeholder={`Synthesize a reply from ${synthesisSelection.length} selected nodes…`}
            rows={3}
            disabled={synthesisPhase === 'submitting'}
            className="w-full rounded-lg bg-surface-base text-content-primary text-sm p-2 border border-line-subtle focus:outline-none focus:border-accent3"
            aria-label="Synthesis content"
          />
          <div className="flex items-center gap-2 mt-1.5">
            <button
              data-testid="multi-reference-send"
              onClick={() => {
                void submitSynthesis();
              }}
              disabled={synthesisPhase === 'submitting' || !synthesisContent.trim()}
              className="px-2.5 py-1 rounded-lg text-xs font-medium text-white disabled:opacity-40 disabled:cursor-not-allowed"
              style={{ backgroundColor: token.accent3 }}
            >
              {synthesisPhase === 'submitting' ? 'Synthesizing…' : 'Create synthesis'}
            </button>
            <button
              onClick={cancelSynthesis}
              disabled={synthesisPhase === 'submitting'}
              className="px-2.5 py-1 rounded-lg text-xs text-content-muted border border-line-subtle disabled:opacity-40"
            >
              Cancel
            </button>
            <span className="text-[11px] text-content-muted">
              {synthesisSelection.length} sources · reply lands under the first selected node
            </span>
          </div>
        </div>
      )}

      {synthesisError && (
        <p
          data-testid="multi-reference-error"
          className="shrink-0 px-3 py-1 text-[11px] text-status-danger"
          role="alert"
        >
          Synthesis failed: {synthesisError}
        </p>
      )}

      {/*
        GAP-096 audit-before-send gate — the pending run's audit moment.
        While open, NO POST /gateway/runs has happened: the dialog previews
        the compile and requires an explicit Send (which proceeds with the
        budget the manifest was computed with), Adjust (re-preview with a
        new ?budget=), or Cancel (no request at all).
      */}
      {pendingRun && selectedNodeId && (
        <ContextAuditDialog
          nodeId={selectedNodeId}
          message={pendingRun.message}
          getPreview={fetchContextPreview}
          onConfirm={(tokenBudget) => {
            void confirmPendingRun(pendingRun.message, tokenBudget);
          }}
          onClose={cancelPendingRun}
        />
      )}

      {/* Message composer — bottom-docked, disabled for viewers */}
      <MessageComposer
        onSend={handleSendMessage}
        disabled={!tree.isReady}
        readOnly={isViewer}
        onRunWithContext={handleRunWithContext}
        runWithContextDisabled={selectedNodeId === null || !tree.isReady || isViewer}
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
