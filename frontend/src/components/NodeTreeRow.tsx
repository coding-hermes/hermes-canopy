/**
 * Hermes Canopy — Node list row with hierarchy chrome (UI-08)
 *
 * Wraps the UI-05 `NodeCard` in the chrome the list needs and the card
 * deliberately does not carry: a selection checkbox, the indent guide
 * lines that show which node replied to which, and a clickable short id.
 *
 *   [✓]  │  ├─  019fb0c2…e000  ┃ (SC) Sarah Chen        2h ago  ··· ┃
 *                              ┃ Message · Depth 1                  ┃
 *                              ┃ Child #3: DAG node with real …     ┃
 *
 * The card itself is untouched — UI-05 owns its layout, and re-styling it
 * from here would fork the design. This module only adds the gutter.
 *
 * Accessibility notes:
 *   - guide lines are pure decoration and are `aria-hidden`; the real
 *     parent/child relationship is carried by `aria-level` on the row,
 *     which is what a screen reader announces.
 *   - the id is a real link (keyboard focusable, Enter activates, and it
 *     can be opened in a new tab) rather than a click handler on a span.
 *     Its accessible name carries the id in FULL — the visible label is
 *     elided, and a user who cannot see the ellipsis still needs to be
 *     able to identify the node.
 */

import { memo, useMemo } from 'react';
import { Link } from 'react-router-dom';
import { NodeCard } from './NodeCard.tsx';
import { rowRails, type HierarchyRow } from '../lib/nodeHierarchy.ts';
import { nodeIdLinkLabel } from '../lib/nodeShortId.ts';
import type { NodeMetaSource } from '../lib/nodeMeta.ts';

// ─── Types ─────────────────────────────────────────────────────────────

/** The node shape this row renders — the card's needs plus linkage. */
export interface TreeRowNode extends NodeMetaSource {
  treeId: string;
  parentId: string | null;
}

export interface NodeTreeRowProps {
  row: HierarchyRow<TreeRowNode>;
  /** Short, list-unique label for the node id (see lib/nodeShortId). */
  shortId: string;
  selected: boolean;
  onToggleSelect: (id: string) => void;
  /** False when the row is only present as an ancestor of a search hit. */
  isMatch: boolean;
  /** True while a search is active — enables the context dimming. */
  searching: boolean;
  authorNames?: ReadonlyMap<string, string>;
  topicTitles?: ReadonlyMap<string, string>;
  onEdit: () => void;
  onDelete: () => void;
  onOpenTopic?: (topicId: string | null, slug: string) => void;
}

// ─── Guide lines ───────────────────────────────────────────────────────

/** Width of one indent level, in px. Matches the rail column width. */
const RAIL_W = 18;

/** Distance from the row's top to the card's first text line, in px. */
const ELBOW_Y = 20;

/**
 * The indent rails for one row. Decorative: a screen reader gets depth
 * from `aria-level`, so painting box characters here would only add
 * noise to the accessible name.
 */
function Rails({ row }: { row: HierarchyRow<TreeRowNode> }) {
  const rails = useMemo(() => rowRails(row), [row]);
  if (row.depth === 0) return null;

  return (
    <span
      aria-hidden="true"
      data-testid="node-row-rails"
      className="flex shrink-0 self-stretch"
    >
      {rails.ancestors.map((segment, i) => (
        <span
          key={i}
          className="relative self-stretch"
          style={{ width: RAIL_W }}
        >
          {segment === 'line' && (
            <span className="absolute inset-y-0 left-1/2 w-px bg-line" />
          )}
        </span>
      ))}

      {rails.elbow && (
        <span className="relative self-stretch" style={{ width: RAIL_W }}>
          {/* Vertical: full height on a `tee`, stops at the elbow on `end`. */}
          <span
            className="absolute left-1/2 top-0 w-px bg-line"
            style={
              rails.elbow === 'end'
                ? { height: ELBOW_Y }
                : { bottom: 0, height: 'auto' }
            }
          />
          {/* Horizontal stub reaching toward the card. */}
          <span
            className="absolute h-px bg-line"
            style={{ left: '50%', right: 0, top: ELBOW_Y }}
          />
        </span>
      )}
    </span>
  );
}

// ─── Row ───────────────────────────────────────────────────────────────

function NodeTreeRowComponent({
  row,
  shortId,
  selected,
  onToggleSelect,
  isMatch,
  searching,
  authorNames,
  topicTitles,
  onEdit,
  onDelete,
  onOpenTopic,
}: NodeTreeRowProps) {
  const { node, depth } = row;

  /*
   * Detail target. There is no `/nodes/:id` route — TreeView IS the node
   * detail context in this product (it is where a node's parents, replies
   * and content live), so the link deep-links into the node's own tree
   * and focuses it via `?node=`. TreeView consumes that param read-only.
   */
  const detailTo = `/tree/${node.treeId}?node=${encodeURIComponent(node.id)}`;

  return (
    <li
      aria-level={depth + 1}
      data-testid="node-tree-row"
      data-depth={depth}
      data-selected={selected || undefined}
      className={[
        'flex items-stretch gap-1.5',
        // Ancestors pulled in purely as context for a search hit are
        // dimmed — they are the path to the answer, not the answer.
        searching && !isMatch ? 'opacity-55' : '',
      ].join(' ')}
    >
      {/* Selection */}
      <span className="flex shrink-0 items-start" style={{ paddingTop: 14 }}>
        <input
          type="checkbox"
          checked={selected}
          onChange={() => onToggleSelect(node.id)}
          aria-label={`Select node ${node.id}`}
          data-testid="node-row-checkbox"
          className="h-3.5 w-3.5 cursor-pointer accent-[var(--color-accent-2)]"
        />
      </span>

      <Rails row={row} />

      {/* Id link — the gutter identifier, always distinguishing */}
      <span className="flex shrink-0 items-start" style={{ paddingTop: 12 }}>
        <Link
          to={detailTo}
          aria-label={nodeIdLinkLabel(node.id)}
          title={node.id}
          data-testid="node-id-link"
          className={[
            'rounded px-1.5 py-0.5 font-mono text-[11px] tabular-nums',
            'text-content-muted transition-colors',
            'hover:bg-surface-hover hover:text-accent',
            'active:text-accent-500',
            // The global `*:focus-visible` ring in index.css resolves its
            // outline-color to `currentColor` on anchors (app-wide — the
            // sidebar nav links focus grey for the same reason), so the
            // ring only picks up the accent if the text does too.
            'focus-visible:text-accent',
          ].join(' ')}
        >
          {shortId}
        </Link>
      </span>

      {/* The UI-05 card, unchanged */}
      <div className="min-w-0 flex-1">
        <NodeCard
          node={node}
          authorNames={authorNames}
          topicTitles={topicTitles}
          onEdit={onEdit}
          onDelete={onDelete}
          onOpenTopic={onOpenTopic}
        />
      </div>
    </li>
  );
}

export const NodeTreeRow = memo(NodeTreeRowComponent);
export default NodeTreeRow;
