/**
 * Hermes Canopy — IterationFileReadCard (§8.2)
 *
 * Compact: file name, language, visible range.
 * Expanded: metadata, a line-numbered annotation list (the card retains
 * highlight ranges/notes, not raw source bytes), and controls to select a
 * range, attach a label/note/color, and expand or collapse the visible range.
 *
 * All paths, labels, and notes render as text only.
 */

import { memo, useState } from 'react';
import { FileText } from 'lucide-react';
import type { FileHighlight, FileReadSubtypeData, IterationRendererProps } from '../../types/agent.ts';
import { formatByteSize } from '../../lib/iterationFormat.ts';
import { IterationLiveRegion, statusLabel } from './iterationShared.tsx';

export type IterationFileReadCardProps = IterationRendererProps<FileReadSubtypeData>;

const COLORS: FileHighlight['color'][] = ['yellow', 'green', 'red', 'blue'];
const RANGE_STEP = 50;

function colorSwatch(color: FileHighlight['color']): string {
  switch (color) {
    case 'yellow': return 'bg-yellow-400';
    case 'green': return 'bg-green-400';
    case 'red': return 'bg-red-400';
    case 'blue': return 'bg-blue-400';
  }
}

function FileReadCard({ cardId, data, compact, submitFeedback }: IterationFileReadCardProps) {
  const initial = data.visibleLines ?? { start: 1, end: Math.max(1, data.lineCount) };
  const [visible, setVisible] = useState<{ start: number; end: number }>(initial);
  const [rangeStart, setRangeStart] = useState(initial.start);
  const [rangeEnd, setRangeEnd] = useState(Math.min(initial.end, data.lineCount));
  const [label, setLabel] = useState('');
  const [note, setNote] = useState('');
  const [color, setColor] = useState<FileHighlight['color']>('yellow');

  const liveMessage = `${data.title}: ${statusLabel(data.state)}; lines ${visible.start}-${visible.end}`;

  const expandRange = () => setVisible((current) => ({ ...current, end: Math.min(data.lineCount, current.end + RANGE_STEP) }));
  const collapseRange = () => setVisible(initial);

  const addHighlight = () => {
    const start = Math.max(1, Math.min(rangeStart, data.lineCount));
    const end = Math.max(start, Math.min(rangeEnd, data.lineCount));
    void submitFeedback?.({
      feedbackType: 'highlight',
      target: { region: { startLine: start, endLine: end }, label: label.trim() || null, note: note.trim() || null, color },
    });
  };

  const inputClass = 'rounded bg-surface-input px-1.5 py-0.5 text-xs text-content-primary';

  return (
    <div className="space-y-2" data-subtype="iteration_file_read" data-testid={`iteration-file-${cardId}`}>
      <IterationLiveRegion id={cardId} message={liveMessage} />

      {compact ? (
        <p className="text-xs text-content-secondary">
          <FileText className="mr-1 inline h-3 w-3" aria-hidden="true" />
          <span className="font-mono text-content-primary">{data.path}</span>
          <span className="ml-2 text-content-muted">· lines {initial.start}-{initial.end}</span>
        </p>
      ) : (
        <>
          <dl className="grid grid-cols-2 gap-x-3 gap-y-1 text-xs">
            <div><dt className="text-content-muted">Path</dt><dd className="font-mono text-content-primary">{data.path}</dd></div>
            <div><dt className="text-content-muted">Language</dt><dd className="text-content-primary">{data.language}</dd></div>
            <div><dt className="text-content-muted">Size</dt><dd className="text-content-primary">{formatByteSize(data.size)}</dd></div>
            <div><dt className="text-content-muted">Lines</dt><dd className="text-content-primary">{data.lineCount}</dd></div>
          </dl>

          <div>
            <div className="flex items-center justify-between">
              <p className="text-xs font-medium text-content-primary">
                Highlights <span className="text-content-muted">(lines {visible.start}-{visible.end})</span>
              </p>
              <div className="flex gap-1">
                <button type="button" onClick={expandRange} className="rounded px-1.5 py-0.5 text-[11px] text-content-secondary hover:bg-surface-hover" aria-label={`Expand range for ${data.title}`}>
                  Expand
                </button>
                <button type="button" onClick={collapseRange} className="rounded px-1.5 py-0.5 text-[11px] text-content-secondary hover:bg-surface-hover" aria-label={`Collapse range for ${data.title}`}>
                  Collapse
                </button>
              </div>
            </div>

            {data.highlights.length === 0 && <p className="mt-1 text-xs text-content-muted">No annotations yet.</p>}
            <ul className="mt-1 space-y-1">
              {data.highlights.map((highlight, index) => (
                <li key={`${highlight.startLine}-${highlight.endLine}-${index}`} className="flex items-start gap-2 rounded bg-surface-input px-2 py-1 text-xs">
                  <span className={`mt-0.5 h-2.5 w-2.5 shrink-0 rounded-full ${colorSwatch(highlight.color)}`} aria-hidden="true" />
                  <span className="font-mono text-content-secondary">{highlight.startLine}-{highlight.endLine}</span>
                  <span className="min-w-0 flex-1 text-content-primary">
                    {highlight.label && <span className="font-medium">{highlight.label}</span>}
                    {highlight.note && <span className="text-content-secondary"> · {highlight.note}</span>}
                  </span>
                </li>
              ))}
            </ul>
          </div>

          {submitFeedback && (
            <form
              className="space-y-1.5 rounded border border-line-subtle p-2"
              onSubmit={(event) => { event.preventDefault(); addHighlight(); }}
              aria-label={`Add highlight to ${data.title}`}
            >
              <p className="text-xs font-medium text-content-primary">Add highlight</p>
              <div className="flex flex-wrap items-center gap-1.5">
                <label className="flex items-center gap-1 text-xs text-content-muted">
                  Start
                  <input type="number" min={1} max={data.lineCount} value={rangeStart} onChange={(event) => setRangeStart(Number(event.target.value))} className={inputClass} aria-label={`Highlight start line for ${data.title}`} />
                </label>
                <label className="flex items-center gap-1 text-xs text-content-muted">
                  End
                  <input type="number" min={1} max={data.lineCount} value={rangeEnd} onChange={(event) => setRangeEnd(Number(event.target.value))} className={inputClass} aria-label={`Highlight end line for ${data.title}`} />
                </label>
                <label className="flex items-center gap-1 text-xs text-content-muted">
                  Label
                  <input type="text" value={label} onChange={(event) => setLabel(event.target.value)} className={inputClass} aria-label={`Highlight label for ${data.title}`} />
                </label>
                <label className="flex items-center gap-1 text-xs text-content-muted">
                  Color
                  <select value={color} onChange={(event) => setColor(event.target.value as FileHighlight['color'])} className={inputClass} aria-label={`Highlight color for ${data.title}`}>
                    {COLORS.map((value) => <option key={value} value={value}>{value}</option>)}
                  </select>
                </label>
              </div>
              <label className="flex items-center gap-1 text-xs text-content-muted">
                Note
                <input type="text" value={note} onChange={(event) => setNote(event.target.value)} className={`${inputClass} flex-1`} aria-label={`Highlight note for ${data.title}`} />
              </label>
              <button type="submit" className="rounded bg-purple-500/20 px-2 py-1 text-xs text-purple-200 hover:bg-purple-500/30" data-testid={`iteration-file-add-${cardId}`}>
                Add highlight
              </button>
            </form>
          )}
        </>
      )}
    </div>
  );
}

export const IterationFileReadCard = memo(FileReadCard);
