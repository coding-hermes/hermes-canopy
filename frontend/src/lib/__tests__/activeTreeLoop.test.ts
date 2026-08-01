/**
 * Regression test — the topics-rail refresh loop (UI-02)
 *
 * `storeTreeId` notifies same-tab listeners so the rail and the Topics
 * page stay in sync. The rail's listener responds by re-fetching, and the
 * fetch re-stores the tree id it just resolved. If the store dispatched
 * unconditionally, that write would re-enter the listener and spin an
 * unbounded fetch loop — observed as a hard renderer crash ("Target
 * crashed") during UI-02 screenshot capture.
 *
 * The contract these tests pin: a store that does not change the value is
 * silent.
 */

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { ACTIVE_TREE_STORAGE_KEY, storeTreeId } from '../activeTree';

const TREE_A = '6d94185a-e3af-4a2b-a6fe-efe9b67e4c38';
const TREE_B = 'b1655761-2d7f-4b3c-85d5-21396da15691';

describe('activeTree — feedback-loop guard', () => {
  beforeEach(() => {
    window.localStorage.clear();
  });

  it('does not re-notify when storing the value already held', () => {
    storeTreeId(TREE_A);

    const listener = vi.fn();
    window.addEventListener(ACTIVE_TREE_STORAGE_KEY, listener);
    storeTreeId(TREE_A); // same value — must be silent
    storeTreeId(TREE_A);
    storeTreeId(TREE_A);
    window.removeEventListener(ACTIVE_TREE_STORAGE_KEY, listener);

    expect(listener).not.toHaveBeenCalled();
  });

  it('still notifies when the value genuinely changes', () => {
    storeTreeId(TREE_A);

    const listener = vi.fn();
    window.addEventListener(ACTIVE_TREE_STORAGE_KEY, listener);
    storeTreeId(TREE_B);
    window.removeEventListener(ACTIVE_TREE_STORAGE_KEY, listener);

    expect(listener).toHaveBeenCalledTimes(1);
  });

  it('terminates when a listener re-stores the same id (the crash shape)', () => {
    storeTreeId(TREE_A);

    let depth = 0;
    let maxDepth = 0;
    const listener = () => {
      depth += 1;
      maxDepth = Math.max(maxDepth, depth);
      // Simulates the rail: on notify, re-fetch and re-store the resolved
      // tree. Without the equality guard this recurses until the stack or
      // the renderer dies.
      if (depth < 50) storeTreeId(TREE_A);
      depth -= 1;
    };

    window.addEventListener(ACTIVE_TREE_STORAGE_KEY, listener);
    expect(() => storeTreeId(TREE_B)).not.toThrow();
    window.removeEventListener(ACTIVE_TREE_STORAGE_KEY, listener);

    // One notification for the real B→A-change, and no runaway recursion.
    expect(maxDepth).toBeLessThanOrEqual(2);
  });
});
