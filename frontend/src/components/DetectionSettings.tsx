/**
 * Hermes Canopy — Detection Settings (TM-02)
 *
 * Spec: SPEC-TM-02 §7 (per-tree DetectionConfig), §11.1 scenario 12/13.
 *
 * Compact settings control: level selector (off | explicit_only | full),
 * auto-create toggle, always-ask toggle. GET config on mount (per active
 * tree), PUT on change.
 *
 * Hidden entirely when detection is off — EXCEPT the settings control
 * itself must remain accessible to turn it back on (spec §6: "A proposal
 * never appears when detection is off; stale cards are reconciled").
 */

import { useState, useEffect, useCallback } from 'react';
import { Sparkles, ChevronDown } from 'lucide-react';
import {
  getDetectionConfig,
  updateDetectionConfig,
} from '../lib/topicDetectionApi';
import type {
  DetectionConfig,
  DetectionLevel,
} from '../types/topic-detection';
import {
  DETECTION_LEVEL_LABELS,
  DEFAULT_DETECTION_CONFIG,
} from '../types/topic-detection';

// ─── Props ─────────────────────────────────────────────────────────────

export interface DetectionSettingsProps {
  treeId: string;
}

// ─── Component ─────────────────────────────────────────────────────────

export function DetectionSettings({ treeId }: DetectionSettingsProps) {
  const [config, setConfig] = useState<DetectionConfig | null>(null);
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);

  // GET config on mount and when treeId changes
  const loadConfig = useCallback(async () => {
    if (!treeId) return;
    setLoading(true);
    try {
      const cfg = await getDetectionConfig(treeId);
      setConfig(cfg);
    } catch {
      // Endpoint may not exist yet (404) — use defaults so the control works
      setConfig(DEFAULT_DETECTION_CONFIG);
    } finally {
      setLoading(false);
    }
  }, [treeId]);

  useEffect(() => {
    void loadConfig();
  }, [loadConfig]);

  // PUT on change
  const updateConfig = useCallback(
    async (patch: Partial<DetectionConfig>) => {
      if (!treeId) return;
      const prev = config;
      // Optimistic update
      const next = { ...(prev ?? DEFAULT_DETECTION_CONFIG), ...patch };
      setConfig(next);
      try {
        const updated = await updateDetectionConfig(treeId, patch);
        setConfig(updated);
      } catch {
        // Revert on error
        setConfig(prev);
      }
    },
    [treeId, config],
  );

  const detectionOff = config?.detection_level === 'off';

  return (
    <div className="relative" data-testid="detection-settings">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        title="Topic detection settings"
        aria-label="Topic detection settings"
        aria-expanded={open}
        data-testid="detection-settings-toggle"
        className={[
          'grid h-8 w-8 place-items-center rounded-md transition-colors',
          detectionOff
            ? 'text-content-muted hover:bg-surface-hover hover:text-content-primary'
            : 'text-accent-3 bg-accent-3/10 hover:bg-accent-3/20',
        ].join(' ')}
      >
        <Sparkles className="h-4 w-4" aria-hidden="true" />
      </button>

      {open && (
        <>
          {/* Outside-click backdrop */}
          <div
            className="fixed inset-0 z-30"
            onClick={() => setOpen(false)}
            aria-hidden="true"
          />
          <div
            role="dialog"
            aria-label="Topic detection settings"
            data-testid="detection-settings-panel"
            className="glass-raised absolute bottom-10 left-0 z-40 w-64 rounded-lg border border-line-subtle p-3"
          >
            <div className="mb-2.5 flex items-center gap-1.5">
              <Sparkles className="h-3.5 w-3.5 text-accent-3" aria-hidden="true" />
              <h3 className="text-xs font-semibold text-content-primary">
                Topic Detection
              </h3>
              {loading && (
                <span className="ml-auto text-[10px] text-content-muted">Loading…</span>
              )}
            </div>

            {/* Level selector */}
            <label
              className="mb-1 block text-[11px] font-medium text-content-secondary"
              htmlFor="detection-level"
            >
              Sensitivity
            </label>
            <div className="relative mb-3">
              <select
                id="detection-level"
                value={config?.detection_level ?? 'full'}
                onChange={(e) =>
                  void updateConfig({
                    detection_level: e.target.value as DetectionLevel,
                  })
                }
                disabled={loading}
                className="h-8 w-full appearance-none rounded-md bg-surface-input px-2.5 pr-7 text-xs text-content-primary ring-1 ring-inset ring-line-subtle focus:outline-none focus-visible:ring-2 focus-visible:ring-accent disabled:opacity-50"
                data-testid="detection-level-select"
              >
                {(Object.keys(DETECTION_LEVEL_LABELS) as DetectionLevel[]).map(
                  (level) => (
                    <option key={level} value={level}>
                      {DETECTION_LEVEL_LABELS[level]}
                    </option>
                  ),
                )}
              </select>
              <ChevronDown
                className="pointer-events-none absolute right-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-content-muted"
                aria-hidden="true"
              />
            </div>

            {/* Toggles — hidden when detection is off (but control remains) */}
            {!detectionOff && (
              <div className="space-y-2">
                <ToggleRow
                  label="Auto-create topics"
                  description="Create without asking"
                  checked={config?.auto_create ?? false}
                  onChange={(v) => void updateConfig({ auto_create: v })}
                  testId="detection-auto-create"
                />
                <ToggleRow
                  label="Always ask"
                  description="Confirm even with auto-create"
                  checked={config?.always_ask ?? true}
                  onChange={(v) => void updateConfig({ always_ask: v })}
                  testId="detection-always-ask"
                />
              </div>
            )}

            {detectionOff && (
              <p className="text-[11px] text-content-muted">
                Detection is off. No proposals will appear. Change the
                sensitivity to re-enable.
              </p>
            )}
          </div>
        </>
      )}
    </div>
  );
}

// ─── Toggle row ────────────────────────────────────────────────────────

function ToggleRow({
  label,
  description,
  checked,
  onChange,
  testId,
}: {
  label: string;
  description: string;
  checked: boolean;
  onChange: (value: boolean) => void;
  testId: string;
}) {
  return (
    <label
      className="flex cursor-pointer items-center justify-between gap-2"
      data-testid={testId}
    >
      <span className="min-w-0">
        <span className="block text-[11px] font-medium text-content-secondary">
          {label}
        </span>
        <span className="block text-[10px] text-content-muted">{description}</span>
      </span>
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        aria-label={label}
        onClick={() => onChange(!checked)}
        className={[
          'relative h-5 w-9 shrink-0 rounded-full transition-colors',
          checked ? 'bg-accent-3/60' : 'bg-surface-input ring-1 ring-inset ring-line-subtle',
        ].join(' ')}
      >
        <span
          className={[
            'absolute top-0.5 h-4 w-4 rounded-full bg-content-inverse shadow transition-transform',
            checked ? 'translate-x-4' : 'translate-x-0.5',
          ].join(' ')}
        />
      </button>
    </label>
  );
}

export default DetectionSettings;
