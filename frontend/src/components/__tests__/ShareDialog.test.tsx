/**
 * Component tests — ShareDialog invite flow (DF-HERMES-CANOPY-89)
 *
 * The dialog used to build `{ email, permission, message }` and nothing else, so
 * on a fresh instance EVERY invite died with 404 INVITEE_NOT_REGISTERED: dev-mode
 * provisioning creates accounts without an email, and the dialog had no way to
 * name an invitee by UUID — the one path the backend already supported.
 *
 * These tests drive the real component and the real fetch it issues (stubbed, so
 * no network) and pin both halves of the fix:
 *
 *   1. a UUID in the field   → the POST body carries `user_id`, never `email`
 *   2. an email in the field → the POST body carries `email`, never `user_id`
 *   3. success               → the server's 201 record reaches `onInvite` and the
 *                              invite is announced with the identifier the
 *                              operator actually typed (an email-less invitee
 *                              must not render "undefined")
 *   4. a coded 404           → the server's message is surfaced verbatim
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

interface CapturedCall {
  url: string;
  method: string | undefined;
  body: Record<string, unknown>;
}

interface Invited {
  payload: ShareInvitePayload;
  result?: ShareInviteResult;
}

let container: HTMLDivElement;
let root: Root;
let calls: CapturedCall[];
let answer: { status: number; body: unknown };

beforeEach(() => {
  calls = [];
  answer = {
    status: 201,
    body: {
      treeId: TREE,
      userId: PROVISIONED_UUID,
      permission: 'editor',
      memberId: 'member-1',
    },
  };
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit) => {
      calls.push({
        url: String(url),
        method: init?.method,
        body: JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>,
      });
      return {
        ok: answer.status >= 200 && answer.status < 300,
        status: answer.status,
        json: async () => answer.body,
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
        members: [],
        onPermissionChange: () => {},
        onRemoveMember: () => {},
        onInvite: (payload: ShareInvitePayload, result?: ShareInviteResult) => {
          invited.push({ payload, result });
        },
      }),
    );
  });
  return invited;
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

/** Type a value, click Send Invite, and let the fetch + response settle. */
async function send(value: string): Promise<void> {
  type(value);
  await act(async () => {
    sendButton().click();
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

// ─── Tests ─────────────────────────────────────────────────────────────

describe('ShareDialog invite routing (DF-HERMES-CANOPY-89)', () => {
  it('sends user_id — never email — when the input is a user UUID', async () => {
    const invited = mount();
    await send(PROVISIONED_UUID);

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toContain(`/trees/${TREE}/share`);
    expect(calls[0].method).toBe('POST');
    expect(calls[0].body).toEqual({
      user_id: PROVISIONED_UUID,
      permission: 'editor',
    });
    expect('email' in calls[0].body).toBe(false);

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

    expect(calls[0].body).toEqual({
      user_id: PROVISIONED_UUID.toUpperCase(),
      permission: 'editor',
    });
    expect('email' in calls[0].body).toBe(false);
  });

  it('keeps the email path for an address, and echoes the server email', async () => {
    answer = {
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

    expect(calls[0].body).toEqual({ email: SEEDED_EMAIL, permission: 'editor' });
    expect('user_id' in calls[0].body).toBe(false);
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
    answer = {
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
