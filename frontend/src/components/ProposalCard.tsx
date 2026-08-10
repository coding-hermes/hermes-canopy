/**
 * Hermes Canopy — Proposal Card (TM-02)
 *
 * Spec: SPEC-TM-02 §5 (Agent Proposal Flow), §6 (User Interaction Model).
 *
 * Inline, non-blocking card attached to the triggering node. Displays:
 * title, one-line rationale, signal type label, confidence band, affected
 * root node. Actions: Accept, Name it differently (inline title input),
 * Reject. Keyboard: Enter=Accept, Escape=Dismiss (only when card focused).
 *
 * State machine: pending → confirming (disabled) → created (show
 * "Topic created" + link) | rejected (hide) | error (inline, card stays).
 *
 * Stale-card reconciliation: if the server returns an expired/already-
 * resolved error, the card is removed (spec §6, §11.2 scenario 6/12).
 *
 * Never steals focus, never blocks sending.
 */

import {
  useState,
  useRef,
  useCallback,
  type KeyboardEvent as ReactKeyboardEvent,
} from 'react';
import { Check, X, Edit3, AlertCircle, ExternalLink } from 'lucide-react';
import type { ProposalCard as ProposalCardVM } from '../stores/topicProposalStore';
import {
  setCardStatus,
  removeCard,
} from '../stores/topicProposalStore';
import {
  confirmProposal,
  dismissProposal,
} from '../lib/topicDetectionApi';
import { notifyTopicsChanged } from '../lib/activeTree';
import {
  confidenceBand,
  CONFIDENCE_BAND_LABELS,
  DETECTION_TYPE_LABELS,
} from '../types/topic-detection';
import { palette, alpha } from '../theme';

// ─── Props ─────────────────────────────────────────────────────────────

export interface ProposalCardProps {
  card: ProposalCardVM;
  /** Tree id for the topic link. */
  treeId: string;
}

// ─── Title validation ──────────────────────────────────────────────────

const MAX_TITLE = 200;

/** Spec §6: title validation 1–200 chars. */
function validateTitle(title: string): string | null {
  const trimmed = title.trim();
  if (trimmed.length === 0) return 'Title is required';
  if (trimmed.length > MAX_TITLE) return `Title must be 1–${MAX_TITLE} characters`;
  return null;
}

// ─── Stale-card reconciliation ─────────────────────────────────────────

/**
 * Error messages from the backend that indicate the proposal is stale
 * (expired, already resolved, or concurrently resolved). On these, the
 * card is removed rather than retried.
 */
const STALE_ERROR_PATTERNS = [
  'expired',
  'already resolved',
  'not found',
  'already exists',
  'slug conflict',
];

function isStaleError(message: string): boolean {
  const lower = message.toLowerCase();
  return STALE_ERROR_PATTERNS.some((p) => lower.includes(p));
}

// ─── Component ─────────────────────────────────────────────────────────

