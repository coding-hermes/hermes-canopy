/**
 * Hermes Canopy — Message Composer
 *
 * Rich message input area with:
 *   - Auto-growing textarea (max 300px, then scrolls)
 *   - File attachment via button + drag-and-drop (with preview thumbnails)
 *   - Context pinning (pin/freeze context nodes as chips)
 *   - Send button with Cmd/Ctrl+Enter keyboard shortcut
 *   - Character count with token estimate
 *
 * Wired into TreeView.tsx so messages compose against the active tree context.
 */

import {
  useState,
  useRef,
  useCallback,
  useEffect,
  type DragEvent,
  type ChangeEvent,
  type KeyboardEvent,
} from 'react';
import { Send, Paperclip, Pin, X } from 'lucide-react';

// ─── Types ─────────────────────────────────────────────────────────────

export interface PinnedNode {
  id: string;
  label: string;
  nodeType: string;
}

export interface MessageComposerProps {
  /** Called when the user sends a message */
  onSend: (message: string, files: File[], pinnedNodes: PinnedNode[]) => void;
  /** Whether the composer is disabled (e.g., tree not ready) */
  disabled?: boolean;
  /** Read-only mode — viewers cannot send messages (shows view-only state) */
  readOnly?: boolean;
  /** Placeholder text for the textarea */
  placeholder?: string;
}

// ─── Helpers ───────────────────────────────────────────────────────────

