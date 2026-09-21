/**
 * Hermes Canopy — Context Audit Dialog (GAP-096)
 *
 * The audit-before-send moment for UI-initiated gateway runs (GAP-080
 * phase 5b). Before a run may POST /gateway/runs against a selected
 * node's compiled context, this dialog shows the manifest the compiler
 * just built for that node — token budget, tokens used, included and
 * omitted accounting, the manifest digest, and the top included sources
 * — and requires an explicit choice:
 *
 *   Send    → proceed with `token_budget` = the budget the previewed
 *             manifest was actually computed with (the effective number,
 *             so a server-capped request is sent as capped — never as
 *             the larger number that was asked for).
 *   Adjust  → reveal a budget input; a validated positive integer
 *             re-previews with `?budget=N` and keeps the dialog open.
 *             0 / negative / NaN are rejected client-side with an inline
 *             message and no request.
 *   Cancel  → close; no request of any kind.
 *
 * The dialog NEVER auto-sends: while the preview request is in flight a
 * loading state is shown, and on preview failure the error is offered
 * with Cancel / Retry only.
 *
 * Appearance mirrors ShareDialog (backdrop + raised card, theme tokens
 * only); the only inline styles are the ones Tailwind cannot express.
 */

import {
  useCallback,
  useEffect,
  useState,
  type CSSProperties,
  type ReactNode,
} from 'react';
import { Shield, Loader2, AlertTriangle, X } from 'lucide-react';
import { token, alpha, palette } from '../theme.ts';
import {
  budgetCappedNote,
  formatTokenCount,
  formatTokenUsage,
  manifestHashShort,
} from '../lib/contextManifest.ts';
import { shortNodeId } from '../lib/nodeShortId.ts';
import {
  auditIncludedCount,
  topAuditSources,
  type CompiledContextResponse,
  type ContextAuditSource,
} from '../lib/contextApi.ts';
import { countLabel } from '../lib/pluralize.ts';

// ─── Props ─────────────────────────────────────────────────────────────

export interface ContextAuditDialogProps {
  /** The node whose compiled context is being audited. */
  nodeId: string;
  /** The message the run would carry — shown for context, never sent here. */
  message: string;
  /**
   * Fetch one preview. `budget === null` sends NO `budget` parameter
   * (server default); a positive number is sent as `?budget=N`.
   * Supplied by the caller so tests (and future surfaces) can substitute
   * the transport without mocking globals.
   */
  getPreview: (budget: number | null) => Promise<CompiledContextResponse>;
  /** Send: proceeds with the budget the previewed manifest was computed with. */
  onConfirm: (tokenBudget: number | undefined) => void;
  /** Cancel / dismiss: the caller must make NO request. */
  onClose: () => void;
}

// ─── Budget input validation (client-side, exact) ──────────────────────

/**
 * A budget the handler will accept, or `null` when the input must be
 * rejected inline. Digits only: rejects empty, NaN, negatives, decimals
 * and other garbage the server would 400.
 */
function parseBudgetInput(raw: string): number | null {
  const trimmed = raw.trim();
  if (!/^\d+$/.test(trimmed)) return null;
  const value = Number(trimmed);
  if (!Number.isSafeInteger(value) || value < 1) return null;
  return value;
}

/** The budget Send proceeds with, from the previewed manifest. */
function effectiveBudget(
  manifest: { tokenBudget?: number | null },
  requested: number | null,
): number | undefined {
  const t = manifest.tokenBudget;
  if (typeof t === 'number' && Number.isFinite(t) && t > 0) return t;
  // Degraded manifest without a recorded budget: fall back to what the
  // user adjusted to, else let the server default apply (absent param).
  if (requested !== null && Number.isSafeInteger(requested) && requested > 0) {
    return requested;
  }
  return undefined;
}

// ─── Sub-pieces ────────────────────────────────────────────────────────

