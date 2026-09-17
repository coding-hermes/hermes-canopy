/**
 * Hermes Canopy — Context Manifest Panel (WIRE-002, GAP-080 phase 2b)
 *
 * Surfaces the Context Compiler's manifest for the selected node: what
 * the model would actually be sent, how much of the token budget it
 * costs, and — the point of the artifact — what got left out and why.
 *
 *   ┌ Context ────────────────── 1,240 / 8,000 tokens ─┐
 *   │ ███████████░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  16%   │
 *   │ [Model ▾] [Budget ▬▬▬●▬] 8,000  Auto             │
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
 * GAP-080 phase 2b — the two knobs. This panel used to pin `budget=8000`
 * on every request, which made the server's window-derived default
 * unreachable from the product; it also could not name a model, so the
 * window the budget derives from was unchoosable. Now:
 *
 *   Model      offered from `GET /api/v1/gateway/models` (fetched once per
 *              mount). "Server default" = no `model` parameter. ANY failure
 *              of that fetch degrades to the single Server-default option:
 *              never an error state, never a spinner that stays forever.
 *   Budget     Auto by default — NO `budget` parameter, so the server
 *              derives its own default. Moving the slider sends an explicit
 *              budget; the Auto button returns to the derived default. The
 *              slider's maximum is the selected model's `desired_budget`
 *              (what the server would derive for it) or the backend's
 *              explicit-request ceiling when no model is chosen.
 *
 * HONESTY: the effective budget is always `manifest.tokenBudget`. When an
 * explicit request came back lower than what was asked for, the panel says
 * so, naming both numbers — a control that shows the requested value as if
 * it were in force would be lying about what the model was sent.
 *
 * All colour comes from the design tokens (theme.ts / index.css); the
 * only inline styles are the ones a Tailwind utility cannot express (the
 * meter's computed width, alpha-composed fills).
 */

import { useEffect, useState } from 'react';
import { ChevronDown, ChevronRight, Scissors, AlertTriangle } from 'lucide-react';
import { useContextManifest } from '../hooks/useContextManifest.ts';
import { apiGet } from '../lib/api.ts';
import {
  CONTEXT_BUDGET_STEP,
  DEFAULT_CONTEXT_BUDGET,
  MIN_CONTEXT_BUDGET,
  budgetCappedNote,
  budgetSeverity,
  budgetUsageRatio,
  contextBudgetCeiling,
  contextErrorNote,
  formatTokenCount,
  formatTokenUsage,
  manifestItemTitle,
  normaliseBudget,
  normaliseModelOptions,
  omissionNote,
  type ContextModelOption,
  type Manifest,
  type ManifestItem,
} from '../lib/contextManifest.ts';
import { countLabel } from '../lib/pluralize.ts';
import { token } from '../theme.ts';

// ─── Props ─────────────────────────────────────────────────────────────

