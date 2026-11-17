/**
 * Component tests — ShareDialog
 *
 * Two surfaces are pinned here:
 *
 * A. Invite routing (DF-HERMES-CANOPY-89). The dialog used to build
 *    `{ email, permission, message }` and nothing else, so on a fresh instance
 *    EVERY invite died with 404 INVITEE_NOT_REGISTERED: dev-mode provisioning
 *    creates accounts without an email, and the dialog had no way to name an
 *    invitee by UUID — the one path the backend already supported.
 *
 *      1. a UUID in the field   → the POST body carries `user_id`, never `email`
 *      2. an email in the field → the POST body carries `email`, never `user_id`
 *      3. success               → the server's 201 record reaches `onInvite` and
 *                                 the invite is announced with the identifier the
 *                                 operator actually typed (an email-less invitee
 *                                 must not render "undefined")
 *      4. a coded 404           → the server's message is surfaced verbatim
 *
 * B. The real membership roster (CAN-MEM-03). The member list used to be
 *    seeded from a hard-coded demo roster in TreeView. The dialog now reads
 *    `GET /api/v1/trees/:id/members` (SPEC-API-06 §9) when it opens and
 *    refreshes it after every write:
 *
 *      5. one GET, with the tree id, on open
 *      6. the served roster renders (names + permission badges, no invented email)
 *      7. a failed read renders "Could not load members: <status/reason>" + Retry
 *         — never an empty list, never a demo user
 *      8. Retry issues a fresh read
 *      9. a role change PATCHes the membership and re-reads the roster
 *     10. a remove DELETEs the membership and re-reads the roster
 *     11. a dead API (fetch rejects) still lands on the error state
 *
 * React 19's `act` + `react-dom/client` are used directly, matching
 * `OfflineIndicator.test.tsx`: the project has no @testing-library dependency.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import ShareDialog from '../ShareDialog.tsx';
import type {
  ShareInvitePayload,
  ShareInviteResult,
} from '../../types/multiUser.ts';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

// ─── Fixtures ──────────────────────────────────────────────────────────

const TREE = '019fbf2d-13d9-714b-9077-735abfc6dc00';
/** What dev-mode provisioning mints: a UUID with no email anywhere. */
const PROVISIONED_UUID = '00000000-0000-0000-0000-000000000002';
/** A user that does have an address on record (the seeded dev user's shape). */
const SEEDED_EMAIL = 'dev@canopy.dev';

/** Membership ids as served — the PATCH/DELETE path segment (§9.3 `id`). */
const OWNER_MEMBER_ID = '019fbf2d-13d9-714b-9077-735abfc60001';
const MEMBER_MEMBER_ID = '019fbf2d-13d9-714b-9077-735abfc60002';

const OWNER_USER_ID = '019fbf2d-13d9-714b-9077-735abfc60011';
const MEMBER_USER_ID = '019fbf2d-13d9-714b-9077-735abfc60012';

/**
 * One `GET /members` entry, verbatim from `memberListEntry`
 * (internal/handler/tree_handler.go / SPEC-API-06 §9.3). There is NO email
 * field on this wire shape — the dialog must not invent one.
 */
function wireMember(overrides: {
  id: string;
  user_id: string;
  display_name: string;
  role: string;
  participant_type?: string;
  profile_id?: string | null;
  profile_name?: string | null;
}): Record<string, unknown> {
  return {
    id: overrides.id,
    tree_id: TREE,
    participant_type: overrides.participant_type ?? 'human',
    user_id: overrides.user_id,
    profile_id: overrides.profile_id ?? null,
    display_name: overrides.display_name,
    profile_name: overrides.profile_name ?? null,
    avatar_url: null,
    role: overrides.role,
    is_visible: true,
    auto_approved: false,
    joined_at: '2026-07-20T20:00:00Z',
    last_seen_at: null,
    invited_by: null,
  };
}

const OWNER_ROW = wireMember({
  id: OWNER_MEMBER_ID,
  user_id: OWNER_USER_ID,
  display_name: 'Bane',
  role: 'owner',
});

const MEMBER_ROW = wireMember({
  id: MEMBER_MEMBER_ID,
  user_id: MEMBER_USER_ID,
  display_name: 'Alexis Okuwa',
  role: 'member',
});

function rosterBody(members: Array<Record<string, unknown>>): unknown {
  return { members, total: members.length, limit: 50, offset: 0 };
}

// ─── Fetch harness ─────────────────────────────────────────────────────

interface CapturedCall {
  url: string;
  method: string;
  body: Record<string, unknown>;
}

interface Invited {
  payload: ShareInvitePayload;
  result?: ShareInviteResult;
}

interface Answer {
  status: number;
  body: unknown;
}

