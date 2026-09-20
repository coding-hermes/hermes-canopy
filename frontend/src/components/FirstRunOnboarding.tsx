/**
 * Hermes Canopy — First-run onboarding (GAP-093)
 *
 * The zero-data state of the Trees page. A brand-new user sees this card
 * INSTEAD of a bare "No trees yet" box. Every action is real:
 *
 *   1. "Create your first tree" — opens the page's real Create Tree dialog
 *      (same button, same POST /trees path as the header's "New Tree").
 *   2. "Import a tree from a Canopy export file" — reads a JSON export
 *      (produced by GET /api/v1/trees/{id}/export) and POSTs it to the
 *      live POST /api/v1/trees/import route, then opens the imported tree.
 *   3. Hermes session import is CLI-ONLY today (`./bin/canopyd session
 *      import` — see README "Hermes session source (GAP-077)"). There is
 *      no in-browser session import, so the card says that plainly instead
 *      of offering a dead button.
 *
 * No demo data is ever injected here: this component issues no writes on
 * mount — the only network call it makes is the explicit import POST after
 * the user picks a file.
 */

import { useRef, useState } from 'react';
import { Sparkles, Plus, FileUp, TerminalSquare, AlertCircle, Loader2 } from 'lucide-react';
import { apiPost } from '../lib/api';

/** Shape of the 201 response from POST /api/v1/trees/import (ExportResult). */
interface ImportResult {
  treeId: string;
  rootNodeId: string;
  nodeCount: number;
  edgeCount: number;
}

/**
 * Parse a user-selected export file into the body POST /trees/import
 * expects. Exported helpers are unit-tested; the component only branches
 * on the outcome, so a malformed file shows its real error and never
 * reaches the API.
 */
export function parseTreeImportFile(
  text: string,
): { ok: true; data: Record<string, unknown> } | { ok: false; error: string } {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return { ok: false, error: 'The file is not valid JSON.' };
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    return { ok: false, error: 'Export files must be a JSON object (tree + nodes + edges).' };
  }
  const obj = parsed as Record<string, unknown>;
  if (typeof obj.tree !== 'object' || obj.tree === null) {
    return { ok: false, error: "The file has no 'tree' section — this does not look like a Canopy tree export." };
  }
  const tree = obj.tree as Record<string, unknown>;
  if (typeof tree.title !== 'string' || tree.title.trim().length === 0) {
    return { ok: false, error: "The export's tree section has no title — can't create a tree without a name." };
  }
  if (!Array.isArray(obj.nodes) || obj.nodes.length === 0) {
    return { ok: false, error: 'The export contains no messages (nodes) — nothing to import.' };
  }
  return { ok: true, data: obj };
}

export default function FirstRunOnboarding({
  onOpenCreate,
  onImported,
}: {
  onOpenCreate: () => void;
  /** Called with the new tree's id after a successful import (real POST /trees/import). */
  onImported?: (treeId: string) => void;
}) {
  const fileInput = useRef<HTMLInputElement>(null);
  const [importing, setImporting] = useState(false);
  const [importError, setImportError] = useState<string | null>(null);

  const handleFile = async (file: File) => {
    setImporting(true);
    setImportError(null);
    try {
      const text = await file.text();
      const parsed = parseTreeImportFile(text);
      if (!parsed.ok) {
        setImportError(parsed.error);
        return;
      }
      const result = await apiPost<ImportResult>('/trees/import', parsed.data);
      onImported?.(result.treeId);
    } catch (err) {
      setImportError(err instanceof Error ? err.message : String(err));
    } finally {
      setImporting(false);
    }
  };

  return (
    <div
      className="rounded-xl border border-line-subtle bg-surface-panel p-8 md:p-12 text-center"
      data-testid="first-run-onboarding"
    >
      <Sparkles className="w-10 h-10 text-content-faint/60 mx-auto mb-3" aria-hidden="true" />
      <h2 className="text-base font-semibold text-content-primary mb-1">Welcome to Canopy</h2>
      <p className="text-xs text-content-muted max-w-md mx-auto mb-6">
        Your trees list is empty because nothing has been created here yet — no demo
        content is fetched or seeded behind your back. Start with one of the actions
        below, or just create a tree and type into it on the canvas.
      </p>

      <div className="flex flex-wrap items-stretch justify-center gap-3">
        <button
          onClick={onOpenCreate}
          className="inline-flex items-center gap-2 px-4 py-2 rounded-lg text-xs font-semibold text-white bg-accent-2-600 hover:bg-accent-2-500 transition-colors"
          data-testid="onboarding-create-tree"
        >
          <Plus className="w-3.5 h-3.5" />
          Create your first tree
        </button>

        <button
          onClick={() => fileInput.current?.click()}
          disabled={importing}
          className="inline-flex items-center gap-2 px-4 py-2 rounded-lg text-xs font-semibold text-content-secondary bg-surface-input hover:bg-surface-hover ring-1 ring-inset ring-line-subtle transition-colors disabled:opacity-50"
          data-testid="onboarding-import-tree"
        >
          {importing ? (
            <Loader2 className="w-3.5 h-3.5 animate-spin" aria-hidden="true" />
          ) : (
            <FileUp className="w-3.5 h-3.5" />
          )}
          Import a tree from an export file…
        </button>
        {/* Hidden but real: choosing a file POSTs the export JSON to the live
            POST /api/v1/trees/import route. */}
        <input
          ref={fileInput}
          type="file"
          accept=".json,application/json"
          className="hidden"
          data-testid="onboarding-import-input"
          onChange={(e) => {
            const f = e.target.files?.[0];
            if (f) void handleFile(f);
            e.target.value = '';
          }}
        />
      </div>

      {importError && (
        <div
          className="mx-auto mt-4 flex max-w-md items-start gap-2 rounded border border-rose-500/30 bg-rose-500/10 p-2 text-left text-xs text-status-danger"
          role="alert"
          data-testid="onboarding-import-error"
        >
          <AlertCircle className="mt-0.5 h-3.5 w-3.5 flex-shrink-0" aria-hidden="true" />
          <span>{importError}</span>
        </div>
      )}

      <div
        className="mx-auto mt-6 max-w-md rounded-lg border border-line-subtle bg-surface-panel/60 p-3 text-left text-[11px] text-content-muted"
        data-testid="onboarding-session-import-note"
      >
        <TerminalSquare className="mr-1.5 inline-block h-3.5 w-3.5 align-text-bottom text-content-faint" aria-hidden="true" />
        <span className="font-medium text-content-secondary">Import your Hermes sessions?</span>{' '}
        That is a command-line step today — run{' '}
        <code className="rounded bg-surface-input px-1 text-content-primary" data-testid="onboarding-session-import-cli">
          ./bin/canopyd session import
        </code>{' '}
        against your running server (it reads the 6-hourly Hermes snapshot, never your
        live chat state). See the README section "Hermes session source (GAP-077)".
        There is no in-browser session import yet.
      </div>
    </div>
  );
}
