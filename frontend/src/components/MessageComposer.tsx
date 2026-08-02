/**
 * Hermes Canopy — Message Composer (UI-06, Phase 11 Mockup Parity)
 *
 * The floating bottom bar from docs/mockups/mockup-1.png: a rounded navy
 * surface that hovers over the canvas rather than being welded to its
 * bottom edge. Left to right —
 *
 *   📎  paperclip …… attach files (client-side only, see below)
 *   │   hairline divider
 *   ▁   the input …… auto-growing textarea, mockup placeholder copy
 *   @ # ☺ …………………… trigger + emoji inserts at the caret
 *   ➤   Send ⌘↵ …… accent-filled primary action with its shortcut badge
 *
 * Retained from FE-05: drag-and-drop attachment with preview thumbnails,
 * pinned context chips, Cmd/Ctrl+Enter to send, character/token count,
 * disabled + read-only states.
 *
 * Attachments never leave the browser. There is no upload endpoint in the
 * MVP backend, so the paperclip collects `File` handles and the send path
 * records only their descriptors in node metadata — no API is invented.
 *
 * `onSend` may return a promise. When it does, the bar shows a sending
 * state, keeps the user's text if the promise rejects, and surfaces the
 * server's own message in an inline error row instead of throwing.
 *
 * Colour comes from the token layer exclusively (`theme.ts` / index.css);
 * every pairing below is ≥ 4.5:1 (white on accent-2-600 is 5.70, the
 * badge's purple-100 on the same fill is 4.80, muted content on the panel
 * surface is 5.90).
 */

import {
  useState,
  useRef,
  useCallback,
  useEffect,
  type DragEvent,
  type ChangeEvent,
  type KeyboardEvent,
  type CSSProperties,
  type ReactNode,
} from 'react';
import { Send, Paperclip, Pin, X, AtSign, Hash, Smile } from 'lucide-react';
import { token, palette, alpha, nodeTypeColor } from '../theme.ts';
import {
  DEFAULT_PLACEHOLDER,
  describeSendError,
  insertAtCursor,
} from '../lib/composer.ts';

// ─── Types ─────────────────────────────────────────────────────────────

export interface PinnedNode {
  id: string;
  label: string;
  nodeType: string;
}

export interface MessageComposerProps {
  /**
   * Called when the user sends. May return a promise — the bar awaits it,
   * clearing the input only once it resolves so a failed POST does not eat
   * the message.
   */
  onSend: (
    message: string,
    files: File[],
    pinnedNodes: PinnedNode[],
  ) => void | Promise<void>;
  /** Whether the composer is disabled (e.g., tree not ready) */
  disabled?: boolean;
  /** Read-only mode — viewers cannot send messages (shows view-only state) */
  readOnly?: boolean;
  /** Placeholder text for the textarea */
  placeholder?: string;
}

// ─── Helpers ───────────────────────────────────────────────────────────

function getNodeColor(nodeType: string): string {
  return nodeTypeColor(nodeType);
}

function getNodeTypeLabel(nodeType: string): string {
  switch (nodeType) {
    case 'synthesis':
      return 'Synthesized';
    case 'card':
      return 'Card';
    case 'topic':
      return 'Topic';
    case 'system':
      return 'System';
    case 'message':
      return 'Message';
    default:
      return nodeType;
  }
}

/** Emoji offered by the ☺ button. Small on purpose — this is a shortcut,
 *  not a full picker, and the OS one is a keystroke away. */
const EMOJI_CHOICES = [
  '👍',
  '🎉',
  '🔥',
  '✅',
  '🌳',
  '🚀',
  '💡',
  '🤔',
  '👀',
  '❤️',
  '😄',
  '🙏',
] as const;

// ─── Icon button ───────────────────────────────────────────────────────

interface IconButtonProps {
  onClick: () => void;
  label: string;
  disabled?: boolean;
  active?: boolean;
  children: ReactNode;
}

/**
 * A composer affordance: muted until hovered, accent-tinted when its popover
 * is open. Kept as one component so every icon in the bar shares the same
 * focus ring, hit area and disabled treatment.
 */
