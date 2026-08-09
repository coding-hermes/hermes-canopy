/**
 * Hermes Canopy — Context Manifest Panel (WIRE-002)
 *
 * Surfaces the Context Compiler's manifest for the selected node: what
 * the model would actually be sent, how much of the token budget it
 * costs, and — the point of the artifact — what got left out and why.
 *
 *   ┌ Context ────────────────── 1,240 / 8,000 tokens ─┐
 *   │ ███████████░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  16%   │
 *   │ 3 items omitted (budget)                         │
 *   │ Ancestry · 4                                     │
 *   │   node   Welcome to Hermes Canopy        412 tok │
 *   │   node   Child 1: Architecture      312 tok ✂    │
 *   └──────────────────────────────────────────────────┘
 *
 * Collapsible and collapsed by default — the canvas is the page, and an
 * inspector that steals a third of it on every click is a worse default
 * than one click to open. Renders nothing at all with no selection.
 *
 * All colour comes from the design tokens (theme.ts / index.css); the
 * only inline styles are the ones a Tailwind utility cannot express (the
 * meter's computed width, alpha-composed fills).
 */

import { useState } from 'react';
import { ChevronDown, ChevronRight, Scissors, AlertTriangle } from 'lucide-react';
import { useContextManifest } from '../hooks/useContextManifest.ts';
import {
  DEFAULT_CONTEXT_BUDGET,
  budgetSeverity,
  budgetUsageRatio,
  contextErrorNote,
  formatTokenCount,
  formatTokenUsage,
  manifestItemTitle,
  omissionNote,
  type Manifest,
  type ManifestItem,
} from '../lib/contextManifest.ts';
import { countLabel } from '../lib/pluralize.ts';
import { token } from '../theme.ts';

// ─── Props ─────────────────────────────────────────────────────────────

export interface ContextManifestPanelProps {
  /** Currently selected node, or `null` when the canvas has no selection. */
  nodeId: string | null;
  /** Token budget requested from the compiler. */
  budget?: number;
}

// ─── Meter ─────────────────────────────────────────────────────────────

const SEVERITY_COLOR = {
  ok: token.accent,
  warn: token.warning,
  over: token.danger,
} as const;

function BudgetMeter({ manifest }: { manifest: Manifest }) {
  const ratio = budgetUsageRatio(manifest.tokensUsed, manifest.tokenBudget);
  const severity = budgetSeverity(manifest.tokensUsed, manifest.tokenBudget);
  const percent = Math.round(ratio * 100);

  return (
    <div
      role="meter"
      aria-valuenow={manifest.tokensUsed}
      aria-valuemin={0}
      aria-valuemax={manifest.tokenBudget}
      aria-label={`Context token usage: ${formatTokenUsage(
        manifest.tokensUsed,
        manifest.tokenBudget,
      )}`}
      data-testid="context-budget-meter"
      data-severity={severity}
      className="h-1.5 w-full overflow-hidden rounded-full bg-surface-input"
    >
      <div
        className="h-full rounded-full transition-[width] duration-300"
        style={{
          width: `${percent}%`,
          backgroundColor: SEVERITY_COLOR[severity],
        }}
      />
    </div>
  );
}

// ─── Item row ──────────────────────────────────────────────────────────

function ItemRow({ item }: { item: ManifestItem }) {
  return (
    <li
      data-testid="context-manifest-item"
      data-kind={item.kind}
      className="flex items-baseline gap-2 py-1"
    >
      <span className="shrink-0 rounded bg-surface-input px-1.5 py-0.5 font-mono text-[10px] uppercase tracking-wide text-content-muted">
        {item.kind}
      </span>
      <span
        className="min-w-0 flex-1 truncate text-xs text-content-secondary"
        title={item.title || item.id}
      >
        {manifestItemTitle(item)}
      </span>
      {item.truncated && (
        <Scissors
          className="h-3 w-3 shrink-0 text-status-warning"
          aria-label="truncated"
        />
      )}
      <span className="shrink-0 font-mono text-[11px] tabular-nums text-content-muted">
        {formatTokenCount(item.tokenCount)}
      </span>
    </li>
  );
}

