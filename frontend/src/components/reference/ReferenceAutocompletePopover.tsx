/**
 * Hermes Canopy — Reference Autocomplete Popover (TM-04)
 *
 * Spec: SPEC-TM-04 §9.1. Opens when the user types '#' followed by a letter
 * in the message composer. Fetches suggestions from the autocomplete API with
 * 80ms debounce. Supports keyboard navigation (Up/Down/Enter/Tab/Escape) and
 * click selection.
 *
 * Adapted from the spec's contenteditable design to work with the existing
 * MessageComposer's HTMLTextAreaElement (spec §9.1 — verified facts #6).
 * Positioning uses the textarea bounding rect + an approximate caret offset
 * (no mirror element needed for MVP — the popover anchors to the bottom-left
 * of the textarea, which is where #references are typically typed).
 */

import {
  useState,
  useEffect,
  useRef,
} from 'react';
import { Loader2, Hash, MessageSquare } from 'lucide-react';
import { autocompleteReferences } from '../../lib/referenceApi';
import { getActiveReferencePrefix } from '../../lib/topic-reference';
import { readStoredTreeId } from '../../lib/activeTree';
import { token, palette, alpha } from '../../theme';
import type { ReferenceAutocompleteResult } from '../../types/reference';

const DEBOUNCE_MS = 80;
const MAX_RESULTS = 10;

export interface ReferenceAutocompletePopoverProps {
  /** The textarea element, used for cursor positioning */
  textareaRef: React.RefObject<HTMLTextAreaElement | null>;
  /** Current textarea content */
  content: string;
  /** Current cursor position in the textarea */
  cursorOffset: number;
  /** Called when a slug is selected — inserts "#slug" at the trigger position */
  onSelect: (slug: string, triggerOffset: number) => void;
  /** Called when the popover should close (Escape, blur, no match) */
  onClose: () => void;
}

export default function ReferenceAutocompletePopover({
  textareaRef,
  content,
  cursorOffset,
  onSelect,
  onClose,
}: ReferenceAutocompletePopoverProps) {
  const [results, setResults] = useState<ReferenceAutocompleteResult[]>([]);
  const [loading, setLoading] = useState(false);
  const [selectedIndex, setSelectedIndex] = useState(0);
  const [triggerOffset, setTriggerOffset] = useState(-1);
  const debounceTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const treeId = readStoredTreeId();

  // Detect the active reference prefix at the cursor.
  const prefix = getActiveReferencePrefix(content, cursorOffset);

  useEffect(() => {
    // Close if no active reference token.
    if (prefix === null) {
      onClose();
      return;
    }

    // Find the trigger offset (the '#').
    let hashIdx = -1;
    for (let i = cursorOffset - 1; i >= 0; i--) {
      if (content[i] === '#') {
        hashIdx = i;
        break;
      }
    }
    setTriggerOffset(hashIdx);
    setSelectedIndex(0);

    // Debounced autocomplete fetch.
    if (debounceTimer.current) clearTimeout(debounceTimer.current);

    if (!treeId) {
      setResults([]);
      return;
    }

    setLoading(true);
    debounceTimer.current = setTimeout(async () => {
      try {
        const resp = await autocompleteReferences(treeId, prefix, {
          limit: MAX_RESULTS,
        });
        setResults(resp.results || []);
      } catch {
        setResults([]);
      } finally {
        setLoading(false);
      }
    }, DEBOUNCE_MS);

    return () => {
      if (debounceTimer.current) clearTimeout(debounceTimer.current);
    };
  }, [prefix, content, cursorOffset, treeId, onClose]);

  // Keyboard handler is attached directly to the textarea element via
  // the useEffect below (capture phase listener).
  // We keep the handleKeyDown function for potential external use but the
  // real handler is inline in the effect.

  // Store the handler so the parent can call it.
  // This is set via a data attribute that the parent reads.
  useEffect(() => {
    if (textareaRef.current) {
      // Attach the keyboard handler to the textarea element directly.
      const el = textareaRef.current;
      const handler = (e: globalThis.KeyboardEvent) => {
        if (triggerOffset < 0) return;
        if (results.length === 0) return;

        let handled = false;
        switch (e.key) {
          case 'ArrowDown':
            e.preventDefault();
            setSelectedIndex((i) => (i + 1) % results.length);
            handled = true;
            break;
          case 'ArrowUp':
            e.preventDefault();
            setSelectedIndex((i) => (i - 1 + results.length) % results.length);
            handled = true;
            break;
          case 'Enter':
          case 'Tab':
            e.preventDefault();
            if (results[selectedIndex]) {
              onSelect(results[selectedIndex].slug, triggerOffset);
            }
            handled = true;
            break;
          case 'Escape':
            e.preventDefault();
            onClose();
            handled = true;
            break;
        }
        if (handled) e.stopPropagation();
      };
      el.addEventListener('keydown', handler, true); // capture phase
      return () => el.removeEventListener('keydown', handler, true);
    }
  }, [triggerOffset, results, selectedIndex, onSelect, onClose, textareaRef]);

  // Don't render if no trigger or prefix is null.
  if (prefix === null || triggerOffset < 0) return null;

  return (
    <div
      role="listbox"
      aria-label="Topic references"
      className="absolute bottom-full left-0 mb-2 z-30 w-80 max-h-64 overflow-y-auto border rounded-lg"
      style={{
        backgroundColor: token.surfaceRaised,
        borderColor: token.line,
        boxShadow: `0 12px 32px -12px ${alpha(palette.surfaceBase, 0.9)}`,
      }}
    >
      {loading && (
        <div className="flex items-center gap-2 px-3 py-2 text-sm" style={{ color: token.contentMuted }}>
          <Loader2 className="w-4 h-4 animate-spin" />
          <span>Searching topics…</span>
        </div>
      )}

      {!loading && results.length === 0 && (
        <div className="flex items-center gap-2 px-3 py-2 text-sm" style={{ color: token.contentMuted }}>
          <MessageSquare className="w-4 h-4" />
          <span>No topics found for "#{prefix}"</span>
        </div>
      )}

      {!loading &&
        results.map((result, i) => (
          <button
            key={result.slug}
            type="button"
            role="option"
            aria-selected={i === selectedIndex}
            onClick={() => onSelect(result.slug, triggerOffset)}
            className="w-full flex items-center gap-2 px-3 py-2 text-left text-sm transition-colors"
            style={{
              backgroundColor: i === selectedIndex ? alpha(palette.accent2, 0.1) : 'transparent',
              color: token.contentPrimary,
            }}
            onMouseEnter={() => setSelectedIndex(i)}
          >
            <Hash className="w-3.5 h-3.5 flex-shrink-0" style={{ color: token.accent2 }} />
            <div className="flex-1 min-w-0">
              <div className="font-medium truncate">{result.title}</div>
              <div
                className="text-xs truncate"
                style={{ color: token.contentMuted }}
              >
                #{result.slug} · {result.node_count} nodes
              </div>
            </div>
          </button>
        ))}
    </div>
  );
}