function IconButton({
  onClick,
  label,
  disabled = false,
  active = false,
  children,
}: IconButtonProps) {
  const [hover, setHover] = useState(false);
  const lit = !disabled && (hover || active);

  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      onBlur={() => setHover(false)}
      className="flex items-center justify-center w-8 h-8 rounded-md transition-colors focus-visible:outline-2 focus-visible:outline-offset-2"
      style={{
        color: lit ? token.accent2 : token.contentMuted,
        backgroundColor: lit ? alpha(palette.accent2, 0.14) : 'transparent',
        outlineColor: token.accent2,
        opacity: disabled ? 0.4 : 1,
        cursor: disabled ? 'not-allowed' : 'pointer',
      }}
      title={label}
      aria-label={label}
    >
      {children}
    </button>
  );
}

// ─── File Preview ──────────────────────────────────────────────────────

interface FilePreviewProps {
  file: File;
  onRemove: () => void;
}

function FilePreview({ file, onRemove }: FilePreviewProps) {
  const [previewUrl, setPreviewUrl] = useState<string | null>(null);

  useEffect(() => {
    if (file.type.startsWith('image/')) {
      const url = URL.createObjectURL(file);
      setPreviewUrl(url);
      return () => URL.revokeObjectURL(url);
    }
  }, [file]);

  const icon = file.type.startsWith('image/') ? '🖼' : '📄';
  const name =
    file.name.length > 20 ? file.name.slice(0, 17) + '...' : file.name;

  return (
    <div
      className="relative group flex items-center gap-2 px-2 py-1 rounded-md border border-line-subtle bg-surface-input text-content-primary text-xs"
      title={file.name}
    >
      {previewUrl ? (
        <img
          src={previewUrl}
          alt={file.name}
          className="w-8 h-8 object-cover rounded-xs"
        />
      ) : (
        <span className="text-base flex-shrink-0">{icon}</span>
      )}
      <span className="max-w-[100px] truncate">{name}</span>
      <span className="flex-shrink-0 text-content-muted">
        {formatFileSize(file.size)}
      </span>
      <button
        type="button"
        onClick={onRemove}
        className="ml-0.5 opacity-60 hover:opacity-100 transition-opacity"
        title={`Remove ${file.name}`}
        aria-label={`Remove ${file.name}`}
      >
        <X className="w-3 h-3" />
      </button>
    </div>
  );
}

function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

// ─── Main Component ────────────────────────────────────────────────────