let container: HTMLDivElement;
let root: Root;
let calls: CapturedCall[];
/** What the invite POST answers with. */
let inviteAnswer: Answer;
/** Queued roster reads, consumed in order; the last one repeats. */
let rosterAnswers: Answer[];
/** When set, the roster fetch rejects instead of answering (canopyd down). */
let rosterRejects: string | null;

beforeEach(() => {
  calls = [];
  inviteAnswer = {
    status: 201,
    body: {
      treeId: TREE,
      userId: PROVISIONED_UUID,
      permission: 'editor',
      memberId: 'member-1',
    },
  };
  rosterAnswers = [{ status: 200, body: rosterBody([]) }];
  rosterRejects = null;

  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const target = String(url);
      const method = init?.method ?? 'GET';
      calls.push({
        url: target,
        method,
        body: init?.body
          ? (JSON.parse(String(init.body)) as Record<string, unknown>)
          : {},
      });

      // Every membership call (GET/PATCH/DELETE) is answered from the roster
      // queue so a re-read can return a different page than the first read.
      if (target.includes('/members')) {
        if (rosterRejects !== null) throw new Error(rosterRejects);
        const queued =
          rosterAnswers.length > 1
            ? rosterAnswers.shift()!
            : rosterAnswers[0];
        return {
          ok: queued.status >= 200 && queued.status < 300,
          status: queued.status,
          statusText: queued.status === 200 ? 'OK' : 'Service Unavailable',
          json: async () => queued.body,
        } as unknown as Response;
      }

      return {
        ok: inviteAnswer.status >= 200 && inviteAnswer.status < 300,
        status: inviteAnswer.status,
        statusText: inviteAnswer.status === 201 ? 'Created' : '',
        json: async () => inviteAnswer.body,
      } as unknown as Response;
    }),
  );

  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => {
    root.unmount();
  });
  container.remove();
  vi.unstubAllGlobals();
});

// ─── Harness ───────────────────────────────────────────────────────────

function mount(): Invited[] {
  const invited: Invited[] = [];
  act(() => {
    root.render(
      createElement(ShareDialog, {
        open: true,
        onClose: () => {},
        treeId: TREE,
        onInvite: (payload: ShareInvitePayload, result?: ShareInviteResult) => {
          invited.push({ payload, result });
        },
      }),
    );
  });
  return invited;
}

