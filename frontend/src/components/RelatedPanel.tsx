/**
 * Hermes Canopy — Related Panel (UI-REL-001)
 *
 * Surfaces the session-lineage associations the backend attaches to a
 * tree imported from a Hermes session (WIRE-006): the parent session,
 * child sessions, the board task / project / commit the session belonged
 * to, and the delegation goals it carried.
 *
 *   ┌ Related ────────────────────────────────────────────────┐
 *   │ Parent session   Import session 20260606_155331_5054b7f3 │
 *   │ Children · 1     Child 1: Architecture                  │
 *   │ [WIRE-006] [hermes-canopy] [a1b2c3d]                    │
 *   │ Delegation goals · 2                                    │
 *   │   dlg-1  Build the related panel                        │
 *   └─────────────────────────────────────────────────────────┘
 *
 * Collapsible, open by default when the tree has associations (the panel
 * IS the point of this section), compact "No associations" empty state
 * otherwise. Renders nothing at all with no tree selected.
 *
 * Drill-down: a parent/child session click navigates the page to that
 * tree through the same selection mechanism as the tree dropdown; a chip
 * click copies its value to the clipboard with a transient "copied"
 * state — no dead buttons.
 *
 * All colour comes from the design tokens (theme.ts / index.css); the
 * only inline styles are the ones a Tailwind utility cannot express.
 */

import { useState, type ReactNode } from 'react';
import {
  Check,
  ChevronDown,
  ChevronRight,
  Copy,
  FolderKanban,
  GitBranch,
  GitCommit,
  Hash,
  Inbox,
} from 'lucide-react';
import { useTreeRelated } from '../hooks/useTreeRelated.ts';
import type { DelegationRef, RelatedRef } from '../types/tree.ts';

// ─── Props ─────────────────────────────────────────────────────────────

export interface RelatedPanelProps {
  /** Currently selected tree, or `null` when nothing is selected. */
  treeId: string | null;
  /**
   * Navigate the page to another tree (drill-down). The panel passes the
   * related tree's id and title so the caller can surface it even when it
   * is not in the page's tree list.
   */
  onNavigateToTree: (treeId: string, title?: string) => void;
}

// ─── Copy helper ───────────────────────────────────────────────────────

/**
 * Copy a value to the clipboard. Prefers the async Clipboard API
 * (available on localhost — a secure context) and falls back to the
 * legacy textarea + execCommand path. Returns whether the copy landed so
 * the caller can decide whether to show the "copied" state.
 */
async function copyText(value: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(value);
      return true;
    }
  } catch {
    // fall through to the legacy path
  }
  try {
    const ta = document.createElement('textarea');
    ta.value = value;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand('copy');
    ta.remove();
    return ok;
  } catch {
    return false;
  }
}

// ─── Copy chip ──────────────────────────────────────────────────────────

function CopyChip({
  label,
  value,
  icon,
  testId,
}: {
  label: string;
  value: string;
  icon: ReactNode;
  testId: string;
}) {
  const [copied, setCopied] = useState(false);

  const handleClick = async () => {
    const ok = await copyText(value);
    if (!ok) return;
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1500);
  };

  return (
    <button
      type="button"
      onClick={handleClick}
      data-testid={testId}
      aria-label={copied ? `${label} copied` : `Copy ${label}`}
      title={copied ? 'Copied' : `Copy ${label}`}
      className="inline-flex max-w-full items-center gap-1.5 rounded-md bg-surface-input px-2 py-1 font-mono text-[11px] text-content-secondary ring-1 ring-inset ring-line-subtle transition-colors hover:bg-surface-hover hover:text-accent"
    >
      {icon}
      <span className="max-w-40 truncate">{value}</span>
      {copied ? (
        <Check
          className="h-3 w-3 shrink-0 text-status-success"
          aria-hidden="true"
        />
      ) : (
        <Copy className="h-3 w-3 shrink-0 text-content-faint" aria-hidden="true" />
      )}
    </button>
  );
}

// ─── Session link ──────────────────────────────────────────────────────

function SessionLink({
  session,
  kind,
  onNavigate,
}: {
  session: RelatedRef;
  kind: 'parent' | 'child';
  onNavigate: (treeId: string, title?: string) => void;
}) {
  return (
    <li>
      <button
        type="button"
        onClick={() => onNavigate(session.id, session.title)}
        data-testid={`related-${kind}-session`}
        aria-label={`Open ${kind} session ${session.title}`}
        className="flex min-w-0 w-full items-center gap-2 rounded-md px-1.5 py-1 text-left text-xs text-content-secondary transition-colors hover:bg-surface-hover hover:text-accent"
      >
        <GitBranch
          className="h-3 w-3 shrink-0 text-content-faint"
          aria-hidden="true"
        />
        <span className="min-w-0 flex-1 truncate" title={session.title}>
          {session.title}
        </span>
        <span className="shrink-0 font-mono text-[10px] text-content-faint">
          {session.id.slice(0, 8)}
        </span>
      </button>
    </li>
  );
}

// ─── Delegation goal row ────────────────────────────────────────────────

function GoalRow({ goal }: { goal: DelegationRef }) {
  return (
    <li
      data-testid="related-goal"
      className="flex items-baseline gap-2 py-1"
    >
      <span className="shrink-0 rounded bg-surface-input px-1.5 py-0.5 font-mono text-[10px] text-content-muted">
        {goal.delegation_id}
      </span>
      <span
        className="min-w-0 flex-1 truncate text-xs text-content-secondary"
        title={goal.goal}
      >
        {goal.goal}
      </span>
    </li>
  );
}