function AuditRow({
  label,
  testid,
  children,
}: {
  label: string;
  testid?: string;
  children: ReactNode;
}) {
  return (
    <div className="flex items-baseline justify-between gap-3 text-sm">
      <span className="text-content-muted" style={{ color: token.contentMuted }}>
        {label}
      </span>
      <span
        data-testid={testid}
        className="font-medium text-right"
        style={{ color: token.contentPrimary }}
      >
        {children}
      </span>
    </div>
  );
}

// ─── Component ─────────────────────────────────────────────────────────

type AuditStatus = 'loading' | 'ready' | 'error';

export default function ContextAuditDialog({
  nodeId,
  message,
  getPreview,
  onConfirm,
  onClose,
}: ContextAuditDialogProps) {
  /** The budget the current preview asked with — `null` = server default. */
  const [budget, setBudget] = useState<number | null>(null);
  /** Bumping re-runs the preview with the SAME budget (Retry). */
  const [reloadSeq, setReloadSeq] = useState(0);
  const [status, setStatus] = useState<AuditStatus>('loading');
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [manifest, setManifest] =
    useState<CompiledContextResponse['manifest']>(null);

  // Adjust state
  const [adjustOpen, setAdjustOpen] = useState(false);
  const [budgetInput, setBudgetInput] = useState('');
  const [inputError, setInputError] = useState<string | null>(null);

  // ── Preview fetch — mount, Retry, and Adjust all land here ──────────
  useEffect(() => {
    let cancelled = false;
    setStatus('loading');
    setPreviewError(null);
    getPreview(budget)
      .then((res) => {
        if (cancelled) return;
        setManifest(res.manifest ?? null);
        setStatus('ready');
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setPreviewError(err instanceof Error ? err.message : String(err));
        setStatus('error');
      });
    return () => {
      cancelled = true;
    };
  }, [getPreview, budget, reloadSeq]);

  // ── Escape closes (Cancel semantics — no request) ───────────────────
  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [onClose]);

  const handleRetry = useCallback(() => {
    setReloadSeq((s) => s + 1);
  }, []);

  const handleAdjustSubmit = useCallback(() => {
    const parsed = parseBudgetInput(budgetInput);
    if (parsed === null) {
      setInputError('Budget must be a positive whole number of tokens.');
      return;
    }
    setInputError(null);
    // Re-preview with ?budget=N and stay open. When the value did not
    // change, bump the sequence so the fetch still re-runs.
    if (budget === parsed) {
      setReloadSeq((s) => s + 1);
    } else {
      setBudget(parsed);
    }
  }, [budgetInput, budget]);

  const handleSend = useCallback(() => {
    if (status !== 'ready' || !manifest) return;
    onConfirm(effectiveBudget(manifest, budget));
  }, [status, manifest, budget, onConfirm]);

  const handleCancel = useCallback(() => {
    setAdjustOpen(false);
    setInputError(null);
    onClose();
  }, [onClose]);

  // ── Derived display values (only meaningful when ready) ─────────────
  const tokenBudget = manifest?.tokenBudget ?? 0;
  const tokensUsed = manifest?.tokensUsed ?? 0;
  const includedCount = auditIncludedCount(manifest);
  const omittedCount = manifest?.omittedCount ?? 0;
  const omittedReason = (manifest?.omittedReason ?? '').trim();
  const hashShort = manifestHashShort(manifest?.manifestHash ?? null);
  const topSources = topAuditSources(manifest, 5);
  const cappedNote = budgetCappedNote(budget, tokenBudget);

  const sendStyle: CSSProperties = {
    backgroundColor: status === 'ready' ? 'var(--color-accent-2-600)' : token.surfaceInput,
    color: status === 'ready' ? 'var(--color-gray-50)' : token.contentFaint,
    borderRadius: 'var(--radius-lg)',
    cursor: status === 'ready' ? 'pointer' : 'not-allowed',
    outlineColor: token.accent2,
  };

  const secondaryStyle: CSSProperties = {
    backgroundColor: token.surfaceInput,
    color: token.contentPrimary,
    borderColor: token.lineSubtle,
    borderRadius: 'var(--radius-lg)',
    outlineColor: token.accent2,
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center"
      style={{ backgroundColor: 'rgba(0,0,0,0.6)' }}
      onClick={handleCancel}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="context-audit-title"
        data-testid="context-audit-dialog"
        className="relative w-full max-w-md mx-4 rounded-xl border shadow-2xl"
        style={{
          backgroundColor: token.surfaceRaised,
          borderColor: token.lineSubtle,
        }}
        onClick={(e) => e.stopPropagation()}
      >
        {/* Header */}
        <div
          className="flex items-center justify-between px-5 py-4 border-b"
          style={{ borderColor: token.lineSubtle }}
        >
          <div className="flex items-center gap-2">
            <Shield className="w-4 h-4" style={{ color: token.accent2 }} />
            <h2
              id="context-audit-title"
              className="text-base font-semibold"
              style={{ color: token.contentPrimary }}
            >
              Run with compiled context
            </h2>
          </div>
          <button
            type="button"
            onClick={handleCancel}
            className="p-1 rounded-md transition-colors hover:bg-white/5"
            style={{ color: token.contentMuted }}
            aria-label="Cancel context run"
            data-testid="context-audit-dismiss"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        {/* Body */}
        <div className="px-5 py-4 space-y-3">
          <p
            className="text-xs line-clamp-2"
            style={{ color: token.contentMuted }}
            title={message}
          >
            Message: {message}
          </p>
          <p className="text-xs" style={{ color: token.contentFaint }}>
            Node: <span className="font-mono">{shortNodeId(nodeId)}</span>
          </p>

          {status === 'loading' && (
            <div
              data-testid="context-audit-loading"
              className="flex items-center gap-2 py-6 justify-center text-sm"
              style={{ color: token.contentMuted }}
            >
              <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />
              Compiling context…
            </div>
          )}

          {status === 'error' && (
            <div
              className="flex items-start gap-2 py-4"
              role="alert"
              data-testid="context-audit-error"
            >
              <AlertTriangle
                className="w-4 h-4 flex-shrink-0 mt-0.5"
                style={{ color: token.danger }}
                aria-hidden="true"
              />
              <div className="space-y-2">
                <p className="text-sm" style={{ color: token.contentPrimary }}>
                  Could not compile this node’s context.
                </p>
                <p className="text-xs" style={{ color: token.contentMuted }}>
                  {previewError}
                </p>
                <div className="flex items-center gap-2">
                  <button
                    type="button"
                    onClick={handleRetry}
                    data-testid="context-audit-retry"
                    className="px-3 h-8 text-sm font-medium border transition-colors focus-visible:outline-2 focus-visible:outline-offset-2"
                    style={secondaryStyle}
                  >
                    Retry
                  </button>
                  <button
                    type="button"
                    onClick={handleCancel}
                    data-testid="context-audit-error-cancel"
                    className="px-3 h-8 text-sm font-medium border transition-colors focus-visible:outline-2 focus-visible:outline-offset-2"
                    style={secondaryStyle}
                  >
                    Cancel
                  </button>
                </div>
              </div>
            </div>
          )}

          {status === 'ready' && manifest && (
            <>
              <div className="space-y-1.5">
                <AuditRow label="Token budget" testid="context-audit-budget">
                  {formatTokenCount(tokenBudget)}
                </AuditRow>
                <AuditRow label="Tokens used" testid="context-audit-tokens-used">
                  {formatTokenUsage(tokensUsed, tokenBudget)}
                </AuditRow>
                <AuditRow label="Included sources" testid="context-audit-included">
                  {countLabel(includedCount, 'source', 'sources')}
                </AuditRow>
                <AuditRow label="Omitted" testid="context-audit-omitted">
                  {countLabel(omittedCount, 'item', 'items')}
                  {omittedReason ? ` (${omittedReason})` : ''}
                </AuditRow>
                {hashShort && (
                  <AuditRow label="Manifest hash" testid="context-audit-hash">
                    <span
                      className="font-mono"
                      title={manifest?.manifestHash ?? undefined}
                    >
                      {hashShort}
                    </span>
                  </AuditRow>
                )}
              </div>

              {cappedNote && (
                <p
                  data-testid="context-audit-capped"
                  className="text-xs"
                  style={{ color: token.warning }}
                >
                  {cappedNote}
                </p>
              )}

              {topSources.length > 0 && (
                <div className="space-y-1">
                  <p
                    className="text-xs font-medium"
                    style={{ color: token.contentMuted }}
                  >
                    Top included sources
                  </p>
                  <ul className="space-y-0.5">
                    {topSources.map((src: ContextAuditSource, i: number) => (
                      <li
                        key={`${src.id}-${i}`}
                        data-testid="context-audit-source"
                        className="flex items-baseline justify-between gap-3 text-xs"
                        style={{ color: token.contentSecondary }}
                        title={src.title}
                      >
                        <span className="truncate">
                          {src.title.trim() || shortNodeId(src.id) || 'Untitled'}
                        </span>
                        <span
                          className="flex-shrink-0 font-mono"
                          style={{ color: token.contentFaint }}
                        >
                          {formatTokenCount(src.tokenCount)}
                          {src.truncated ? ' ✂' : ''}
                        </span>
                      </li>
                    ))}
                  </ul>
                </div>
              )}

              {/* Adjust — budget input, client-validated */}
              <div className="space-y-1.5">
                <button
                  type="button"
                  onClick={() => setAdjustOpen((v) => !v)}
                  aria-expanded={adjustOpen}
                  data-testid="context-audit-adjust"
                  className="text-xs font-medium focus-visible:outline-2 focus-visible:outline-offset-2"
                  style={{ color: token.accent2, outlineColor: token.accent2 }}
                >
                  {adjustOpen ? 'Hide budget input' : 'Adjust budget'}
                </button>
                {adjustOpen && (
                  <div className="flex items-center gap-2">
                    <input
                      type="text"
                      inputMode="numeric"
                      value={budgetInput}
                      onChange={(e) => {
                        setBudgetInput(e.target.value);
                        if (inputError) setInputError(null);
                      }}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') {
                          e.preventDefault();
                          handleAdjustSubmit();
                        }
                      }}
                      placeholder={String(tokenBudget || '')}
                      aria-label="Token budget"
                      data-testid="context-audit-budget-input"
                      className="flex-1 px-3 py-2 rounded-lg border text-sm outline-none focus:ring-1 focus:ring-accent-2"
                      style={{
                        backgroundColor: token.surfaceInput,
                        borderColor: token.lineSubtle,
                        color: token.contentPrimary,
                      }}
                    />
                    <button
                      type="button"
                      onClick={handleAdjustSubmit}
                      data-testid="context-audit-budget-apply"
                      className="px-3 h-9 text-sm font-medium border transition-colors focus-visible:outline-2 focus-visible:outline-offset-2"
                      style={secondaryStyle}
                    >
                      Update preview
                    </button>
                  </div>
                )}
                {inputError && (
                  <p
                    role="alert"
                    data-testid="context-audit-budget-error"
                    className="text-xs"
                    style={{ color: token.danger }}
                  >
                    {inputError}
                  </p>
                )}
              </div>
            </>
          )}
        </div>

        {/* Footer actions — Cancel always; Send only when a manifest is shown */}
        <div
          className="flex items-center justify-end gap-2 px-5 py-4 border-t"
          style={{ borderColor: token.lineSubtle }}
        >
          <button
            type="button"
            onClick={handleCancel}
            data-testid="context-audit-cancel"
            className="px-3 h-8 text-sm font-medium border transition-colors focus-visible:outline-2 focus-visible:outline-offset-2"
            style={secondaryStyle}
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={handleSend}
            disabled={status !== 'ready'}
            data-testid="context-audit-send"
            className="px-3.5 h-8 text-sm font-medium transition-colors focus-visible:outline-2 focus-visible:outline-offset-2"
            style={{
              ...sendStyle,
              opacity: status === 'ready' ? 1 : 0.6,
              boxShadow:
                status === 'ready'
                  ? `0 0 0 1px ${alpha(palette.accent2, 0.24)}`
                  : undefined,
            }}
          >
            Send
          </button>
        </div>
      </div>
    </div>
  );
}