function ItemSection({
  label,
  items,
}: {
  label: string;
  items: ManifestItem[];
}) {
  if (items.length === 0) return null;

  return (
    <section className="mt-2" data-testid={`context-section-${label.toLowerCase()}`}>
      <h4 className="text-[11px] font-medium uppercase tracking-wide text-content-muted">
        {label} · {items.length}
      </h4>
      <ul className="mt-0.5">
        {items.map((item, i) => (
          <ItemRow key={`${item.id}-${i}`} item={item} />
        ))}
      </ul>
    </section>
  );
}

// ─── Panel ─────────────────────────────────────────────────────────────

export default function ContextManifestPanel({
  nodeId,
  budget,
}: ContextManifestPanelProps) {
  const [open, setOpen] = useState(false);
  const { manifest, loading, error } = useContextManifest(
    nodeId,
    budget ?? DEFAULT_CONTEXT_BUDGET,
  );

  // No selection — the inspector has nothing to inspect.
  if (!nodeId) return null;

  const omission = manifest ? omissionNote(manifest) : null;
  const warnings = manifest?.warnings ?? [];

  return (
    <aside
      data-testid="context-manifest-panel"
      aria-label="Context manifest"
      className="glass shrink-0 border-t border-line-subtle px-3 py-2"
    >
      {/* Header — always visible, doubles as the disclosure control */}
      <div className="flex items-center gap-2">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          data-testid="context-manifest-toggle"
          className="flex min-w-0 flex-1 items-center gap-1.5 rounded-md text-left transition-colors hover:text-accent"
        >
          {open ? (
            <ChevronDown className="h-3.5 w-3.5 shrink-0 text-content-muted" />
          ) : (
            <ChevronRight className="h-3.5 w-3.5 shrink-0 text-content-muted" />
          )}
          <span className="text-xs font-medium text-content-primary">
            Context
          </span>
          {manifest && (
            <span
              data-testid="context-token-usage"
              className="font-mono text-[11px] tabular-nums text-content-secondary"
            >
              {formatTokenUsage(manifest.tokensUsed, manifest.tokenBudget)}
            </span>
          )}
          {loading && (
            <span className="text-[11px] text-content-muted animate-pulse">
              compiling…
            </span>
          )}
        </button>

        {manifest && warnings.length > 0 && (
          <span
            data-testid="context-warning-count"
            title={warnings.join('\n')}
            className="flex shrink-0 items-center gap-1 text-[11px] text-status-warning"
          >
            <AlertTriangle className="h-3 w-3" aria-hidden="true" />
            {countLabel(warnings.length, 'warning')}
          </span>
        )}
      </div>

      {/* Meter — visible collapsed too; it is the one number that matters */}
      {manifest && (
        <div className="mt-1.5">
          <BudgetMeter manifest={manifest} />
        </div>
      )}

      {/*
       * Failure is a note, never a banner. A node the compiler cannot
       * resolve (local-only replica, DB blip) must not read as a broken
       * tree — the canvas above is still perfectly valid.
       */}
      {error && !loading && (
        <p
          data-testid="context-manifest-error"
          className="mt-1 text-[11px] text-content-muted"
        >
          {contextErrorNote(error)}
        </p>
      )}

      {/* Detail */}
      {open && manifest && (
        <div data-testid="context-manifest-detail" className="mt-1.5">
          {omission && (
            <p
              data-testid="context-omission-note"
              className="text-[11px] text-status-warning"
            >
              {omission}
            </p>
          )}

          {manifest.truncationMarkers.map((marker, i) => (
            <p key={i} className="text-[11px] text-content-muted">
              {marker}
            </p>
          ))}

          <ItemSection label="Ancestry" items={manifest.ancestry} />
          <ItemSection label="References" items={manifest.references} />
          <ItemSection label="Cards" items={manifest.cards} />

          {warnings.length > 0 && (
            <ul className="mt-2 space-y-0.5" data-testid="context-warnings">
              {warnings.map((warning, i) => (
                <li key={i} className="text-[11px] text-status-warning">
                  {warning}
                </li>
              ))}
            </ul>
          )}

          {manifest.ancestry.length === 0 &&
            manifest.references.length === 0 &&
            manifest.cards.length === 0 && (
              <p className="mt-1 text-[11px] text-content-muted">
                Nothing compiled into this context.
              </p>
            )}
        </div>
      )}
    </aside>
  );
}