// ─── Panel ─────────────────────────────────────────────────────────────

export default function RelatedPanel({
  treeId,
  onNavigateToTree,
}: RelatedPanelProps) {
  const [open, setOpen] = useState(true);
  const { related, loading, error } = useTreeRelated(treeId);

  // No selection — the panel has nothing to inspect.
  if (!treeId) return null;

  const associationCount =
    (related?.parent ? 1 : 0) +
    (related?.children?.length ?? 0) +
    (related?.board_task ? 1 : 0) +
    (related?.project ? 1 : 0) +
    (related?.commit_hash ? 1 : 0) +
    (related?.delegation_goals?.length ?? 0);

  const hasAssociations = related !== null && associationCount > 0;

  return (
    <aside
      data-testid="related-panel"
      aria-label="Related sessions"
      className="glass rounded-xl px-3 py-2"
    >
      {/* Header — always visible, doubles as the disclosure control */}
      <div className="flex items-center gap-2">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          data-testid="related-panel-toggle"
          className="flex min-w-0 flex-1 items-center gap-1.5 rounded-md text-left transition-colors hover:text-accent"
        >
          {open ? (
            <ChevronDown className="h-3.5 w-3.5 shrink-0 text-content-muted" />
          ) : (
            <ChevronRight className="h-3.5 w-3.5 shrink-0 text-content-muted" />
          )}
          <span className="text-xs font-medium text-content-primary">
            Related
          </span>
          {related && (
            <span
              data-testid="related-association-count"
              className="font-mono text-[11px] tabular-nums text-content-secondary"
            >
              {associationCount}
            </span>
          )}
          {loading && (
            <span className="text-[11px] text-content-muted animate-pulse">
              loading…
            </span>
          )}
        </button>
      </div>

      {/* Detail */}
      {open && (
        <div data-testid="related-panel-detail" className="mt-1.5">
          {/*
           * Failure is a note, never a banner. A tree the backend cannot
           * resolve must not read as a broken page — the node list above
           * is still perfectly valid.
           */}
          {error && !loading && (
            <p
              data-testid="related-panel-error"
              className="text-[11px] text-content-muted"
            >
              {error}
            </p>
          )}

          {/*
           * Graceful empty state: an ordinary tree (no `related` key) or
           * an imported tree whose associations all failed to resolve.
           */}
          {!loading && !error && !hasAssociations && (
            <p
              data-testid="related-empty"
              className="flex items-center gap-1.5 text-[11px] text-content-muted"
            >
              <Inbox className="h-3 w-3 shrink-0" aria-hidden="true" />
              No associations
            </p>
          )}

          {!loading && !error && hasAssociations && related && (
            <div className="space-y-2">
              {related.parent && (
                <section data-testid="related-parent-section">
                  <h4 className="text-[11px] font-medium uppercase tracking-wide text-content-muted">
                    Parent session
                  </h4>
                  <ul className="mt-0.5">
                    <SessionLink
                      session={related.parent}
                      kind="parent"
                      onNavigate={onNavigateToTree}
                    />
                  </ul>
                </section>
              )}

              {related.children && related.children.length > 0 && (
                <section data-testid="related-children-section">
                  <h4 className="text-[11px] font-medium uppercase tracking-wide text-content-muted">
                    Children · {related.children.length}
                  </h4>
                  <ul className="mt-0.5">
                    {related.children.map((child) => (
                      <SessionLink
                        key={child.id}
                        session={child}
                        kind="child"
                        onNavigate={onNavigateToTree}
                      />
                    ))}
                  </ul>
                </section>
              )}

              {(related.board_task ||
                related.project ||
                related.commit_hash) && (
                <section data-testid="related-chips-section">
                  <h4 className="text-[11px] font-medium uppercase tracking-wide text-content-muted">
                    Task
                  </h4>
                  <div className="mt-1 flex flex-wrap gap-1.5">
                    {related.board_task && (
                      <CopyChip
                        label="board task"
                        value={related.board_task}
                        testId="related-chip-board-task"
                        icon={
                          <Hash
                            className="h-3 w-3 text-purple-400"
                            aria-hidden="true"
                          />
                        }
                      />
                    )}
                    {related.project && (
                      <CopyChip
                        label="project"
                        value={related.project}
                        testId="related-chip-project"
                        icon={
                          <FolderKanban
                            className="h-3 w-3 text-blue-400"
                            aria-hidden="true"
                          />
                        }
                      />
                    )}
                    {related.commit_hash && (
                      <CopyChip
                        label="commit hash"
                        value={related.commit_hash}
                        testId="related-chip-commit"
                        icon={
                          <GitCommit
                            className="h-3 w-3 text-amber-400"
                            aria-hidden="true"
                          />
                        }
                      />
                    )}
                  </div>
                </section>
              )}

              {related.delegation_goals &&
                related.delegation_goals.length > 0 && (
                  <section data-testid="related-goals-section">
                    <h4 className="text-[11px] font-medium uppercase tracking-wide text-content-muted">
                      Delegation goals · {related.delegation_goals.length}
                    </h4>
                    <ul className="mt-0.5">
                      {related.delegation_goals.map((goal) => (
                        <GoalRow key={goal.delegation_id} goal={goal} />
                      ))}
                    </ul>
                  </section>
                )}
            </div>
          )}
        </div>
      )}
    </aside>
  );
}
