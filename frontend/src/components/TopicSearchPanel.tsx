/**
 * Hermes Canopy — Topic Search Panel (TM-03)
 *
 * Spec: SPEC-TM-03 §10.2 Frontend scenarios. Adapts the spec's sidebar
 * search behavior to the existing sidebar architecture:
 *   • Search box with debounce (300ms)
 *   • Recent topics when no search query
 *   • Result cards with <mark> snippet rendering
 *   • Hover preview popover
 *   • Multi-select injection bar
 *   • Add to Context action
 *   • Empty states + keyboard nav (Esc clears)
 *
 * Uses the shared topicSearchApi client → real backend endpoints.
 * No mocks, no fixtures.
 */

import { useState, useEffect, useCallback, useRef } from 'react';
import {
  Search,
  X,
  Plus,
  Loader2,
  MessageSquare,
  Clock,
  Users,
  Check,
  AlertTriangle,
} from 'lucide-react';
import {
  searchTopics,
  getRecentTopics,
  getTopicPreview,
  injectContext,
} from '../lib/topicSearchApi';
import { readStoredTreeId, resolveDemoAlias, resolveDemoAliasSync } from '../lib/activeTree';
import type {
  TopicSearchResult,
  TopicPreview,
} from '../types/topic-search';

// ── Relative time formatter ──────────────────────────────────────────────

function formatRelativeTime(iso: string): string {
  const d = new Date(iso);
  const now = new Date();
  const diff = now.getTime() - d.getTime();
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return 'just now';
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 2) return 'yesterday';
  return `${days}d ago`;
}

// ── Snippet renderer (renders <mark> tags as highlighted spans) ──────────

function SnippetRenderer({ snippet }: { snippet: string }) {
  // Split on <mark>...</mark> and render highlighted spans.
  const parts = snippet.split(/(<mark>.*?<\/mark>)/g);
  return (
    <p className="text-sm text-muted line-clamp-2 leading-snug">
      {parts.map((part, i) => {
        if (part.startsWith('<mark>') && part.endsWith('</mark>')) {
          return (
            <mark
              key={i}
              className="bg-yellow-500/25 text-text rounded px-0.5"
            >
              {part.slice(6, -7)}
            </mark>
          );
        }
        return <span key={i}>{part}</span>;
      })}
    </p>
  );
}

// ── Search Result Card ───────────────────────────────────────────────────

function SearchResultCard({
  result,
  selected,
  onToggleSelect,
  onHover,
  onHoverEnd,
}: {
  result: TopicSearchResult;
  selected: boolean;
  onToggleSelect: (topicId: string) => void;
  onHover: (topicId: string) => void;
  onHoverEnd: () => void;
}) {
  const title = result.title.length > 60
    ? result.title.slice(0, 57) + '...'
    : result.title;

  return (
    <div
      className={`group rounded-lg border p-2.5 transition-colors cursor-pointer ${
        selected
          ? 'border-accent-2/40 bg-accent-2/8'
          : 'border-border hover:border-border-hover hover:bg-surface-hover/30'
      }`}
      onMouseEnter={() => onHover(result.topic_id)}
      onMouseLeave={onHoverEnd}
      onClick={() => onToggleSelect(result.topic_id)}
    >
      <div className="flex items-start justify-between gap-2">
        <h4 className="text-sm font-medium text-text truncate flex-1" title={result.title}>
          {title}
        </h4>
        {result.status === 'archived' && (
          <span className="text-[10px] uppercase tracking-wide text-muted bg-surface-hover/50 px-1.5 py-0.5 rounded">
            Archived
          </span>
        )}
      </div>
      {result.snippet && <SnippetRenderer snippet={result.snippet} />}
      <div className="flex items-center gap-3 mt-1.5 text-xs text-muted">
        <span className="flex items-center gap-1">
          <MessageSquare className="w-3 h-3" />
          {result.node_count}
        </span>
        <span className="flex items-center gap-1">
          <Clock className="w-3 h-3" />
          {formatRelativeTime(result.last_active_at)}
        </span>
      </div>
    </div>
  );
}

// ── Preview Popover ──────────────────────────────────────────────────────