export interface ContextManifestPanelProps {
  /** Currently selected node, or `null` when the canvas has no selection. */
  nodeId: string | null;
  /**
   * Token budget requested from the compiler. When OMITTED the panel sends
   * no budget parameter at all and the server derives its own default from
   * the selected model's context window (GAP-080). The prop is the initial
   * value: the slider takes over from it once the user moves it.
   */
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

// ─── Budget + model controls (GAP-080 phase 2b) ────────────────────────

const MODEL_SELECT_ID = 'context-model-select';
const BUDGET_SLIDER_ID = 'context-budget-slider';

interface BudgetControlsProps {
  /** Models the gateway reports, or `[]` when the catalog is unavailable. */
  options: ContextModelOption[];
  model: string;
  onModelChange: (model: string) => void;
  /** `null` = Auto: no `budget` parameter is sent. */
  requested: number | null;
  onRequestedChange: (budget: number | null) => void;
  /** The effective budget the last manifest reported, when there was one. */
  effective: number | null;
}

/**
 * The two request knobs, always visible — a control the user cannot reach
 * while the detail is collapsed is not a control.
 */
function BudgetControls({
  options,
  model,
  onModelChange,
  requested,
  onRequestedChange,
  effective,
}: BudgetControlsProps) {
  const selected = options.find((option) => option.id === model);
  const ceiling = contextBudgetCeiling(selected);

  /*
   * In Auto the slider FOLLOWS the effective budget the server granted (so
   * the control shows what is in force rather than a stale guess); once the
   * user moves it, it shows the value they asked for, and the capped note
   * below reconciles the two when the server disagreed.
   */
  const raw = requested ?? effective ?? DEFAULT_CONTEXT_BUDGET;
  const value = Math.min(Math.max(raw, MIN_CONTEXT_BUDGET), ceiling);

  return (
    <div className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1">
      <label
        htmlFor={MODEL_SELECT_ID}
        className="text-[11px] text-content-muted"
      >
        Model
      </label>
      <select
        id={MODEL_SELECT_ID}
        data-testid="context-model-select"
        aria-label="Model whose context window sizes the budget"
        className="max-w-48 min-w-0 rounded-md border border-line-subtle bg-surface-input px-1.5 py-0.5 text-[11px] text-content-secondary"
        value={model}
        onChange={(event) => onModelChange(event.target.value)}
      >
        {/* Empty value = no `model` parameter: the server's own default. */}
        <option value="">Server default</option>
        {options.map((option) => (
          <option key={option.id} value={option.id}>
            {option.id}
          </option>
        ))}
      </select>

      <label
        htmlFor={BUDGET_SLIDER_ID}
        className="text-[11px] text-content-muted"
      >
        Budget
      </label>
      <input
        id={BUDGET_SLIDER_ID}
        data-testid="context-budget-slider"
        type="range"
        min={MIN_CONTEXT_BUDGET}
        max={ceiling}
        step={CONTEXT_BUDGET_STEP}
        value={value}
        aria-label="Context token budget"
        aria-valuemin={MIN_CONTEXT_BUDGET}
        aria-valuemax={ceiling}
        aria-valuenow={value}
        className="h-1 w-24 rounded-full bg-surface-input"
        onChange={(event) => onRequestedChange(Number(event.target.value))}
      />
      <span
        data-testid="context-budget-value"
        className="font-mono text-[11px] tabular-nums text-content-secondary"
      >
        {formatTokenCount(value)}
      </span>

      {requested === null ? (
        <span
          data-testid="context-budget-mode"
          className="text-[11px] text-content-muted"
        >
          Auto
        </span>
      ) : (
        <>
          <span
            data-testid="context-budget-mode"
            className="text-[11px] text-content-muted"
          >
            Custom
          </span>
          <button
            type="button"
            data-testid="context-budget-auto"
            onClick={() => onRequestedChange(null)}
            className="rounded-md px-1.5 py-0.5 text-[11px] text-content-muted transition-colors hover:text-accent"
          >
            Auto
          </button>
        </>
      )}
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
  const [model, setModel] = useState('');
  const [options, setOptions] = useState<ContextModelOption[]>([]);
  /*
   * `null` means AUTO — no `budget` parameter is sent and the server applies
   * its own default. The prop seeds the initial choice for callers that do
   * request a specific budget; `normaliseBudget` turns an unusable value
   * (0, NaN) into Auto rather than into a `?budget=0` the handler 400s.
   */
  const [requested, setRequested] = useState<number | null>(() =>
    normaliseBudget(budget),
  );

  /*
   * The model catalog, once per mount. Every failure is the same failure:
   * an empty list, which renders as the single "Server default" option —
   * the panel can always compile context, it just cannot offer a choice.
   */
  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const body = await apiGet<unknown>('/gateway/models');
        if (!cancelled) setOptions(normaliseModelOptions(body));
      } catch {
        if (!cancelled) setOptions([]);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const { manifest, loading, error } = useContextManifest(
    nodeId,
    requested,
    model,
  );

  // No selection — the inspector has nothing to inspect.
  if (!nodeId) return null;

  const omission = manifest ? omissionNote(manifest) : null;
  const warnings = manifest?.warnings ?? [];
  const capped = manifest ? budgetCappedNote(requested, manifest.tokenBudget) : null;

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

      {/* The request knobs — model + budget (GAP-080 phase 2b) */}
      <BudgetControls
        options={options}
        model={model}
        onModelChange={setModel}
        requested={requested}
        onRequestedChange={setRequested}
        effective={manifest?.tokenBudget ?? null}
      />

      {/*
       * An explicit request the server did not grant. Both numbers are
       * named: the effective budget shown everywhere else is the server's,
       * and silently displaying the requested value as if it were in force
       * would misreport what the model was sent.
       */}
      {capped && (
        <p
          data-testid="context-budget-capped"
          className="mt-1 text-[11px] text-status-warning"
        >
          {capped}
        </p>
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