export function ProposalCard({ card, treeId }: ProposalCardProps) {
  const { proposal, status, createdTopic, error } = card;
  const [showRename, setShowRename] = useState(false);
  const [renameValue, setRenameValue] = useState('');
  const [renameError, setRenameError] = useState<string | null>(null);
  const cardRef = useRef<HTMLDivElement>(null);

  const band = confidenceBand(proposal.confidence);
  const accent = palette.accent3;

  // ── Accept (spec §5.5) ────────────────────────────────────────────

  const handleAccept = useCallback(async () => {
    setCardStatus(proposal.proposalId, 'confirming');
    try {
      const result = await confirmProposal(proposal.proposalId);
      setCardStatus(proposal.proposalId, 'created', {
        createdTopic: result.topic,
      });
      notifyTopicsChanged();
      // Auto-remove after showing confirmation
      setTimeout(() => removeCard(proposal.proposalId), 4000);
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Failed to confirm';
      if (isStaleError(msg)) {
        removeCard(proposal.proposalId);
      } else {
        setCardStatus(proposal.proposalId, 'error', { error: msg });
      }
    }
  }, [proposal.proposalId]);

  // ── Rename (spec §5.6) ────────────────────────────────────────────

  const handleStartRename = useCallback(() => {
    setRenameValue(proposal.title);
    setRenameError(null);
    setShowRename(true);
  }, [proposal.title]);

  const handleCancelRename = useCallback(() => {
    setShowRename(false);
    setRenameError(null);
    setRenameValue('');
  }, []);

  const handleSubmitRename = useCallback(async () => {
    const validation = validateTitle(renameValue);
    if (validation) {
      setRenameError(validation);
      return;
    }
    setCardStatus(proposal.proposalId, 'confirming');
    setShowRename(false);
    try {
      const result = await confirmProposal(proposal.proposalId, renameValue.trim());
      setCardStatus(proposal.proposalId, 'created', {
        createdTopic: result.topic,
      });
      notifyTopicsChanged();
      setTimeout(() => removeCard(proposal.proposalId), 4000);
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Failed to rename';
      if (isStaleError(msg)) {
        removeCard(proposal.proposalId);
      } else {
        setCardStatus(proposal.proposalId, 'error', { error: msg });
      }
    }
  }, [proposal.proposalId, renameValue]);

  // ── Reject/Dismiss (spec §5.7) ────────────────────────────────────

  const handleReject = useCallback(async () => {
    setCardStatus(proposal.proposalId, 'confirming');
    try {
      await dismissProposal(proposal.proposalId);
      removeCard(proposal.proposalId);
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Failed to dismiss';
      if (isStaleError(msg)) {
        removeCard(proposal.proposalId);
      } else {
        setCardStatus(proposal.proposalId, 'error', { error: msg });
      }
    }
  }, [proposal.proposalId]);

  // ── Keyboard (spec §6: Enter=Accept, Escape=Dismiss) ──────────────
  // Only fires when the card itself has focus — doesn't steal global keys.

  const handleKeyDown = useCallback(
    (e: ReactKeyboardEvent<HTMLDivElement>) => {
      if (showRename) return; // input handles its own keys
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault();
        void handleAccept();
      } else if (e.key === 'Escape') {
        e.preventDefault();
        void handleReject();
      }
    },
    [showRename, handleAccept, handleReject],
  );

  // ── Created state (spec §6: "Topic created" + link) ───────────────

  if (status === 'created' && createdTopic) {
    return (
      <div
        data-testid="proposal-card-created"
        className="mt-2 rounded-lg border px-3 py-2"
        style={{
          borderColor: alpha(palette.success, 0.35),
          backgroundColor: alpha(palette.success, 0.08),
        }}
      >
        <div className="flex items-center gap-2 text-xs">
          <Check
            className="h-3.5 w-3.5 shrink-0"
            style={{ color: palette.success }}
            aria-hidden="true"
          />
          <span className="font-medium text-content-primary">
            Topic created
          </span>
          <a
            href={`/topics?tree=${encodeURIComponent(treeId)}&topic=${encodeURIComponent(createdTopic.id)}`}
            className="ml-auto inline-flex items-center gap-1 text-[11px] underline-offset-2 hover:underline"
            style={{ color: accent }}
          >
            #{createdTopic.slug}
            <ExternalLink className="h-3 w-3" aria-hidden="true" />
          </a>
        </div>
      </div>
    );
  }

  // ── Pending / confirming / error ──────────────────────────────────

  const disabled = status === 'confirming';

  return (
    <div
      ref={cardRef}
      tabIndex={0}
      onKeyDown={handleKeyDown}
      data-testid="proposal-card"
      data-proposal-id={proposal.proposalId}
      data-status={status}
      role="article"
      aria-label={`Topic proposal: ${proposal.title}`}
      className="mt-2 rounded-lg border px-3 py-2.5 outline-none focus-visible:ring-2"
      style={{
        borderColor: alpha(accent, 0.35),
        backgroundColor: alpha(accent, 0.06),
      }}
    >
      {/* Header — signal type + confidence band */}
      <div className="mb-1.5 flex items-center gap-2 text-[10px] uppercase tracking-wide">
        <span
          className="rounded-sm px-1.5 py-0.5 font-medium"
          style={{
            color: accent,
            backgroundColor: alpha(accent, 0.12),
          }}
        >
          {DETECTION_TYPE_LABELS[proposal.detectionType] ?? proposal.detectionType}
        </span>
        <span className="text-content-muted">
          {CONFIDENCE_BAND_LABELS[band]}
        </span>
      </div>

      {/* Title — "I think this is a new topic about [title] — create?" */}
      {!showRename && (
        <p className="text-sm text-content-primary">
          I think this is a new topic about{' '}
          <span className="font-semibold">{proposal.title}</span> — create?
        </p>
      )}

      {/* Rationale (one-line) */}
      {!showRename && proposal.description && (
        <p className="mt-0.5 line-clamp-1 text-[11px] text-content-muted">
          {proposal.description}
        </p>
      )}

      {/* Rename input */}
      {showRename && (
        <div className="space-y-1.5">
          <label className="sr-only" htmlFor={`rename-${proposal.proposalId}`}>
            Topic title
          </label>
          <input
            id={`rename-${proposal.proposalId}`}
            type="text"
            value={renameValue}
            onChange={(e) => {
              setRenameValue(e.target.value);
              setRenameError(null);
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                void handleSubmitRename();
              } else if (e.key === 'Escape') {
                e.preventDefault();
                handleCancelRename();
              }
            }}
            autoFocus
            maxLength={MAX_TITLE + 10}
            data-testid="proposal-rename-input"
            aria-label="Topic title"
            className="w-full rounded-md bg-surface-input px-2 py-1 text-sm text-content-primary ring-1 ring-inset ring-line-subtle focus:outline-none focus-visible:ring-2 focus-visible:ring-accent"
          />
          <div className="flex items-center justify-between text-[10px]">
            <span className={renameError ? 'text-status-danger' : 'text-content-muted'}>
              {renameError ?? `${renameValue.trim().length}/${MAX_TITLE}`}
            </span>
            <span className="text-content-faint">Enter to submit · Esc to cancel</span>
          </div>
        </div>
      )}

      {/* Inline error */}
      {status === 'error' && error && (
        <div
          className="mt-1.5 flex items-start gap-1.5 rounded-md px-2 py-1 text-[11px] text-status-danger"
          style={{ backgroundColor: alpha(palette.danger, 0.08) }}
          role="alert"
          data-testid="proposal-card-error"
        >
          <AlertCircle className="mt-px h-3 w-3 shrink-0" aria-hidden="true" />
          <span className="min-w-0 break-words">{error}</span>
        </div>
      )}

      {/* Actions */}
      {!showRename && (
        <div className="mt-2 flex items-center gap-1.5">
          <button
            type="button"
            onClick={() => void handleAccept()}
            disabled={disabled}
            data-testid="proposal-accept"
            className="inline-flex items-center gap-1 rounded-md px-2.5 py-1 text-xs font-medium text-content-inverse transition-opacity hover:brightness-110 disabled:opacity-50"
            style={{ backgroundColor: accent }}
          >
            <Check className="h-3 w-3" aria-hidden="true" />
            Accept
          </button>
          <button
            type="button"
            onClick={handleStartRename}
            disabled={disabled}
            data-testid="proposal-rename"
            className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-content-secondary ring-1 ring-inset ring-line-subtle transition-colors hover:bg-surface-hover disabled:opacity-50"
          >
            <Edit3 className="h-3 w-3" aria-hidden="true" />
            Name it differently
          </button>
          <button
            type="button"
            onClick={() => void handleReject()}
            disabled={disabled}
            data-testid="proposal-reject"
            className="ml-auto inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-content-muted transition-colors hover:bg-surface-hover hover:text-content-primary disabled:opacity-50"
          >
            <X className="h-3 w-3" aria-hidden="true" />
            Reject
          </button>
        </div>
      )}

      {/* Rename actions */}
      {showRename && (
        <div className="mt-2 flex items-center gap-1.5">
          <button
            type="button"
            onClick={() => void handleSubmitRename()}
            disabled={disabled || validateTitle(renameValue) !== null}
            data-testid="proposal-rename-submit"
            className="inline-flex items-center gap-1 rounded-md px-2.5 py-1 text-xs font-medium text-content-inverse transition-opacity hover:brightness-110 disabled:opacity-50"
            style={{ backgroundColor: accent }}
          >
            <Check className="h-3 w-3" aria-hidden="true" />
            Submit
          </button>
          <button
            type="button"
            onClick={handleCancelRename}
            disabled={disabled}
            data-testid="proposal-rename-cancel"
            className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-content-muted ring-1 ring-inset ring-line-subtle transition-colors hover:bg-surface-hover disabled:opacity-50"
          >
            Cancel
          </button>
        </div>
      )}
    </div>
  );
}

export default ProposalCard;