function PreviewPopover({ preview }: { preview: TopicPreview }) {
  return (
    <div className="absolute z-50 right-full top-0 mr-2 w-72 rounded-lg border border-border bg-surface shadow-xl p-3">
      <h4 className="text-sm font-semibold text-text mb-2 truncate">
        {preview.title}
      </h4>
      {preview.snippets.length > 0 ? (
        <div className="space-y-1.5 mb-2">
          {preview.snippets.slice(0, 3).map((s, i) => (
            <p key={i} className="text-xs text-muted line-clamp-2">
              {s}
            </p>
          ))}
        </div>
      ) : (
        <p className="text-xs text-muted italic mb-2">No content yet</p>
      )}
      <div className="flex items-center gap-3 text-xs text-muted">
        <span className="flex items-center gap-1">
          <Users className="w-3 h-3" />
          {preview.participant_count}
        </span>
        <span className="flex items-center gap-1">
          <MessageSquare className="w-3 h-3" />
          {preview.node_count}
        </span>
        <span>{preview.last_active_rel}</span>
      </div>
    </div>
  );
}

// ── Main Search Panel ────────────────────────────────────────────────────

export default function TopicSearchPanel() {
  // The stored id can be the literal 'demo' (BUG-038/039: sidebar Tree
  // View nav persists it); the backend 400s on it. Resolve synchronously
  // at init so the first effect run never fires tree_id=demo, then
  // re-verify by label search once (harmless; resolves to the same UUID).
  const storedTreeId = readStoredTreeId();
  const [treeId, setTreeId] = useState<string>(resolveDemoAliasSync(storedTreeId));
  useEffect(() => {
    let cancelled = false;
    resolveDemoAlias(storedTreeId).then((id) => {
      if (!cancelled) setTreeId(id);
    });
    return () => {
      cancelled = true;
    };
  }, [storedTreeId]);
  const [query, setQuery] = useState('');
  const [debouncedQuery, setDebouncedQuery] = useState('');
  const [results, setResults] = useState<TopicSearchResult[]>([]);
  const [recent, setRecent] = useState<TopicSearchResult[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [injecting, setInjecting] = useState(false);
  const [injected, setInjected] = useState<string | null>(null);
  const [previewTopicId, setPreviewTopicId] = useState<string | null>(null);
  const [previewData, setPreviewData] = useState<TopicPreview | null>(null);
  const debounceTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Load recent topics on mount.
  useEffect(() => {
    if (!treeId) return;
    setLoading(true);
    getRecentTopics(treeId, 10)
      .then((resp) => {
        setRecent(resp.topics || []);
        setError(null);
      })
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false));
  }, [treeId]);

  // Debounced search.
  useEffect(() => {
    if (debounceTimer.current) clearTimeout(debounceTimer.current);
    if (!query || query.trim().length < 2) {
      setResults([]);
      return;
    }
    debounceTimer.current = setTimeout(() => {
      setDebouncedQuery(query);
    }, 300);
    return () => {
      if (debounceTimer.current) clearTimeout(debounceTimer.current);
    };
  }, [query]);

  // Execute search when debounced query changes.
  useEffect(() => {
    if (!treeId || !debouncedQuery || debouncedQuery.trim().length < 2) return;
    setLoading(true);
    searchTopics(treeId, debouncedQuery, { limit: 20 })
      .then((resp) => {
        setResults(resp.results || []);
        setError(null);
      })
      .catch((e) => {
        setError(e.message);
        setResults([]);
      })
      .finally(() => setLoading(false));
  }, [treeId, debouncedQuery]);

  // Preview hover handler (debounced).
  const hoverTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const handleHover = useCallback((topicId: string) => {
    if (hoverTimer.current) clearTimeout(hoverTimer.current);
    hoverTimer.current = setTimeout(() => {
      if (!treeId) return;
      getTopicPreview(treeId, topicId)
        .then(setPreviewData)
        .catch(() => {});
      setPreviewTopicId(topicId);
    }, 400);
  }, [treeId]);

  const handleHoverEnd = useCallback(() => {
    if (hoverTimer.current) clearTimeout(hoverTimer.current);
    setPreviewTopicId(null);
    setPreviewData(null);
  }, []);

  const handleInject = useCallback(async () => {
    if (!treeId || selected.size === 0) return;
    setInjecting(true);
    setError(null);
    try {
      const resp = await injectContext(treeId, {
        topic_ids: Array.from(selected),
      });
      setInjected(resp.event_id || 'injected');
      setSelected(new Set());
      setTimeout(() => setInjected(null), 3000);
    } catch (e: any) {
      setError(e.message);
    } finally {
      setInjecting(false);
    }
  }, [treeId, selected]);

  const toggleSelect = useCallback((topicId: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(topicId)) next.delete(topicId);
      else next.add(topicId);
      return next;
    });
  }, []);

  // Keyboard navigation.
  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      setQuery('');
      setResults([]);
    }
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey) && selected.size > 0) {
      handleInject();
    }
  }, [selected, handleInject]);

  const displayList = query.trim().length >= 2 ? results : recent;
  const totalSelectedNodes = displayList
    .filter((r) => selected.has(r.topic_id))
    .reduce((sum, r) => sum + r.node_count, 0);

  if (!treeId) {
    return (
      <div className="px-3 py-4 text-center text-sm text-muted">
        Select a tree to search topics
      </div>
    );
  }

  return (
    <div className="flex flex-col h-full" onKeyDown={handleKeyDown}>
      {/* Search box */}
      <div className="px-3 py-2 border-b border-border">
        <div className="relative">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 w-4 h-4 text-muted" />
          <input
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search topics..."
            className="w-full pl-8 pr-8 py-1.5 text-sm bg-surface-hover/30 border border-border rounded-lg focus:outline-none focus:border-accent-2/40 text-text placeholder:text-muted"
            aria-label="Search topics"
          />
          {query && (
            <button
              onClick={() => { setQuery(''); setResults([]); }}
              className="absolute right-2 top-1/2 -translate-y-1/2 text-muted hover:text-text"
            >
              <X className="w-4 h-4" />
            </button>
          )}
        </div>
      </div>

      {/* Error banner */}
      {error && (
        <div className="px-3 py-1.5 text-xs text-red-400 bg-red-500/10 border-b border-red-500/20">
          {error}
        </div>
      )}

      {/* Results / Recent list */}
      <div className="flex-1 overflow-y-auto px-2 py-2 space-y-1.5">
        {loading && (
          <div className="flex items-center justify-center py-4">
            <Loader2 className="w-5 h-5 text-muted animate-spin" />
          </div>
        )}

        {!loading && displayList.length === 0 && (
          <div className="text-center py-6 px-3">
            {query.trim().length >= 2 ? (
              <>
                <p className="text-sm text-muted mb-2">
                  No topics match your query
                </p>
                <button
                  onClick={() => { setQuery(''); setResults([]); }}
                  className="text-xs text-accent-2 hover:underline"
                >
                  Clear search
                </button>
              </>
            ) : (
              <p className="text-sm text-muted">
                No recent topics. Topics appear as you discuss.
              </p>
            )}
          </div>
        )}

        {!loading && displayList.map((result) => (
          <div key={result.topic_id} className="relative">
            <SearchResultCard
              result={result}
              selected={selected.has(result.topic_id)}
              onToggleSelect={toggleSelect}
              onHover={handleHover}
              onHoverEnd={handleHoverEnd}
            />
            {previewTopicId === result.topic_id && previewData && (
              <PreviewPopover preview={previewData} />
            )}
          </div>
        ))}
      </div>

      {/* Selected topics injection bar */}
      {selected.size > 0 && (
        <div className="border-t border-border px-3 py-2 bg-surface-hover/20">
          {totalSelectedNodes > 500 && (
            <div className="flex items-center gap-1.5 text-xs text-yellow-500 mb-1.5">
              <AlertTriangle className="w-3 h-3" />
              <span>
                ~{totalSelectedNodes} nodes — context may truncate
              </span>
            </div>
          )}
          <div className="flex items-center justify-between gap-2">
            <span className="text-xs text-muted">
              {selected.size} selected (~{totalSelectedNodes} nodes)
            </span>
            <button
              onClick={handleInject}
              disabled={injecting}
              className="flex items-center gap-1.5 px-3 py-1 text-xs font-medium bg-accent-2 text-white rounded-lg hover:bg-accent-2/90 disabled:opacity-50"
            >
              {injecting ? (
                <Loader2 className="w-3 h-3 animate-spin" />
              ) : (
                <Plus className="w-3 h-3" />
              )}
              Add to Context
            </button>
          </div>
        </div>
      )}

      {/* Single inject success indicator */}
      {injected && selected.size === 0 && (
        <div className="border-t border-border px-3 py-1.5 bg-green-500/10">
          <div className="flex items-center gap-1.5 text-xs text-green-400">
            <Check className="w-3 h-3" />
            <span>Context injected successfully</span>
          </div>
        </div>
      )}
    </div>
  );
}
