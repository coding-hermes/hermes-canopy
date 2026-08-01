/**
 * Unit tests — active-tree persistence (UI-02 topics rail)
 *
 * The rail and the Topics page coordinate through this module: the tree
 * id is persisted so the rail survives route changes, and a same-tab DOM
 * event lets one surface refresh the other without prop-drilling through
 * the router `Outlet`.
 */

import { describe, it, expect, beforeEach, vi } from 'vitest';
import {
  ACTIVE_TREE_STORAGE_KEY,
  readStoredTreeId,
  storeTreeId,
  notifyTopicsChanged,
} from '../activeTree';

const TREE_A = '6d94185a-e3af-4a2b-a6fe-efe9b67e4c38';
const TREE_B = 'b1655761-2d7f-4b3c-85d5-21396da15691';

describe('activeTree', () => {
  beforeEach(() => {
    window.localStorage.clear();
  });

  it('returns an empty string when nothing is stored', () => {
    expect(readStoredTreeId()).toBe('');
  });

  it('round-trips a stored tree id', () => {
    storeTreeId(TREE_A);
    expect(readStoredTreeId()).toBe(TREE_A);

    storeTreeId(TREE_B);
    expect(readStoredTreeId()).toBe(TREE_B);
  });

  it('clears the selection when given an empty string', () => {
    storeTreeId(TREE_A);
    storeTreeId('');
    expect(readStoredTreeId()).toBe('');
    expect(window.localStorage.getItem(ACTIVE_TREE_STORAGE_KEY)).toBeNull();
  });

  it('notifies same-tab listeners on store', () => {
    const listener = vi.fn();
    window.addEventListener(ACTIVE_TREE_STORAGE_KEY, listener);
    storeTreeId(TREE_A);
    window.removeEventListener(ACTIVE_TREE_STORAGE_KEY, listener);

    expect(listener).toHaveBeenCalledTimes(1);
  });

  it('notifies listeners on a topic-set change without touching storage', () => {
    storeTreeId(TREE_A);
    const listener = vi.fn();
    window.addEventListener(ACTIVE_TREE_STORAGE_KEY, listener);
    notifyTopicsChanged();
    window.removeEventListener(ACTIVE_TREE_STORAGE_KEY, listener);

    expect(listener).toHaveBeenCalledTimes(1);
    expect(readStoredTreeId()).toBe(TREE_A);
  });

  it('degrades gracefully when localStorage throws (private mode)', () => {
    const getItem = vi
      .spyOn(Storage.prototype, 'getItem')
      .mockImplementation(() => {
        throw new Error('SecurityError');
      });
    const setItem = vi
      .spyOn(Storage.prototype, 'setItem')
      .mockImplementation(() => {
        throw new Error('QuotaExceededError');
      });

    expect(readStoredTreeId()).toBe('');
    expect(() => storeTreeId(TREE_A)).not.toThrow();

    getItem.mockRestore();
    setItem.mockRestore();
  });
});
