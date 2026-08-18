/**
 * Integration test — Fork/Branch UI affordance (GAP-043 proof)
 *
 * The P2 gap: 'Branch from any message' (MVP promise) had NO UI
 * affordance — the tree-scoped fork route existed but only the
 * undocumented API could reach it. This exercises the REAL path:
 *
 *   Node card ··· menu → Branch → composer dialog
 *     → POST /trees/{id}/nodes/{id}/fork → list refresh
 *
 * plus the leaf-fork rule: forking a message with no replies is rejected
 * by the service (deliberate guard, SPEC-API-03 §7.3) and the dialog must
 * show the guidance inline and STAY OPEN.
 *
 * No mocks. One Playwright browser context. BASE_URL = http://localhost:5173
 * (Vite dev proxy injects the dev JWT, so plain page loads + fetch() calls
 * are authenticated).
 */
import { describe, it, expect, beforeAll, afterAll } from 'vitest';
import { chromium, type Browser, type Page } from '@playwright/test';
import { BASE_URL, isServerRunning } from './setup';

/** The dialog's inline guidance for the deliberate leaf-fork guard. */
const LEAF_GUIDANCE = 'Branching needs at least one reply on this message';

describe('Fork/Branch UI (GAP-043)', () => {
  let browser: Browser;
  let page: Page;
  let serverAvailable = false;

  // Fixture tree, created once in beforeAll and shared across cases:
  // a root message and ONE reply to it. The reply stays a LEAF (no
  // children) — exactly the case the leaf-fork guard protects.
  let treeId = '';
  let rootId = '';
  let replyId = '';
  let rootContent = '';
  let replyContent = '';

  async function openBranchDialog(cardText: string) {
    const card = page.locator('[data-testid="node-card"]', { hasText: cardText });
    await card.waitFor({ state: 'visible', timeout: 15_000 });
    await card.hover();
    await card.locator('[data-testid="node-card-menu-trigger"]').click();
    const menu = page.locator('[data-testid="node-card-menu"]');
    await menu.waitFor({ state: 'visible', timeout: 5_000 });
    await menu.locator('[data-testid="node-card-menu-branch"]').click();
    const textarea = page.locator('#branch-content');
    await textarea.waitFor({ state: 'visible', timeout: 5_000 });
    return textarea;
  }

  beforeAll(async () => {
    serverAvailable = await isServerRunning();
    if (!serverAvailable) return;

    browser = await chromium.launch({ headless: true });
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    page = await ctx.newPage();

    // ── Fixture: fresh, uniquely-named tree with root + one reply. ──
    const suffix = `${Date.now()}_${Math.random().toString(36).slice(2, 8)}`;
    rootContent = `GAP043 root ${suffix}`;
    replyContent = `GAP043 reply ${suffix}`;

    const api = page.request;
    const createResp = await api.post(`${BASE_URL}/api/v1/trees`, {
      headers: { 'Content-Type': 'application/json' },
      data: {
        title: `GAP043 E2E ${suffix}`,
        rootMessage: { content: rootContent, contentFormat: 'markdown', nodeType: 'message' },
      },
    });
    if (!createResp.ok()) {
      throw new Error(`create tree failed: ${createResp.status()} ${await createResp.text()}`);
    }
    const tree = (await createResp.json()) as { id: string; root_node_id?: string };
    treeId = tree.id;
    expect(treeId).toMatch(/^[0-9a-fA-F-]{36}$/);

    // The reply is created through the tree-scoped node route so the
    // fixture never depends on the deprecated flat reply mount.
    const replyResp = await api.post(`${BASE_URL}/api/v1/trees/${treeId}/nodes`, {
      headers: { 'Content-Type': 'application/json' },
      data: {
        parent_id: tree.root_node_id,
        content: replyContent,
        content_format: 'markdown',
        node_type: 'message',
        edge_type: 'reply',
      },
    });
    if (!replyResp.ok()) {
      throw new Error(`create reply failed: ${replyResp.status()} ${await replyResp.text()}`);
    }
    const reply = (await replyResp.json()) as { node: { id: string } };
    replyId = reply.node.id;
    rootId = tree.root_node_id ?? '';
    if (!rootId) {
      // Fallback: derive the root from the node list (parentId null).
      const list = (await (
        await api.get(`${BASE_URL}/api/v1/trees/${treeId}/nodes`)
      ).json()) as { nodes: Array<{ id: string; parentId: string | null }> };
      rootId = list.nodes.find((n) => n.parentId === null)?.id ?? '';
    }
    expect(rootId).toMatch(/^[0-9a-fA-F-]{36}$/);
    expect(replyId).toMatch(/^[0-9a-fA-F-]{36}$/);
  }, 30_000);

  afterAll(async () => {
    if (!serverAvailable) return;
    await page?.context()?.close();
    await browser?.close();
  });

  it(
    'node card overflow menu shows the Branch item',
    async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      await page.goto(`${BASE_URL}/nodes?tree=${treeId}`, {
        waitUntil: 'domcontentloaded',
        timeout: 15_000,
      });

      const card = page.locator('[data-testid="node-card"]', { hasText: rootContent });
      await card.waitFor({ state: 'visible', timeout: 15_000 });
      await card.hover();
      await card.locator('[data-testid="node-card-menu-trigger"]').click();

      const menu = page.locator('[data-testid="node-card-menu"]');
      await menu.waitFor({ state: 'visible', timeout: 5_000 });
      const branchItem = menu.locator('[data-testid="node-card-menu-branch"]');
      await branchItem.waitFor({ state: 'visible', timeout: 5_000 });
      expect((await branchItem.innerText()).trim()).toBe('Branch');

      // Keyboard accessibility: the new item participates in the roving
      // focus (Edit → Branch → Delete).
      expect(await menu.locator('[role="menuitem"]').count()).toBe(3);
      await page.keyboard.press('ArrowDown');
      expect(
        await branchItem.evaluate((el) => document.activeElement === el),
      ).toBe(true);
      await page.keyboard.press('Escape');
    },
    60_000,
  );

  it(
    'clicking Branch opens the composer dialog with focus in the textarea',
    async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      await page.goto(`${BASE_URL}/nodes?tree=${treeId}`, {
        waitUntil: 'domcontentloaded',
        timeout: 15_000,
      });

      const textarea = await openBranchDialog(rootContent);

      // Composer shape: pre-labeled textarea, Branch + Cancel buttons,
      // focus lands in the textarea.
      expect(
        await textarea.evaluate((el) => document.activeElement === el),
      ).toBe(true);
      expect(
        await page.locator('label[for="branch-content"]').innerText(),
      ).toContain('Branch from');
      expect((await page.locator('[data-testid="branch-submit"]').innerText()).trim()).toBe(
        'Branch',
      );
      await page.locator('button', { hasText: 'Cancel' }).waitFor({
        state: 'visible',
        timeout: 5_000,
      });

      // Escape closes the dialog.
      await page.keyboard.press('Escape');
      await textarea.waitFor({ state: 'detached', timeout: 5_000 });
    },
    60_000,
  );

  it(
    'submitting a branch POSTs to the tree-scoped fork route, closes the dialog and refreshes the list',
    async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      await page.goto(`${BASE_URL}/nodes?tree=${treeId}`, {
        waitUntil: 'domcontentloaded',
        timeout: 15_000,
      });

      const textarea = await openBranchDialog(rootContent);
      const branchContent = `GAP043 branch ${Date.now()}`;

      // Count hits against the REAL tree-scoped fork route.
      let forkCalls = 0;
      const onRequest = (req: { url: () => string; method: () => string }) => {
        if (
          req.method() === 'POST' &&
          req.url().endsWith(`/api/v1/trees/${treeId}/nodes/${rootId}/fork`)
        ) {
          forkCalls += 1;
        }
      };
      page.on('request', onRequest);
      try {
        await textarea.fill(branchContent);
        await page.locator('[data-testid="branch-submit"]').click();

        // Success: the dialog closes and the list refresh surfaces the
        // new branch node — no reload, same page.
        await textarea.waitFor({ state: 'detached', timeout: 15_000 });
        await page
          .locator('[data-testid="node-card"]', { hasText: branchContent })
          .waitFor({ state: 'visible', timeout: 15_000 });
        expect(forkCalls).toBe(1);
      } finally {
        page.off('request', onRequest);
      }
    },
    60_000,
  );

  it(
    'forking a leaf keeps the dialog open and shows the leaf-fork guidance inline',
    async () => {
      if (!serverAvailable) {
        console.warn('⚠ Dev server not running — skipping integration test');
        return;
      }

      await page.goto(`${BASE_URL}/nodes?tree=${treeId}`, {
        waitUntil: 'domcontentloaded',
        timeout: 15_000,
      });

      // The REPLY is still a leaf (the previous case forked the ROOT),
      // so forking it must be rejected by the deliberate service guard.
      const textarea = await openBranchDialog(replyContent);
      await textarea.fill(`GAP043 leaf branch ${Date.now()}`);
      await page.locator('[data-testid="branch-submit"]').click();

      const errorBox = page.locator('[data-testid="branch-error"]');
      await errorBox.waitFor({ state: 'visible', timeout: 15_000 });
      expect(await errorBox.innerText()).toContain(LEAF_GUIDANCE);

      // The dialog must stay open so the user can cancel or retry.
      await textarea.waitFor({ state: 'visible', timeout: 5_000 });
      await page.locator('[data-testid="branch-submit"]').waitFor({
        state: 'visible',
        timeout: 5_000,
      });
    },
    60_000,
  );
});
