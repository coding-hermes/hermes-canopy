/**
 * Hermes Canopy — Reference inspector (SPEC-PL-06 §7.3, §8.2, §9.3)
 *
 *   ┌ Context sources ─────────────────────────── 4 references ┐
 *   │ Context from 2 branches · nearest common ancestor 0191… │
 *   │ ── branch 0191…10 ──                                    │
 *   │ ● R1  Message A            lex · 09:48  ⑂ 0191…10       │
 *   │ ● R2  Message B            lex · 09:51  ⑂ 0191…10       │
 *   │ ── branch 0191…20 ──                                    │
 *   │ ● R3  Message C            lex · 10:02  ⑂ 0191…20       │
 *   │ manifest 91a2e5… · 1,460 / 8,192 tokens · 1 truncated   │
 *   │ [ View context ]                                        │
 *   └─────────────────────────────────────────────────────────┘
 *
 * Sits in the page, under the canvas — never inside a node card — because
 * §7.1 requires the source list to be reachable without the graph canvas
 * (the Canvas 2D fallback renders no DOM nodes at all).
 *
 * §8.2 is a hard constraint on the wording: a multi-reference reply is a
 * plain `message` whose provenance spans branches. It may be called a
 * "synthetic context merge point" and its sources may be grouped by branch
 * root, but it must NOT be labelled "resolved", "merged" or "synthesis".
 * Every derivation lives in lib/multiReference.ts; this file paints.
 */

import { useState } from 'react';
import { ChevronDown, ChevronRight, GitFork, Layers } from 'lucide-react';
import {
  formatTokenCount,
  formatTokenUsage,
} from '../lib/contextManifest.ts';
import { disambiguateNodeIds, shortNodeId } from '../lib/nodeShortId.ts';
import type {
  MultiReferenceMetadata,
  ReferenceInspectorSource,
  ReferenceSourceNodeInfo,
} from '../lib/multiReference.ts';
import { buildReferenceInspectorModel } from '../lib/multiReference.ts';
import { useReferenceContext } from '../hooks/useReferenceContext.ts';
import ReferenceSourceList from './ReferenceSourceList.tsx';
import { token } from '../theme.ts';

// ─── Props ─────────────────────────────────────────────────────────────

export interface ReferenceInspectorPanelProps {
  /** The selected node, or null when nothing is selected. */
  nodeId: string | null;
  /** `metadata.multi_reference` of that node, when it is a reference reply. */
  metadata: MultiReferenceMetadata | null;
  /** Whether the detail body is expanded. */
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Resolve a source node in the local replica (author, time, body). */
  resolveNode?: (id: string) => ReferenceSourceNodeInfo | null | undefined;
  /**
   * The reply's persisted reference-edge metadata keyed by source — where
   * `color_key` and `source_label` actually live (§5.2).
   */
  edgeMeta?: (sourceNodeId: string) => { colorKey?: string; sourceLabel?: string } | null;
  /** Real author display names, when the page has them. */
  authorNames?: ReadonlyMap<string, string>;
  /** §7.2 hover sync — reports the pointed-at source to the canvas. */
  onHighlightChange?: (sourceNodeId: string | null) => void;
  /** The source the canvas currently has highlighted. */
  highlightedNodeId?: string | null;
}

// ─── Panel ─────────────────────────────────────────────────────────────

