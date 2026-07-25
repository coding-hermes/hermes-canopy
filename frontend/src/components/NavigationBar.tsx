/**
 * Hermes Canopy — Navigation Bar
 *
 * Provides search and breadcrumb navigation for the tree canvas.
 *   - Fuzzy search bar that filters nodes by title/content
 *   - Search results dropdown with jump-to on select
 *   - Breadcrumb trail from root to the currently selected node
 */

import { useState, useCallback, useMemo, useRef, useEffect } from 'react';
import type { Node, Edge } from '@xyflow/react';
import type { TreeNodeCardData } from '../types/tree.ts';

// ─── Props ─────────────────────────────────────────────────────────────

export interface NavigationBarProps {
  nodes: Node<TreeNodeCardData>[];
  edges: Edge[];
  selectedNodeId: string | null;
  onNavigateToNode: (nodeId: string) => void;
}

// ─── Fuzzy matching ────────────────────────────────────────────────────

/**
 * Simple fuzzy match: checks if all characters of `needle` appear in order
 * within `haystack` (case-insensitive).
 * Returns match score (0–1) or 0 if no match.
 */
function fuzzyScore(needle: string, haystack: string): number {
  const n = needle.toLowerCase();
  const h = haystack.toLowerCase();
  let ni = 0;
  let consecutive = 0;
  let maxConsecutive = 0;

  for (let hi = 0; hi < h.length && ni < n.length; hi++) {
    if (h[hi] === n[ni]) {
      consecutive++;
      maxConsecutive = Math.max(maxConsecutive, consecutive);
      ni++;
    } else {
      consecutive = 0;
    }
  }

  if (ni < n.length) return 0; // not all chars matched

  // Score based on how well it matched:
  // - Higher score for consecutive matches
  // - Higher score for shorter haystack (closer to needle length)
  const consecBonus = maxConsecutive / n.length;
  const lengthRatio = n.length / h.length;
  return (consecBonus * 0.6 + lengthRatio * 0.4);
}

// ─── Breadcrumb helpers ────────────────────────────────────────────────

interface BreadcrumbItem {
  id: string;
  label: string;
}

/**
 * Build breadcrumb path from root to the given node.
 * Walks parent pointers up from the node to find the path.
 */
function buildBreadcrumbs(
  nodeId: string,
  nodes: Node<TreeNodeCardData>[],
  edges: Edge[],
): BreadcrumbItem[] {
  // Build parent map: targetId → sourceId
  const parentMap = new Map<string, string>();
  for (const edge of edges) {
    parentMap.set(edge.target, edge.source);
  }

  // Build node label map
  const labelMap = new Map<string, string>();
  for (const node of nodes) {
    labelMap.set(
      node.id,
      node.data.label || node.data.content?.slice(0, 50) || 'Untitled',
    );
  }

  // Walk up from the node to roots
  const path: BreadcrumbItem[] = [];
  const visited = new Set<string>();
  let current: string | undefined = nodeId;

  while (current && !visited.has(current)) {
    visited.add(current);
    path.unshift({
      id: current,
      label: labelMap.get(current) ?? current.slice(0, 8),
    });
    current = parentMap.get(current);
  }

  return path;
}

// ─── Component ─────────────────────────────────────────────────────────

