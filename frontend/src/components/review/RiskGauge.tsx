/**
 * Hermes Canopy — RiskGauge Component (SPEC-023-UI-004)
 *
 * An SVG arc gauge that visualizes a PR risk score (0.0–1.0). Used in
 * the review rail (mini mode) and the detail panel (full mode).
 *
 * No external charting dependency — pure SVG arc with a color band
 * (green < 0.4, amber < 0.7, red ≥ 0.7) matching the backend's
 * deriveVerdict thresholds.
 */

import { memo } from 'react';

interface RiskGaugeProps {
  /** Risk score in [0, 1]. Clamped to this range. */
  score: number;
  /** Mini mode renders a compact horizontal bar for the rail. */
  mini?: boolean;
}

function riskColor(score: number): string {
  const s = Math.max(0, Math.min(1, score));
  if (s < 0.4) return '#22c55e'; // green-500
  if (s < 0.7) return '#f59e0b'; // amber-500
  return '#ef4444'; // red-500
}

function riskLabel(score: number): string {
  const s = Math.max(0, Math.min(1, score));
  if (s < 0.4) return 'Low';
  if (s < 0.7) return 'Medium';
  return 'High';
}

/** Full-size semicircular arc gauge (SVG, no deps). */
function FullGauge({ score }: { score: number }) {
  const clamped = Math.max(0, Math.min(1, score));
  const pct = clamped;
  const color = riskColor(clamped);

  // Semicircle arc: radius 52, centered at (60, 60), from 180° to 360°.
  const r = 52;
  const cx = 60;
  const cy = 60;
  const startAngle = Math.PI; // 180° (left)
  const endAngle = 2 * Math.PI; // 360° (right)
  const angle = startAngle + (endAngle - startAngle) * pct;

  const pointAt = (a: number): [number, number] => [
    cx + r * Math.cos(a),
    cy + r * Math.sin(a),
  ];

  const [sx, sy] = pointAt(startAngle);
  const [ex, ey] = pointAt(endAngle);
  const [vx, vy] = pointAt(angle);
  const largeArcFilled = angle - startAngle > Math.PI ? 1 : 0;
  const largeArcBg = endAngle - startAngle > Math.PI ? 1 : 0;

  const bgPath = `M ${sx} ${sy} A ${r} ${r} 0 ${largeArcBg} 1 ${ex} ${ey}`;
  const fillPath = `M ${sx} ${sy} A ${r} ${r} 0 ${largeArcFilled} 1 ${vx} ${vy}`;

  return (
    <div
      data-testid="risk-gauge"
      className="inline-flex flex-col items-center gap-1"
    >
      <svg width="120" height="70" viewBox="0 0 120 70" role="img" aria-label={`Risk ${Math.round(clamped * 100)}% — ${riskLabel(clamped)}`}>
        <path d={bgPath} fill="none" stroke="#2d2d4a" strokeWidth="8" strokeLinecap="round" />
        <path d={fillPath} fill="none" stroke={color} strokeWidth="8" strokeLinecap="round" />
        <text
          x={cx}
          y={cy - 4}
          textAnchor="middle"
          className="fill-content-primary"
          style={{ fontSize: 18, fontWeight: 700 }}
        >
          {Math.round(clamped * 100)}%
        </text>
      </svg>
      <span
        className="text-[11px] font-medium"
        style={{ color }}
      >
        {riskLabel(clamped)} risk
      </span>
    </div>
  );
}

/** Mini inline risk indicator for the rail. */
function MiniGauge({ score }: { score: number }) {
  const clamped = Math.max(0, Math.min(1, score));
  const color = riskColor(clamped);
  return (
    <span
      data-testid="risk-gauge-mini"
      className="inline-flex items-center gap-1"
      title={`Risk ${Math.round(clamped * 100)}% — ${riskLabel(clamped)}`}
    >
      <span
        className="h-1.5 w-10 overflow-hidden rounded-full bg-surface-base"
        aria-hidden="true"
      >
        <span
          className="block h-full rounded-full"
          style={{ width: `${clamped * 100}%`, backgroundColor: color }}
        />
      </span>
      <span className="text-[11px] tabular-nums text-content-muted">
        {Math.round(clamped * 100)}
      </span>
    </span>
  );
}

export const RiskGauge = memo(function RiskGauge({ score, mini }: RiskGaugeProps) {
  return mini ? <MiniGauge score={score} /> : <FullGauge score={score} />;
});