/** Let pending promises (the roster GET) settle inside act. */
async function settle(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

/** Every call that touched a membership route, in order. */
function membershipCalls(): CapturedCall[] {
  return calls.filter((c) => c.url.includes('/members'));
}

function memberGets(): CapturedCall[] {
  return membershipCalls().filter((c) => c.method === 'GET');
}

/** The invite POSTs only — the roster reads are filtered out. */
function inviteCalls(): CapturedCall[] {
  return calls.filter((c) => !c.url.includes('/members'));
}

function inviteInput(): HTMLInputElement {
  const input = container.querySelector<HTMLInputElement>('#share-invite-email');
  if (input === null) throw new Error('invite input not found (#share-invite-email)');
  return input;
}

function sendButton(): HTMLButtonElement {
  const button = Array.from(container.querySelectorAll('button')).find((b) =>
    (b.textContent ?? '').includes('Send Invite'),
  );
  if (button === undefined) throw new Error('Send Invite button not found');
  return button as HTMLButtonElement;
}

function retryButton(): HTMLButtonElement {
  const button = Array.from(container.querySelectorAll('button')).find((b) =>
    (b.textContent ?? '').includes('Retry'),
  );
  if (button === undefined) throw new Error('Retry button not found');
  return button as HTMLButtonElement;
}

/** The permission <select> inside one member row. */
function roleSelect(memberId: string): HTMLSelectElement {
  const row = container.querySelector(`[data-testid="share-member-${memberId}"]`);
  if (row === null) throw new Error(`member row not found: ${memberId}`);
  const select = row.querySelector('select');
  if (select === null) throw new Error(`role select not found in ${memberId}`);
  return select;
}

/** The remove button inside one member row. */
function removeButton(memberId: string): HTMLButtonElement {
  const row = container.querySelector(`[data-testid="share-member-${memberId}"]`);
  if (row === null) throw new Error(`member row not found: ${memberId}`);
  const button = row.querySelector('button[aria-label^="Remove"]');
  if (button === null) throw new Error(`remove button not found in ${memberId}`);
  return button as HTMLButtonElement;
}

/** Type a value the way a browser does — native setter + input event (React 19). */
function type(value: string): void {
  const input = inviteInput();
  const nativeInputValueSetter = Object.getOwnPropertyDescriptor(
    window.HTMLInputElement.prototype,
    'value',
  )!.set!;
  act(() => {
    nativeInputValueSetter.call(input, value);
    input.dispatchEvent(new Event('input', { bubbles: true }));
  });
}

/** Pick a value in a <select> — native setter + change event (React 19). */
function pick(select: HTMLSelectElement, value: string): void {
  const nativeSelectValueSetter = Object.getOwnPropertyDescriptor(
    window.HTMLSelectElement.prototype,
    'value',
  )!.set!;
  act(() => {
    nativeSelectValueSetter.call(select, value);
    select.dispatchEvent(new Event('change', { bubbles: true }));
  });
}

/** Type a value, click Send Invite, and let the fetch + response settle. */
async function send(value: string): Promise<void> {
  type(value);
  await act(async () => {
    sendButton().click();
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

// ═══ A. Invite routing (DF-HERMES-CANOPY-89) ═══════════════════════════

describe('ShareDialog invite routing (DF-HERMES-CANOPY-89)', () => {
  it('sends user_id — never email — when the input is a user UUID', async () => {
    const invited = mount();
    await send(PROVISIONED_UUID);

    expect(inviteCalls()).toHaveLength(1);
    expect(inviteCalls()[0].url).toContain(`/trees/${TREE}/share`);
    expect(inviteCalls()[0].method).toBe('POST');
    expect(inviteCalls()[0].body).toEqual({
      user_id: PROVISIONED_UUID,
      permission: 'editor',
    });
    expect('email' in inviteCalls()[0].body).toBe(false);

    // The 201 record is handed to the parent, tagged as the user_id path.
    expect(invited).toHaveLength(1);
    expect(invited[0].payload.user_id).toBe(PROVISIONED_UUID);
    expect(invited[0].payload.email).toBeUndefined();
    expect(invited[0].result?.userId).toBe(PROVISIONED_UUID);

    // An email-less invitee must not render "undefined" — the identifier the
    // operator typed is what the confirmation names.
    expect(container.textContent).toContain('Invitation sent to');
    expect(container.textContent).toContain(PROVISIONED_UUID);
    expect(inviteInput().value).toBe('');
  });

  it('accepts an upper-case UUID as the user_id path', async () => {
    mount();
    await send(PROVISIONED_UUID.toUpperCase());

    expect(inviteCalls()[0].body).toEqual({
      user_id: PROVISIONED_UUID.toUpperCase(),
      permission: 'editor',
    });
    expect('email' in inviteCalls()[0].body).toBe(false);
  });

  it('keeps the email path for an address, and echoes the server email', async () => {
    inviteAnswer = {
      status: 201,
      body: {
        treeId: TREE,
        userId: '019fbf2d-13d9-714b-9077-735abfc6dc11',
        email: SEEDED_EMAIL,
        permission: 'editor',
        memberId: 'member-2',
      },
    };
    const invited = mount();
    await send(SEEDED_EMAIL);

    expect(inviteCalls()[0].body).toEqual({ email: SEEDED_EMAIL, permission: 'editor' });
    expect('user_id' in inviteCalls()[0].body).toBe(false);
    expect(invited[0].payload.email).toBe(SEEDED_EMAIL);
    expect(invited[0].result?.email).toBe(SEEDED_EMAIL);
    expect(container.textContent).toContain(`Invitation sent to ${SEEDED_EMAIL}`);
  });

  it('surfaces the server’s coded error and does not report an invite', async () => {
    const message =
      'no account is registered for "lexi.dogfood@example.com" on this Canopy ' +
      'instance; the invitee must sign in to the instance once before they can ' +
      'be shared with; that account is created without an email, so share by the ' +
      "account's user UUID instead";
    inviteAnswer = {
      status: 404,
      body: { error: { code: 'INVITEE_NOT_REGISTERED', message } },
    };
    const invited = mount();
    await send('lexi.dogfood@example.com');

    expect(container.textContent).toContain('no account is registered');
    expect(container.textContent).toContain('user UUID');
    expect(invited).toHaveLength(0);
  });

  it('disables Send Invite until something is typed', () => {
    mount();
    expect(sendButton().disabled).toBe(true);
    type(PROVISIONED_UUID);
    expect(sendButton().disabled).toBe(false);
  });
});

// ═══ B. Real membership roster (CAN-MEM-03) ════════════════════════════

describe('ShareDialog membership roster (CAN-MEM-03)', () => {
  it('reads GET /trees/:id/members exactly once with the tree id on open', async () => {
    mount();
    await settle();

    const gets = memberGets();
    expect(gets).toHaveLength(1);
    expect(gets[0].url).toBe(`/api/v1/trees/${TREE}/members`);
    expect(gets[0].method).toBe('GET');
  });

  it('renders the roster the API returned, with served names and roles', async () => {
    rosterAnswers = [
      { status: 200, body: rosterBody([OWNER_ROW, MEMBER_ROW]) },
    ];

    mount();
    await settle();

    const text = container.textContent ?? '';
    expect(text).toContain('Members (2)');
    expect(text).toContain('Bane');
    expect(text).toContain('Alexis Okuwa');
    // The owner row is read-only: ownership is not transferable through the
    // members routes (SPEC-API-06 §10.2 rejects `owner` as a PATCH role).
    expect(text).toContain('Owner');
    // A `member` role is the dialog's `editor` — the select value proves the
    // mapping (the option list itself is static, so its text proves nothing).
    expect(roleSelect(MEMBER_MEMBER_ID).value).toBe('editor');
    // No demo/placeholder identity survives a real read.
    expect(text).not.toContain('Demo User');
    expect(text).not.toContain('demo@example.com');
    // The wire carries no email, so none may be invented for a row.
    expect(text).not.toContain('@example.com');
  });

  it('renders "Could not load members" with a retry when the read fails', async () => {
    rosterAnswers = [
      {
        status: 503,
        body: {
          error: { code: 'SERVICE_UNAVAILABLE', message: 'database unavailable' },
        },
      },
    ];

    mount();
    await settle();

    const error = container.querySelector('[data-testid="share-members-error"]');
    expect(error).not.toBeNull();
    expect(container.textContent).toContain(
      'Could not load members: HTTP 503 — SERVICE_UNAVAILABLE: database unavailable',
    );
    // An error is not an empty list: the "no members" copy must not appear,
    // and no placeholder member may render.
    expect(container.textContent).not.toContain('No members yet');
    expect(container.querySelectorAll('[data-testid^="share-member-"]')).toHaveLength(0);
    expect(container.textContent).not.toContain('Demo User');
    // The retry affordance exists.
    expect(retryButton().textContent).toContain('Retry');
  });

  it('retries the read and renders the roster once the API answers', async () => {
    rosterAnswers = [
      {
        status: 503,
        body: { error: { code: 'SERVICE_UNAVAILABLE', message: 'database unavailable' } },
      },
      { status: 200, body: rosterBody([OWNER_ROW, MEMBER_ROW]) },
    ];

    mount();
    await settle();
    expect(container.textContent).toContain('Could not load members');

    await act(async () => {
      retryButton().click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(memberGets()).toHaveLength(2);
    expect(container.textContent).toContain('Alexis Okuwa');
    expect(container.querySelector('[data-testid="share-members-error"]')).toBeNull();
  });

  it('PATCHes a role change against the membership id and re-reads the roster', async () => {
    rosterAnswers = [
      { status: 200, body: rosterBody([OWNER_ROW, MEMBER_ROW]) },
      // The re-read after the write: the server now stores `admin`.
      {
        status: 200,
        body: rosterBody([
          OWNER_ROW,
          { ...MEMBER_ROW, role: 'admin' },
        ]),
      },
    ];

    mount();
    await settle();
    expect(roleSelect(MEMBER_MEMBER_ID).value).toBe('editor');

    await act(async () => {
      pick(roleSelect(MEMBER_MEMBER_ID), 'admin');
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    const patch = membershipCalls().find((c) => c.method === 'PATCH');
    expect(patch).toBeDefined();
    expect(patch!.url).toBe(`/api/v1/trees/${TREE}/members/${MEMBER_MEMBER_ID}`);
    // `editor` is the dialog's name for the DB `member` role (§10.2): the
    // request carries the DB spelling, `admin` passes through unchanged.
    expect(patch!.body).toEqual({ role: 'admin' });

    // Refreshed from the API, not mutated optimistically.
    expect(memberGets()).toHaveLength(2);
    expect(roleSelect(MEMBER_MEMBER_ID).value).toBe('admin');
  });

  it('DELETEs a removed membership and re-reads the roster', async () => {
    rosterAnswers = [
      { status: 200, body: rosterBody([OWNER_ROW, MEMBER_ROW]) },
      // The re-read after the delete: the row is gone.
      { status: 200, body: rosterBody([OWNER_ROW]) },
    ];

    mount();
    await settle();
    expect(container.textContent).toContain('Alexis Okuwa');

    await act(async () => {
      removeButton(MEMBER_MEMBER_ID).click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    const del = membershipCalls().find((c) => c.method === 'DELETE');
    expect(del).toBeDefined();
    expect(del!.url).toBe(`/api/v1/trees/${TREE}/members/${MEMBER_MEMBER_ID}`);

    expect(memberGets()).toHaveLength(2);
    expect(container.textContent).not.toContain('Alexis Okuwa');
    expect(container.textContent).toContain('Members (1)');
  });

  it('shows the error state when the API is unreachable (fetch rejects)', async () => {
    rosterRejects = 'Failed to fetch';

    mount();
    await settle();

    expect(container.textContent).toContain('Could not load members: Failed to fetch');
    expect(container.textContent).not.toContain('Demo User');
    expect(container.textContent).not.toContain('No members yet');
  });
});