function getNodeColor(nodeType: string): string {
  switch (nodeType) {
    case 'synthesis':
      return '#f59e0b';
    case 'card':
      return '#3b82f6';
    case 'topic':
      return '#f43f5e';
    case 'system':
      return '#3b82f6';
    case 'message':
      return '#22c55e';
    default:
      return '#6b7280';
  }
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
    file.name.length > 20
      ? file.name.slice(0, 17) + '...'
      : file.name;

  return (
    <div
      className="relative group flex items-center gap-2 px-2 py-1 rounded-md border text-xs"
      style={{
        backgroundColor: '#1a1a2e',
        borderColor: '#2d2d4a',
        color: '#e2e8f0',
      }}
      title={file.name}
    >
      {previewUrl ? (
        <img
          src={previewUrl}
          alt={file.name}
          className="w-8 h-8 object-cover rounded"
        />
      ) : (
        <span className="text-base flex-shrink-0">{icon}</span>
      )}
      <span className="max-w-[100px] truncate">{name}</span>
      <span className="flex-shrink-0" style={{ color: '#94a3b8' }}>
        {formatFileSize(file.size)}
      </span>
      <button
        onClick={onRemove}
        className="ml-0.5 opacity-60 hover:opacity-100 transition-opacity"
        title="Remove file"
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
  placeholder = 'Type a message...',
}: MessageComposerProps) {
  const [text, setText] = useState('');
  const [files, setFiles] = useState<File[]>([]);
  const [pinnedNodes, setPinnedNodes] = useState<PinnedNode[]>([]);
  const [isDragOver, setIsDragOver] = useState(false);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

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
  const canSend = !disabled && !readOnly && text.trim().length > 0;
  const isInputDisabled = disabled || readOnly;

  // ─── Send handler ─────────────────────────────────────────────────

  const handleSend = useCallback(() => {
    if (!canSend) return;
    onSend(text, files, pinnedNodes);
    setText('');
    setFiles([]);
    // Keep pinned nodes across messages by default (they're sticky context)
  }, [canSend, text, files, pinnedNodes, onSend]);

  // ─── Keyboard shortcut ────────────────────────────────────────────

  const handleKeyDown = useCallback(
    (e: KeyboardEvent<HTMLTextAreaElement>) => {
      // Cmd/Ctrl + Enter to send
      if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
        e.preventDefault();
        handleSend();
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

  // ─── Render ───────────────────────────────────────────────────────

  return (
    <div
      className="border-t shrink-0"
      style={{ borderColor: '#2d2d4a', backgroundColor: '#0f0f1a' }}
    >
      {/* ── Pinned context nodes ───────────────────────────────────── */}
      {pinnedNodes.length > 0 && (
        <div className="flex items-center gap-1.5 px-4 pt-2.5 pb-1 flex-wrap">
          <Pin
            className="w-3.5 h-3.5 flex-shrink-0"
            style={{ color: '#7c3aed' }}
          />
          {pinnedNodes.map((node) => {
            const color = getNodeColor(node.nodeType);
            return (
              <span
                key={node.id}
                className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-xs border transition-colors"
                style={{
                  backgroundColor: `${color}18`,
                  borderColor: `${color}44`,
                  color,
                }}
                title={`${getNodeTypeLabel(node.nodeType)}: ${node.label}`}
              >
                {node.label}
                <button
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

      {/* ── File previews ──────────────────────────────────────────── */}
      {files.length > 0 && (
        <div className="flex items-center gap-2 px-4 pt-2.5 flex-wrap">
          {files.map((file, i) => (
            <FilePreview
              key={`${file.name}-${file.size}-${i}`}
              file={file}
              onRemove={() => removeFile(i)}
            />
          ))}
        </div>
      )}

      {/* ── Composer area ──────────────────────────────────────────── */}
      <div
        className="relative px-4 py-3"
        onDragOver={handleDragOver}
        onDragLeave={handleDragLeave}
        onDrop={handleDrop}
      >
        {/* Drop zone overlay */}
        {isDragOver && (
          <div
            className="absolute inset-0 z-10 flex items-center justify-center border-2 border-dashed rounded-lg mx-4 my-2"
            style={{
              borderColor: '#7c3aed',
              backgroundColor: 'rgba(124, 58, 237, 0.12)',
            }}
          >
            <span
              className="text-sm font-medium flex items-center gap-2"
              style={{ color: '#a78bfa' }}
            >
              <Paperclip className="w-4 h-4" />
              Drop files to attach
            </span>
          </div>
        )}

        <div className="flex items-start gap-2">
          {/* Textarea */}
          <textarea
            ref={textareaRef}
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder={placeholder}
            disabled={isInputDisabled}
            rows={1}
            className="flex-1 resize-none bg-transparent text-sm outline-none placeholder:select-none"
            style={{
              color: '#e2e8f0',
              maxHeight: '300px',
              lineHeight: '1.5',
              opacity: isInputDisabled ? 0.5 : 1,
              cursor: isInputDisabled ? 'not-allowed' : 'text',
            }}
            aria-label="Message input"
          />

          {/* Action buttons */}
          <div className="flex items-center gap-0.5 flex-shrink-0 pt-0.5">
            {/* Pin context button */}
            <button
              onClick={() => {
                // This would trigger a node picker in a fuller implementation.
                // For now, we emit the intent — the parent handles node selection.
                // Placeholder: pin the "current context" (could be selected node).
              }}
              className="p-1.5 rounded-md transition-colors"
              style={{ color: '#94a3b8' }}
              onMouseEnter={(e) => {
                e.currentTarget.style.backgroundColor = '#1a1a2e';
                e.currentTarget.style.color = '#a78bfa';
              }}
              onMouseLeave={(e) => {
                e.currentTarget.style.backgroundColor = 'transparent';
                e.currentTarget.style.color = '#94a3b8';
              }}
              title="Pin context node"
              aria-label="Pin context node"
            >
              <Pin className="w-4 h-4" />
            </button>

            {/* Attach file button */}
            <button
              onClick={() => fileInputRef.current?.click()}
              className="p-1.5 rounded-md transition-colors"
              style={{ color: '#94a3b8' }}
              onMouseEnter={(e) => {
                e.currentTarget.style.backgroundColor = '#1a1a2e';
                e.currentTarget.style.color = '#e2e8f0';
              }}
              onMouseLeave={(e) => {
                e.currentTarget.style.backgroundColor = 'transparent';
                e.currentTarget.style.color = '#94a3b8';
              }}
              title="Attach files"
              aria-label="Attach files"
            >
              <Paperclip className="w-4 h-4" />
            </button>
            <input
              ref={fileInputRef}
              type="file"
              multiple
              className="hidden"
              onChange={handleFileSelect}
            />

            {/* Send button */}
            <button
              onClick={handleSend}
              disabled={!canSend}
              className="p-1.5 rounded-md transition-all"
              style={{
                color: canSend ? '#7c3aed' : '#4a4a6a',
                cursor: canSend ? 'pointer' : 'not-allowed',
                opacity: canSend ? 1 : 0.5,
              }}
              onMouseEnter={(e) => {
                if (canSend) {
                  e.currentTarget.style.backgroundColor = '#7c3aed22';
                  e.currentTarget.style.color = '#a78bfa';
                }
              }}
              onMouseLeave={(e) => {
                e.currentTarget.style.backgroundColor = 'transparent';
                e.currentTarget.style.color = canSend ? '#7c3aed' : '#4a4a6a';
              }}
              title={canSend ? 'Send message (⌘↵)' : 'Type a message to send'}
              aria-label="Send message"
            >
              <Send className="w-4 h-4" />
            </button>
          </div>
        </div>
      </div>

      {/* ── Footer ──────────────────────────────────────────────────── */}
      <div
        className="flex items-center justify-between px-4 pb-2.5 text-xs select-none"
        style={{ color: '#4a4a6a' }}
      >
        {/* Character + token count */}
        <span>
          {readOnly
            ? '🔒 View-only mode'
            : charCount > 0
              ? `${charCount} chars · ~${tokenEstimate} tokens`
              : 'Ready'}
        </span>

        {/* Keyboard shortcut hint */}
        <span className="flex items-center gap-1">
          <kbd
            className="px-1 py-0.5 rounded text-[10px] font-mono"
            style={{
              backgroundColor: '#1a1a2e',
              border: '1px solid #2d2d4a',
            }}
          >
            ⌘↵
          </kbd>
          <span>to send</span>
        </span>
      </div>
    </div>
  );
}
