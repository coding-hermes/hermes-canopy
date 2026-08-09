/**
 * Hermes Canopy — BlastRadiusViz Component (SPEC-023-UI-004)
 *
 * Visualizes a PR's blast radius: the set of files touched (left column)
 * and the count of downstream dependents (right column). Uses a
 * dependency-light SVG radial burst — no d3-force needed.
 *
 * The radial layout places the PR node at the center and arranges file
 * nodes around it in a circle, connected by edges. Dependents are shown
 * as a numeric badge.
 */

import { memo } from 'react';
import { FileCode2, GitBranch } from 'lucide-react';

interface BlastRadiusVizProps {
  files: string[];
  dependents: number;
}

function shortPath(path: string): string {
  const parts = path.split('/');
  if (parts.length <= 2) return path;
  return `…/${parts.slice(-2).join('/')}`;
}

export const BlastRadiusViz = memo(function BlastRadiusViz({
  files,
  dependents,
}: BlastRadiusVizProps) {
  const fileCount = files.length;
  const maxNodes = 8; // cap visible nodes to keep the viz readable
  const visible = files.slice(0, maxNodes);

  // SVG radial layout — PR node at center, file nodes around it.
  const cx = 80;
  const cy = 80;
  const r = 52;
  const nodeR = 6;

  const nodes = visible.map((file, i) => {
    const angle = (i / Math.max(visible.length, 1)) * 2 * Math.PI - Math.PI / 2;
    return {
      file,
      x: cx + r * Math.cos(angle),
      y: cy + r * Math.sin(angle),
    };
  });

  return (
    <div
      data-testid="blast-radius-viz"
      className="rounded-lg bg-surface-input/60 p-3 ring-1 ring-inset ring-line-subtle"
    >
      <div className="flex items-start gap-4">
        {/* Radial SVG graph */}
        <div className="shrink-0">
          <svg
            width="160"
            height="160"
            viewBox="0 0 160 160"
            role="img"
            aria-label={`Blast radius: ${fileCount} files, ${dependents} dependents`}
          >
            {/* Edges */}
            {nodes.map((n, i) => (
              <line
                key={`edge-${i}`}
                x1={cx}
                y1={cy}
                x2={n.x}
                y2={n.y}
                stroke="#2d2d4a"
                strokeWidth="1"
                strokeDasharray="2 2"
              />
            ))}
            {/* File nodes */}
            {nodes.map((n, i) => (
              <g key={`node-${i}`}>
                <circle
                  cx={n.x}
                  cy={n.y}
                  r={nodeR}
                  fill="#1a1a2e"
                  stroke="#7c3aed"
                  strokeWidth="1.5"
                />
                <title>{n.file}</title>
              </g>
            ))}
            {/* Center PR node */}
            <circle cx={cx} cy={cy} r="12" fill="#7c3aed" opacity="0.25" />
            <circle cx={cx} cy={cy} r="8" fill="#7c3aed" />
          </svg>
        </div>

        {/* Stats + file list */}
        <div className="min-w-0 flex-1 space-y-2">
          <div className="flex items-center gap-3">
            <span className="inline-flex items-center gap-1 rounded-md bg-accent-2/10 px-2 py-1 text-[11px] font-medium text-accent-2-300 ring-1 ring-inset ring-accent-2/20">
              <FileCode2 className="h-3 w-3" aria-hidden="true" />
              {fileCount} {fileCount === 1 ? 'file' : 'files'}
            </span>
            <span className="inline-flex items-center gap-1 rounded-md bg-status-warning/10 px-2 py-1 text-[11px] font-medium text-status-warning ring-1 ring-inset ring-status-warning/20">
              <GitBranch className="h-3 w-3" aria-hidden="true" />
              {dependents} {dependents === 1 ? 'dependent' : 'dependents'}
            </span>
          </div>
          {fileCount > 0 ? (
            <ul className="space-y-0.5">
              {visible.map((f) => (
                <li
                  key={f}
                  data-testid="blast-file"
                  className="truncate font-mono text-[11px] text-content-secondary"
                  title={f}
                >
                  {shortPath(f)}
                </li>
              ))}
              {fileCount > maxNodes && (
                <li className="text-[11px] text-content-faint">
                  +{fileCount - maxNodes} more
                </li>
              )}
            </ul>
          ) : (
            <p className="text-[11px] text-content-muted">No files touched.</p>
          )}
        </div>
      </div>
    </div>
  );
});
