/**
 * Hermes Canopy — Context Run Indicator (GAP-084)
 *
 * The AFTER side of the Context Compiler promise. `ContextManifestPanel`
 * shows what the compiler WOULD send for the selected node (the BEFORE
 * side, a preview compiled on click); this component shows what a run
 * that actually happened was given — the compiler manifest canopyd
 * attached to the run record, read back from the run registry.
 *
 *   ┌ ▸ Context window: 1,240 / 8,000 tokens   node 019fb0c2…e000 ┐
 *   │   Ancestry · 4                                              │
 *   │     node  Welcome to Hermes Canopy                  412 tok │
 *   └─────────────────────────────────────────────────────────────┘
 *
 * Rendering rules, and why they are what they are:
 *
 *   - No run, or a run with neither `context_tokens` nor `token_budget`
 *     → render NOTHING. Those four fields are `omitempty` on the Go
 *     record, so a context-free run genuinely carries no provenance; a
 *     badge reading "0 tokens" would invent a compile that never ran.
 *   - A run WITH provenance but no manifest (a degraded compile) still
 *     renders the window line and says so — an audit gap is reported,
 *     never hidden behind an empty list.
 *   - Collapsed by default, same disclosure idiom as
 *     `ContextManifestPanel`: the tree canvas is the page.
 */

import { useState } from 'react';
import { ChevronDown, ChevronRight, Scissors, Sparkles } from 'lucide-react';
import {
  formatTokenCount,
  formatTokenUsage,
  manifestItemTitle,
  normaliseManifest,
  omissionNote,
  type Manifest,
  type ManifestItem,
} from '../lib/contextManifest.ts';
import { runContextWindowLine, type GatewayRun } from '../lib/gatewayApi.ts';
import { shortNodeId } from '../lib/nodeShortId.ts';

// ─── Props ─────────────────────────────────────────────────────────────

export interface ContextRunIndicatorProps {
  /** The started run, once the registry has it; `null` while unknown. */
  run: GatewayRun | null;
  className?: string;
}

// ─── Item rows (same shape as the panel's, scoped to this file) ────────

function ItemRow({ item }: { item: ManifestItem }) {
  return (
    <li
      data-testid="context-run-item"
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
    <section className="mt-2" data-testid={`context-run-section-${label.toLowerCase()}`}>
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

// ─── Indicator ─────────────────────────────────────────────────────────

export default function ContextRunIndicator({
  run,
  className,
}: ContextRunIndicatorProps) {
  const [open, setOpen] = useState(false);

  if (!run) return null;

  const line = runContextWindowLine(run);
  // Context-free run: the omitempty fields were the whole point — there is
  // no provenance to show, and the pre-GAP-075 look must be preserved.
  if (line === null) return null;

  const manifest: Manifest | null = normaliseManifest(
    run.manifest ? { manifest: run.manifest } : null,
  );
  const omission = manifest ? omissionNote(manifest) : null;
  const source = run.source_node_id ? shortNodeId(run.source_node_id) : '';
  const warnings = manifest?.warnings ?? [];

  return (
    <aside
      data-testid="context-run-indicator"
      aria-label="Context provenance for the last run"
      className={`glass shrink-0 border-t border-line-subtle px-3 py-2 ${className ?? ''}`}
    >
      {/* Header — always visible, doubles as the disclosure control */}
      <div className="flex items-center gap-2">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          data-testid="context-run-toggle"
          className="flex min-w-0 flex-1 items-center gap-1.5 rounded-md text-left transition-colors hover:text-accent"
        >
          {open ? (
            <ChevronDown className="h-3.5 w-3.5 shrink-0 text-content-muted" />
          ) : (
            <ChevronRight className="h-3.5 w-3.5 shrink-0 text-content-muted" />
          )}
          <Sparkles
            className="h-3.5 w-3.5 shrink-0 text-accent-2"
            aria-hidden="true"
          />
          <span
            data-testid="context-run-window"
            className="font-mono text-[11px] tabular-nums text-content-secondary"
          >
            {line}
          </span>
        </button>

        <span
          data-testid="context-run-source"
          title={run.source_node_id ?? ''}
          className="shrink-0 font-mono text-[11px] text-content-muted"
        >
          {source || 'unknown node'}
        </span>
      </div>

      {/* Detail — what the model was actually given, and what was dropped */}
      {open && (
        <div data-testid="context-run-detail" className="mt-1.5">
          {manifest ? (
            <>
              <p
                data-testid="context-run-usage"
                className="font-mono text-[11px] tabular-nums text-content-secondary"
              >
                {formatTokenUsage(manifest.tokensUsed, manifest.tokenBudget)}
              </p>

              {omission && (
                <p
                  data-testid="context-run-omission"
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
                <ul className="mt-2 space-y-0.5" data-testid="context-run-warnings">
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
            </>
          ) : (
            /*
             * A degraded compile: the run recorded a context window but no
             * manifest reached the record. Say so — the audit gap is the
             * finding, and an empty list would read as "nothing was sent".
             */
            <p
              data-testid="context-run-manifest-missing"
              className="text-[11px] text-content-muted"
            >
              No compiler manifest was recorded for this run — the context it
              used cannot be audited from the run record.
            </p>
          )}
        </div>
      )}
    </aside>
  );
}