export default function MessageComposer({
  onSend,
  disabled = false,
  readOnly = false,
  placeholder = DEFAULT_PLACEHOLDER,
}: MessageComposerProps) {
  const [text, setText] = useState('');
  const [files, setFiles] = useState<File[]>([]);
  const [pinnedNodes, setPinnedNodes] = useState<PinnedNode[]>([]);
  const [isDragOver, setIsDragOver] = useState(false);
  const [showEmoji, setShowEmoji] = useState(false);
  const [isSending, setIsSending] = useState(false);
  const [sendError, setSendError] = useState<string | null>(null);
  const [isFocused, setIsFocused] = useState(false);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const emojiWrapRef = useRef<HTMLDivElement>(null);

  // ─── Auto-resize textarea ─────────────────────────────────────────

  const adjustHeight = useCallback(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = 'auto';
    const newHeight = Math.min(el.scrollHeight, 300);
    el.style.height = `${newHeight}px`;
  }, []);

  useEffect(() => {
    adjustHeight();
  }, [text, adjustHeight]);

  // Focus textarea on mount
  useEffect(() => {
    if (!disabled) {
      textareaRef.current?.focus();
    }
  }, [disabled]);

  // ─── Derived state ────────────────────────────────────────────────

  const charCount = text.length;
  // Rough token estimate: ~4 characters per token for English text
  const tokenEstimate = Math.max(0, Math.ceil(charCount / 4));
  const isInputDisabled = disabled || readOnly;
  const canSend = !isInputDisabled && !isSending && text.trim().length > 0;

  // ─── Caret insertion (@ / # / emoji) ──────────────────────────────

  /**
   * Type `trigger` for the user at the caret and hand focus back, so the
   * @ button behaves like pressing @ rather than like a menu.
   */
  const insertTrigger = useCallback(
    (trigger: string) => {
      const el = textareaRef.current;
      const start = el?.selectionStart ?? text.length;
      const end = el?.selectionEnd ?? start;
      const { text: next, cursor } = insertAtCursor(text, start, end, trigger);
      setText(next);
      requestAnimationFrame(() => {
        const node = textareaRef.current;
        if (!node) return;
        node.focus();
        node.setSelectionRange(cursor, cursor);
      });
    },
    [text],
  );

  // Close the emoji popover on outside click or Escape.
  useEffect(() => {
    if (!showEmoji) return;
    const onPointerDown = (e: MouseEvent) => {
      if (!emojiWrapRef.current?.contains(e.target as Node)) {
        setShowEmoji(false);
      }
    };
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === 'Escape') setShowEmoji(false);
    };
    document.addEventListener('mousedown', onPointerDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onPointerDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [showEmoji]);

  // ─── Send handler ─────────────────────────────────────────────────

  const handleSend = useCallback(async () => {
    if (!canSend) return;
    const sentText = text;
    const sentFiles = files;

    setIsSending(true);
    setSendError(null);
    try {
      await onSend(sentText, sentFiles, pinnedNodes);
      // Only clear once the write actually landed — a rejected send keeps
      // the user's words in the box.
      setText('');
      setFiles([]);
      // Keep pinned nodes across messages by default (they're sticky context)
    } catch (err) {
      setSendError(describeSendError(err));
    } finally {
      setIsSending(false);
    }
  }, [canSend, text, files, pinnedNodes, onSend]);

  // ─── Keyboard shortcut ────────────────────────────────────────────

  const handleKeyDown = useCallback(
    (e: KeyboardEvent<HTMLTextAreaElement>) => {
      // Cmd/Ctrl + Enter to send. Enter alone stays a newline.
      if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
        e.preventDefault();
        void handleSend();
      }
    },
    [handleSend],
  );

  // ─── File handling ────────────────────────────────────────────────

  const handleFileSelect = useCallback((e: ChangeEvent<HTMLInputElement>) => {
    const selected = Array.from(e.target.files ?? []);
    if (selected.length > 0) {
      setFiles((prev) => [...prev, ...selected]);
    }
    // Reset so the same file can be re-selected
    if (fileInputRef.current) fileInputRef.current.value = '';
  }, []);

  const removeFile = useCallback((index: number) => {
    setFiles((prev) => prev.filter((_, i) => i !== index));
  }, []);

  const removePinnedNode = useCallback((nodeId: string) => {
    setPinnedNodes((prev) => prev.filter((n) => n.id !== nodeId));
  }, []);

  // ─── Drag-and-drop ────────────────────────────────────────────────

  const handleDragOver = useCallback((e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    e.stopPropagation();
    // Only activate drop zone if dragging files (not text)
    if (e.dataTransfer?.types.includes('Files')) {
      setIsDragOver(true);
    }
  }, []);

  const handleDragLeave = useCallback((e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    e.stopPropagation();
    setIsDragOver(false);
  }, []);

  const handleDrop = useCallback((e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    e.stopPropagation();
    setIsDragOver(false);
    const dropped = Array.from(e.dataTransfer.files);
    if (dropped.length > 0) {
      setFiles((prev) => [...prev, ...dropped]);
    }
  }, []);

  // ─── Styles ───────────────────────────────────────────────────────

  /** The floating bar: rounded navy slab lifted off the canvas edge. */
  const barStyle: CSSProperties = {
    backgroundColor: token.surfacePanel,
    borderColor: isFocused ? alpha(palette.accent2, 0.45) : token.line,
    borderRadius: 'var(--radius-xl)',
    boxShadow: isFocused
      ? `0 10px 30px -12px ${alpha(palette.surfaceBase, 0.85)}, 0 0 0 3px ${alpha(palette.accent2, 0.14)}`
      : `0 10px 30px -12px ${alpha(palette.surfaceBase, 0.85)}`,
  };

  /** Send: accent-2-600 fill. White label measures 5.70:1 on it (AA). */
  const sendStyle: CSSProperties = {
    backgroundColor: canSend
      ? 'var(--color-accent-2-600)'
      : token.surfaceInput,
    color: canSend ? 'var(--color-gray-50)' : token.contentFaint,
    borderRadius: 'var(--radius-lg)',
    cursor: canSend ? 'pointer' : 'not-allowed',
    outlineColor: token.accent2,
  };

  return (
    <div className="shrink-0 px-4 pb-4 pt-1">
      <div
        className="relative border transition-shadow"
        style={barStyle}
        onDragOver={handleDragOver}
        onDragLeave={handleDragLeave}
        onDrop={handleDrop}
        data-testid="composer-bar"
      >
        {/* Drop zone overlay */}
        {isDragOver && (
          <div
            className="absolute inset-0 z-20 flex items-center justify-center border-2 border-dashed border-accent-2"
            style={{
              backgroundColor: alpha(palette.accent2Strong, 0.14),
              borderRadius: 'var(--radius-xl)',
            }}
          >
            <span className="text-sm font-medium flex items-center gap-2 text-accent-2">
              <Paperclip className="w-4 h-4" />
              Drop files to attach
            </span>
          </div>
        )}

        {/* ── Pinned context nodes ─────────────────────────────────── */}
        {pinnedNodes.length > 0 && (
          <div className="flex items-center gap-1.5 px-3 pt-2.5 flex-wrap">
            <Pin className="w-3.5 h-3.5 flex-shrink-0 text-accent-2" />
            {pinnedNodes.map((node) => {
              const color = getNodeColor(node.nodeType);
              return (
                <span
                  key={node.id}
                  className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-xs border transition-colors"
                  style={{
                    backgroundColor: alpha(color, 0.1),
                    borderColor: alpha(color, 0.27),
                    color,
                  }}
                  title={`${getNodeTypeLabel(node.nodeType)}: ${node.label}`}
                >
                  {node.label}
                  <button
                    type="button"
                    onClick={() => removePinnedNode(node.id)}
                    className="ml-0.5 opacity-60 hover:opacity-100 transition-opacity"
                    aria-label={`Unpin ${node.label}`}
                  >
                    <X className="w-3 h-3" />
                  </button>
                </span>
              );
            })}
          </div>
        )}

        {/* ── File previews ────────────────────────────────────────── */}
        {files.length > 0 && (
          <div className="flex items-center gap-2 px-3 pt-2.5 flex-wrap">
            {files.map((file, i) => (
              <FilePreview
                key={`${file.name}-${file.size}-${i}`}
                file={file}
                onRemove={() => removeFile(i)}
              />
            ))}
          </div>
        )}

        {/* ── Input row ────────────────────────────────────────────── */}
        <div className="flex items-end gap-2 px-3 py-2.5">
          {/* Attach — leftmost, per mockup */}
          <div className="flex items-center gap-1 pb-0.5">
            <IconButton
              onClick={() => fileInputRef.current?.click()}
              label="Attach files"
              disabled={isInputDisabled}
            >
              <Paperclip className="w-4 h-4" />
            </IconButton>
            <input
              ref={fileInputRef}
              type="file"
              multiple
              className="hidden"
              onChange={handleFileSelect}
              tabIndex={-1}
              aria-hidden="true"
            />
            {/* Hairline divider between the attach control and the input */}
            <span
              aria-hidden="true"
              className="w-px h-5 mx-0.5"
              style={{ backgroundColor: token.lineSubtle }}
            />
          </div>

          {/* Textarea */}
          <textarea
            ref={textareaRef}
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={handleKeyDown}
            onFocus={() => setIsFocused(true)}
            onBlur={() => setIsFocused(false)}
            placeholder={placeholder}
            disabled={isInputDisabled}
            rows={1}
            className="flex-1 min-w-0 resize-none bg-transparent text-sm text-content-primary placeholder:text-content-muted outline-none placeholder:select-none self-center py-1.5"
            style={{
              maxHeight: '300px',
              lineHeight: '1.5',
              opacity: isInputDisabled ? 0.5 : 1,
              cursor: isInputDisabled ? 'not-allowed' : 'text',
            }}
            aria-label="Message input"
          />

          {/* Trigger cluster + Send */}
          <div className="flex items-center gap-1 flex-shrink-0 pb-0.5">
            <IconButton
              onClick={() => insertTrigger('@')}
              label="Mention someone"
              disabled={isInputDisabled}
            >
              <AtSign className="w-4 h-4" />
            </IconButton>

            <IconButton
              onClick={() => insertTrigger('#')}
              label="Reference a topic"
              disabled={isInputDisabled}
            >
              <Hash className="w-4 h-4" />
            </IconButton>

            {/* Emoji + its popover */}
            <div className="relative" ref={emojiWrapRef}>
              <IconButton
                onClick={() => setShowEmoji((v) => !v)}
                label="Insert emoji"
                disabled={isInputDisabled}
                active={showEmoji}
              >
                <Smile className="w-4 h-4" />
              </IconButton>

              {showEmoji && (
                <div
                  role="menu"
                  aria-label="Emoji picker"
                  className="absolute bottom-full right-0 mb-2 z-30 grid gap-0.5 p-1.5 border"
                  style={{
                    backgroundColor: token.surfaceRaised,
                    borderColor: token.line,
                    borderRadius: 'var(--radius-lg)',
                    boxShadow: `0 12px 32px -12px ${alpha(palette.surfaceBase, 0.9)}`,
                    // Fixed 1.75rem tracks, not `grid-cols-6`. The popover is
                    // absolutely positioned, so its width is shrink-to-fit and
                    // `minmax(0,1fr)` tracks collapse below the buttons' own
                    // width — the cells then overlap and only the last one is
                    // clickable.
                    gridTemplateColumns: 'repeat(6, 1.75rem)',
                  }}
                >
                  {EMOJI_CHOICES.map((emoji) => (
                    <button
                      key={emoji}
                      type="button"
                      role="menuitem"
                      onClick={() => {
                        insertTrigger(emoji);
                        setShowEmoji(false);
                      }}
                      className="w-7 h-7 flex items-center justify-center text-base rounded-md transition-colors hover:bg-surface-hover focus-visible:outline-2 focus-visible:outline-offset-1"
                      style={{ outlineColor: token.accent2 }}
                      aria-label={`Insert ${emoji}`}
                      title={emoji}
                    >
                      {emoji}
                    </button>
                  ))}
                </div>
              )}
            </div>

            {/* Send — prominent accent action with its shortcut badge */}
            <button
              type="button"
              onClick={() => void handleSend()}
              disabled={!canSend}
              className="ml-1 flex items-center gap-1.5 pl-3 pr-2 h-8 text-sm font-medium transition-colors focus-visible:outline-2 focus-visible:outline-offset-2"
              style={sendStyle}
              title={
                canSend ? 'Send message (⌘↵)' : 'Type a message to send'
              }
              aria-label="Send message"
              aria-keyshortcuts="Meta+Enter Control+Enter"
            >
              <Send className="w-4 h-4" aria-hidden="true" />
              <span>{isSending ? 'Sending' : 'Send'}</span>
              <kbd
                aria-hidden="true"
                className="px-1 py-0.5 rounded-xs text-[10px] font-mono leading-none"
                style={{
                  backgroundColor: canSend
                    ? alpha(palette.surfaceBase, 0.3)
                    : 'transparent',
                  color: canSend
                    ? 'var(--color-purple-100)'
                    : token.contentFaint,
                }}
              >
                ⌘↵
              </kbd>
            </button>
          </div>
        </div>

        {/* ── Inline error ─────────────────────────────────────────── */}
        {sendError && (
          <div
            role="alert"
            className="flex items-start gap-2 px-3.5 pb-2 text-xs text-status-danger"
          >
            <span className="flex-1">{sendError}</span>
            <button
              type="button"
              onClick={() => setSendError(null)}
              className="opacity-70 hover:opacity-100 transition-opacity"
              aria-label="Dismiss error"
              title="Dismiss error"
            >
              <X className="w-3 h-3" />
            </button>
          </div>
        )}
      </div>

      {/* ── Footer ─────────────────────────────────────────────────── */}
      <div className="flex items-center justify-between px-2 pt-1.5 text-xs select-none text-content-muted">
        <span>
          {readOnly
            ? '🔒 View-only mode'
            : charCount > 0
              ? `${charCount} chars · ~${tokenEstimate} tokens`
              : 'Ready'}
        </span>
        <span className="flex items-center gap-1">
          <kbd className="px-1 py-0.5 rounded-xs text-[10px] font-mono bg-surface-input border border-line-subtle text-content-secondary">
            ⌘↵
          </kbd>
          <span>to send</span>
        </span>
      </div>
    </div>
  );
}