export default function ReferenceInspectorPanel({
  nodeId,
  metadata,
  open,
  onOpenChange,
  resolveNode,
  edgeMeta,
  authorNames,
  onHighlightChange,
  highlightedNodeId,
}: ReferenceInspectorPanelProps) {
  const { context, loading, error, request } = useReferenceContext(nodeId);
  const [showFullHash, setShowFullHash] = useState(false);

  if (!nodeId || !metadata) return null;

  // §7.3 view model: canonical order, labels, colours, branch groups and the
  // wire-backed extras (truncation, live token usage) when a §9.3 read has
  // happened. The replica lookup supplies author + timestamp.
  const model = buildReferenceInspectorModel({
    nodeId,
    metadata,
    sources: context?.sources ?? null,
    branchSpan: context?.branchSpan ?? metadata.branchSpan,
    ...(context?.manifestHash ? { manifestHash: context.manifestHash } : {}),
    ...(context ? { tokenBudget: context.tokenBudget, tokensUsed: context.tokensUsed } : {}),
    ...(resolveNode ? { resolveNode } : {}),
    ...(edgeMeta ? { edgeMeta } : {}),
    ...(authorNames ? { authorNames } : {}),
  });
  if (!model) return null;

  const labelMap = disambiguateNodeIds(model.sources.map((s) => s.nodeId));
  const ancestorLabel = model.commonAncestorId
    ? labelMap.get(model.commonAncestorId) ?? shortNodeId(model.commonAncestorId)
    : '';

  /** §8.2: group by branch root only when the selection spans branches. */
  const grouped = model.isSyntheticMergePoint && model.branchGroups.length > 1;

  return (
    <aside
      data-testid="reference-inspector"
      aria-label={`Reference sources for ${model.badge}`}
      className="glass shrink-0 border-t border-line-subtle px-3 py-2"
    >
      <div className="flex items-center gap-2">
        <button
          type="button"
          onClick={() => onOpenChange(!open)}
          aria-expanded={open}
          data-testid="reference-inspector-toggle"
          className="flex min-w-0 flex-1 items-center gap-1.5 rounded-md text-left transition-colors hover:text-accent"
        >
          {open ? (
            <ChevronDown className="h-3.5 w-3.5 shrink-0 text-content-muted" />
          ) : (
            <ChevronRight className="h-3.5 w-3.5 shrink-0 text-content-muted" />
          )}
          <Layers className="h-3 w-3 shrink-0 text-content-muted" aria-hidden="true" />
          <span className="text-xs font-medium text-content-primary" data-testid="reference-inspector-badge">
            {model.badge}
          </span>
          {model.isSyntheticMergePoint && model.branchSpanLabel && (
            <span
              data-testid="reference-merge-marker"
              className="inline-flex items-center gap-1 rounded-full bg-surface-input px-1.5 py-0.5 text-[10px] text-content-secondary"
            >
              <GitFork className="h-2.5 w-2.5" aria-hidden="true" />
              {model.branchSpanLabel}
            </span>
          )}
        </button>
      </div>

      {open && (
        <div data-testid="reference-inspector-detail" className="mt-1.5">
          {model.isSyntheticMergePoint && (
            <p data-testid="reference-common-ancestor" className="text-[11px] text-content-muted">
              Synthetic context merge point · nearest common ancestor{' '}
              <span className="font-mono">{ancestorLabel}</span>
            </p>
          )}

          {grouped ? (
            model.branchGroups.map((group) => (
              <section
                key={group.branchRootId || 'ungrouped'}
                data-testid="reference-branch-group"
                data-branch-root={group.branchRootId}
                className="mt-1.5"
              >
                <h4 className="text-[11px] font-medium uppercase tracking-wide text-content-muted">
                  Branch <span className="font-mono">{shortNodeId(group.branchRootId)}</span> ·{' '}
                  {group.sources.length}
                </h4>
                <ReferenceSourceList
                  sources={group.sources}
                  {...(highlightedNodeId ? { highlightedNodeId } : {})}
                  {...(onHighlightChange ? { onHighlightChange } : {})}
                />
              </section>
            ))
          ) : (
            <div className="mt-1.5">
              <ReferenceSourceList
                sources={model.sources}
                {...(highlightedNodeId ? { highlightedNodeId } : {})}
                {...(onHighlightChange ? { onHighlightChange } : {})}
              />
            </div>
          )}

          {/* Manifest hash + token allocation + truncation indicators (§7.3) */}
          <div className="mt-2 space-y-0.5 text-[11px] text-content-muted">
            <p data-testid="reference-manifest-hash">
              manifest{' '}
              <button
                type="button"
                onClick={() => setShowFullHash((v) => !v)}
                className="font-mono hover:text-accent"
                title={model.manifestHash}
                aria-label="Toggle full manifest hash"
              >
                {model.manifestHash
                  ? showFullHash
                    ? model.manifestHash
                    : shortNodeId(model.manifestHash)
                  : 'not recorded'}
              </button>
            </p>
            {model.tokenBudget > 0 && (
              <p data-testid="reference-token-allocation">
                {formatTokenUsage(model.tokensUsed, model.tokenBudget)}
              </p>
            )}
            {model.truncatedCount > 0 && (
              <p data-testid="reference-truncation-note" className="text-status-warning">
                {model.truncatedCount} source{model.truncatedCount === 1 ? '' : 's'} truncated
                {model.omittedTokens > 0
                  ? ` · ${formatTokenCount(model.omittedTokens)} tokens omitted`
                  : ''}
              </p>
            )}
            {context?.sourceChangedSinceCreation && (
              <p data-testid="reference-source-changed" className="text-status-warning">
                A source changed since this reply was created.
              </p>
            )}
          </div>

          {/* §7.3: "View context" — the §9.3 provenance read. */}
          <div className="mt-1.5 flex items-center gap-2">
            <button
              type="button"
              data-testid="reference-view-context"
              onClick={request}
              disabled={loading}
              className="rounded-md border border-line-subtle px-2 py-0.5 text-[11px] text-content-secondary transition-colors hover:text-accent disabled:opacity-60"
            >
              {loading ? 'Loading context…' : 'View context'}
            </button>
            {error && !loading && (
              <span data-testid="reference-context-error" className="text-[11px] text-content-muted">
                Reference context unavailable.
              </span>
            )}
          </div>
        </div>
      )}

      {/* Colour-independent identifiers for screen readers, always present. */}
      <span className="sr-only">{model.ariaDescription}</span>
      <span className="sr-only" style={{ color: token.contentFaint }}>
        {model.sources.map((s: ReferenceInspectorSource) => s.label).join(', ')}
      </span>
    </aside>
  );
}
