/**
 * Hermes Canopy — Reference source list (SPEC-PL-06 §7.1, §7.2, §7.3)
 *
 * The reply's sources, rendered as ORDINARY DOM: a list of focusable links,
 * one per source, each showing its `R#` label, colour swatch, author,
 * timestamp, branch root and content preview (§7.3).
 *
 * §7.1 is explicit that this list must be openable "without relying on the
 * graph canvas" — and at 2000+ nodes the canvas is painted pixels with no
 * DOM nodes at all. The list therefore lives here, in the page chrome,
 * never inside the node card.
 *
 * §7.2's hover contract runs both ways: pointing at a row highlights that
 * source's exact convergence edge and dims the rest (the parent tells the
 * canvas), and a row highlights itself when the canvas reports the pointer
 * on its edge (`highlighted`).
 *
 * Everything it prints comes from `buildReferenceInspectorModel` — this
 * component paints and forwards events, it derives nothing.
 */

import { GitBranch, Scissors } from 'lucide-react';
import type { ReferenceInspectorSource } from '../lib/multiReference.ts';
import { formatNodeTime } from '../lib/nodeCard.ts';
import { shortNodeId } from '../lib/nodeShortId.ts';
import { token, alpha } from '../theme.ts';

// ─── Props ─────────────────────────────────────────────────────────────

export interface ReferenceSourceListProps {
  /** Sources in canonical order (§3.5 invariant 5). */
  sources: readonly ReferenceInspectorSource[];
  /** The source whose edge is currently highlighted, if any. */
  highlightedNodeId?: string | null;
  /** Reports row hover/focus so the canvas can highlight the edge (§7.2). */
  onHighlightChange?: (sourceNodeId: string | null) => void;
}

/** Branch groups, when the model has them (§8.2). */
export interface ReferenceSourceGroup {
  branchRootId: string;
  sources: ReferenceInspectorSource[];
}

// ─── Row ───────────────────────────────────────────────────────────────

function SourceRow({
  source,
  highlighted,
  onHighlightChange,
}: {
  source: ReferenceInspectorSource;
  highlighted: boolean;
  onHighlightChange?: (sourceNodeId: string | null) => void;
}) {
  const report = (value: string | null) => () => onHighlightChange?.(value);

  return (
    <li
      data-testid="reference-source-row"
      data-reference-label={source.label}
      data-color-key={source.colorKey}
      data-highlighted={highlighted ? 'true' : 'false'}
      className="rounded-md px-1.5 py-1 transition-colors"
      style={{
        backgroundColor: highlighted ? alpha(source.stroke, 0.12) : undefined,
        boxShadow: highlighted ? `inset 0 0 0 1px ${alpha(source.stroke, 0.45)}` : undefined,
      }}
      onMouseEnter={report(source.nodeId)}
      onMouseLeave={report(null)}
    >
      <div className="flex items-center gap-1.5">
        <span
          aria-hidden="true"
          data-testid="reference-swatch"
          className="h-2 w-2 shrink-0 rounded-full"
          style={{ backgroundColor: source.stroke }}
        />
        <span className="shrink-0 font-mono text-[10px] font-semibold text-content-primary">
          {source.label}
        </span>
        {/*
          §7.3: the source list is a list of LINKS. `?node=` is the app's
          existing deep-link (TreeView focuses the node from the query
          param), so this works with keyboard, middle-click and no JS.
        */}
        <a
          data-testid="reference-source-link"
          href={`?node=${encodeURIComponent(source.nodeId)}`}
          onFocus={report(source.nodeId)}
          onBlur={report(null)}
          className="min-w-0 flex-1 truncate text-xs text-content-secondary underline-offset-2 hover:text-accent hover:underline focus-visible:text-accent focus-visible:underline"
          title={source.preview || source.nodeId}
        >
          {source.preview || shortNodeId(source.nodeId) || 'Untitled source'}
        </a>
        {source.truncated && (
          <span
            data-testid="reference-truncated"
            title={
              source.omittedTokens > 0
                ? `${source.omittedTokens} tokens omitted from ${source.label}`
                : `truncated in ${source.label}`
            }
          >
            <Scissors className="h-3 w-3 shrink-0 text-status-warning" aria-hidden="true" />
            <span className="sr-only">truncated</span>
          </span>
        )}
      </div>

      <div className="mt-0.5 flex items-center gap-2 pl-3.5 text-[10px] text-content-muted">
        <span data-testid="reference-source-author" className="truncate">
          {source.author}
        </span>
        {source.createdAt && (
          <time dateTime={source.createdAt}>{formatNodeTime(source.createdAt)}</time>
        )}
        {source.branchRootId && (
          <span
            data-testid="reference-branch-root"
            className="inline-flex items-center gap-0.5 truncate"
            title={`Branch root ${source.branchRootId}`}
          >
            <GitBranch className="h-2.5 w-2.5 shrink-0" aria-hidden="true" />
            {shortNodeId(source.branchRootId)}
          </span>
        )}
      </div>
    </li>
  );
}

// ─── List ──────────────────────────────────────────────────────────────

export default function ReferenceSourceList({
  sources,
  highlightedNodeId,
  onHighlightChange,
}: ReferenceSourceListProps) {
  if (sources.length === 0) {
    return (
      <p data-testid="reference-source-empty" className="text-[11px] text-content-muted">
        No sources recorded for this reply.
      </p>
    );
  }

  return (
    <ul
      data-testid="reference-source-list"
      aria-label={`Source messages: ${sources.map((s) => s.label).join(', ')}`}
      className="space-y-0.5"
      style={{ color: token.contentSecondary }}
    >
      {sources.map((source) => (
        <SourceRow
          key={source.nodeId}
          source={source}
          highlighted={highlightedNodeId === source.nodeId}
          {...(onHighlightChange ? { onHighlightChange } : {})}
        />
      ))}
    </ul>
  );
}