export default function NavigationBar({
  nodes,
  edges,
  selectedNodeId,
  onNavigateToNode,
}: NavigationBarProps) {
  const [searchQuery, setSearchQuery] = useState('');
  const [showResults, setShowResults] = useState(false);
  const [selectedIndex, setSelectedIndex] = useState(0);
  const searchInputRef = useRef<HTMLInputElement>(null);
  const resultsRef = useRef<HTMLDivElement>(null);

  // ─── Search results ───────────────────────────────────────────────

  const searchResults = useMemo(() => {
    if (!searchQuery.trim()) return [];

    const query = searchQuery.trim();
    const scored = nodes
      .map((node) => {
        const title = node.data.label || '';
        const content = node.data.content || '';

        // Score against both title and content, use the better one
        const titleScore = fuzzyScore(query, title);
        const contentScore = fuzzyScore(query, content);
        const score = Math.max(titleScore * 1.2, contentScore); // title matches preferred

        return { node, score };
      })
      .filter((r) => r.score > 0)
      .sort((a, b) => b.score - a.score)
      .slice(0, 20);

    return scored;
  }, [nodes, searchQuery]);

  // Reset selection index when results change
  useEffect(() => {
    setSelectedIndex(0);
  }, [searchResults.length]);

  // ─── Breadcrumbs ──────────────────────────────────────────────────

  const breadcrumbs = useMemo(() => {
    if (!selectedNodeId) return [];
    return buildBreadcrumbs(selectedNodeId, nodes, edges);
  }, [selectedNodeId, nodes, edges]);

  // ─── Handlers ─────────────────────────────────────────────────────

  const handleSearchChange = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      setSearchQuery(e.target.value);
      setShowResults(true);
    },
    [],
  );

  const handleSelectResult = useCallback(
    (nodeId: string) => {
      onNavigateToNode(nodeId);
      setShowResults(false);
      setSearchQuery('');
    },
    [onNavigateToNode],
  );

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (!showResults || searchResults.length === 0) return;

      switch (e.key) {
        case 'ArrowDown':
          e.preventDefault();
          setSelectedIndex((prev) =>
            prev < searchResults.length - 1 ? prev + 1 : 0,
          );
          break;
        case 'ArrowUp':
          e.preventDefault();
          setSelectedIndex((prev) =>
            prev > 0 ? prev - 1 : searchResults.length - 1,
          );
          break;
        case 'Enter':
          e.preventDefault();
          if (searchResults[selectedIndex]) {
            handleSelectResult(searchResults[selectedIndex].node.id);
          }
          break;
        case 'Escape':
          setShowResults(false);
          searchInputRef.current?.blur();
          break;
      }
    },
    [showResults, searchResults, selectedIndex, handleSelectResult],
  );

  const handleFocus = useCallback(() => {
    if (searchQuery.trim()) {
      setShowResults(true);
    }
  }, [searchQuery]);

  // Close results on outside click
  useEffect(() => {
    function handleClickOutside(e: MouseEvent) {
      const target = e.target as HTMLElement;
      if (
        resultsRef.current &&
        !resultsRef.current.contains(target) &&
        searchInputRef.current &&
        !searchInputRef.current.contains(target)
      ) {
        setShowResults(false);
      }
    }

    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, []);

  // ─── Render ───────────────────────────────────────────────────────

  return (
    <div
      className="flex items-center gap-3 px-4 py-2"
      style={{ backgroundColor: '#0f0f1a', borderBottom: '1px solid #2d2d4a' }}
    >
      {/* Search */}
      <div className="relative flex-1 max-w-md">
        <div className="relative">
          <svg
            className="absolute left-2.5 top-1/2 -translate-y-1/2 w-4 h-4"
            style={{ color: '#94a3b8' }}
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"
            />
          </svg>
          <input
            ref={searchInputRef}
            type="text"
            placeholder="Search nodes..."
            value={searchQuery}
            onChange={handleSearchChange}
            onKeyDown={handleKeyDown}
            onFocus={handleFocus}
            className="w-full pl-9 pr-3 py-1.5 text-sm rounded-md border outline-none transition-colors focus:ring-1 focus:ring-[#7c3aed]"
            style={{
              backgroundColor: '#1a1a2e',
              borderColor: '#2d2d4a',
              color: '#e2e8f0',
            }}
          />
        </div>

        {/* Search results dropdown */}
        {showResults && searchResults.length > 0 && (
          <div
            ref={resultsRef}
            className="absolute top-full mt-1 w-full rounded-md border shadow-xl z-50 max-h-64 overflow-y-auto"
            style={{
              backgroundColor: '#1a1a2e',
              borderColor: '#2d2d4a',
            }}
          >
            {searchResults.map((result, index) => (
              <button
                key={result.node.id}
                onClick={() => handleSelectResult(result.node.id)}
                className="w-full text-left px-3 py-2 text-sm transition-colors flex items-center gap-2"
                style={{
                  backgroundColor:
                    index === selectedIndex ? '#7c3aed22' : 'transparent',
                  color: '#e2e8f0',
                  borderBottom: '1px solid #2d2d4a',
                }}
                onMouseEnter={() => setSelectedIndex(index)}
              >
                <span
                  className="flex-shrink-0 w-5 h-5 rounded text-[10px] flex items-center justify-center font-bold"
                  style={{
                    backgroundColor: '#2d2d4a',
                    color:
                      result.node.data.isAgent
                        ? '#7c3aed'
                        : result.node.data.nodeType === 'synthesis'
                          ? '#f59e0b'
                          : '#22c55e',
                  }}
                >
                  {result.node.data.nodeType === 'synthesis'
                    ? 'S'
                    : result.node.data.nodeType === 'card'
                      ? 'C'
                      : result.node.data.nodeType === 'topic'
                        ? 'T'
                        : result.node.data.isAgent
                          ? 'A'
                          : 'M'}
                </span>
                <span className="truncate">
                  {result.node.data.label || result.node.data.content?.slice(0, 60)}
                </span>
                <span className="ml-auto text-xs flex-shrink-0" style={{ color: '#94a3b8' }}>
                  {Math.round(result.score * 100)}%
                </span>
              </button>
            ))}
          </div>
        )}

        {/* No results */}
        {showResults && searchQuery.trim() && searchResults.length === 0 && (
          <div
            className="absolute top-full mt-1 w-full rounded-md border shadow-xl z-50 px-3 py-4 text-center text-sm"
            style={{
              backgroundColor: '#1a1a2e',
              borderColor: '#2d2d4a',
              color: '#94a3b8',
            }}
          >
            No nodes match "{searchQuery}"
          </div>
        )}
      </div>

      {/* Breadcrumbs */}
      <div className="flex items-center gap-1 flex-1 overflow-x-auto text-sm">
        {breadcrumbs.length > 0 ? (
          breadcrumbs.map((crumb, index) => (
            <div key={crumb.id} className="flex items-center gap-1 flex-shrink-0">
              {index > 0 && (
                <svg
                  className="w-3 h-3 flex-shrink-0"
                  style={{ color: '#4a4a6a' }}
                  fill="none"
                  stroke="currentColor"
                  viewBox="0 0 24 24"
                >
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth={2}
                    d="M9 5l7 7-7 7"
                  />
                </svg>
              )}
              <button
                onClick={() => onNavigateToNode(crumb.id)}
                className="px-2 py-0.5 rounded transition-colors truncate max-w-[200px] hover:underline"
                style={{
                  color: index === breadcrumbs.length - 1 ? '#e2e8f0' : '#94a3b8',
                  backgroundColor:
                    index === breadcrumbs.length - 1 ? '#7c3aed22' : 'transparent',
                  fontWeight: index === breadcrumbs.length - 1 ? 600 : 400,
                }}
                title={crumb.label}
              >
                {crumb.label}
              </button>
            </div>
          ))
        ) : (
          <span style={{ color: '#4a4a6a' }} className="text-sm">
            Select a node to see its path
          </span>
        )}
      </div>

      {/* Keyboard shortcuts hint */}
      <div className="flex items-center gap-2 flex-shrink-0">
        <span style={{ color: '#4a4a6a' }} className="text-[11px] hidden lg:inline">
          <kbd
            className="px-1 py-0.5 rounded text-[10px] font-mono"
            style={{ backgroundColor: '#1a1a2e', border: '1px solid #2d2d4a' }}
          >
            ⌘0
          </kbd>{' '}
          fit ·{' '}
          <kbd
            className="px-1 py-0.5 rounded text-[10px] font-mono"
            style={{ backgroundColor: '#1a1a2e', border: '1px solid #2d2d4a' }}
          >
            ⌘+/−
          </kbd>{' '}
          zoom
        </span>
      </div>
    </div>
  );
}
