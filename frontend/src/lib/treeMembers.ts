/**
 * Hermes Canopy — Tree membership roster (real API source)
 *
 * The Share dialog's member list used to be seeded from a hard-coded demo
 * roster in TreeView. This module is the real source of truth:
 *
 *   GET    /api/v1/trees/{tree_id}/members                  (SPEC-API-06 §9)
 *   PATCH  /api/v1/trees/{tree_id}/members/{member_id}      (SPEC-API-06 §10)
 *   DELETE /api/v1/trees/{tree_id}/members/{member_id}      (SPEC-API-06 §11)
 *
 * Field names below are the SERVED snake_case ones verbatim — see
 * `memberListEntry` / `listMembersResponse` in internal/handler/tree_handler.go
 * (routes from CAN-MEM-01/CAN-MEM-02). Nothing here invents a field:
 *
 *   id, tree_id, participant_type, user_id, profile_id, display_name,
 *   profile_name, avatar_url, role, is_visible, auto_approved, joined_at,
 *   last_seen_at, invited_by      →  { members, total, limit, offset }
 *
 * NOTE — the wire carries NO email. Identity is `display_name` for humans and
 * `profile_name` for agent/profile memberships (`user_id` is null on profile
 * rows, `profile_id`/`profile_name` are null on human rows, per §9.3/§9.4).
 * The dialog therefore renders a name plus a factual short id instead of
 * inventing an address for a row that never had one.
 */

import { apiUrl, authInit } from './api.ts';
import {
  getColorForUser,
  type PermissionLevel,
} from '../types/multiUser.ts';

// ─── Wire shapes (served JSON) ─────────────────────────────────────────

/** One entry of `GET /trees/{tree_id}/members` (§9.3). */
export interface TreeMemberWire {
  id: string;
  tree_id: string;
  /** `human` or `profile` (§9.4's polymorphic rows). */
  participant_type: string;
  user_id: string | null;
  profile_id: string | null;
  display_name: string;
  profile_name: string | null;
  avatar_url: string | null;
  /** DB enum: `owner` | `admin` | `member` | `viewer`. */
  role: string;
  is_visible: boolean;
  auto_approved: boolean;
  joined_at: string;
  last_seen_at: string | null;
  invited_by: string | null;
}

/** The §9.3 envelope: the page plus the echoed pagination values. */
export interface TreeMemberListResponse {
  members: TreeMemberWire[];
  total: number;
  limit: number;
  offset: number;
}

// ─── UI shape ──────────────────────────────────────────────────────────

/** A roster row, mapped onto the permission vocabulary the dialog edits. */
export interface TreeMember {
  /** The membership id (`id` on the wire) — the PATCH/DELETE path segment. */
  memberId: string;
  /** The user (human) or profile (agent) id behind the membership. */
  participantId: string;
  /** Display name: `display_name`, else `profile_name`, else a short id. */
  userName: string;
  /** Secondary line — a factual short id, never an invented email. */
  detail: string;
  /** `owner` | `admin` | `member` | `viewer` as served. */
  role: string;
  /** The role in the dialog's vocabulary (`owner` reads as `admin`). */
  permission: PermissionLevel;
  avatarColor: string;
  participantType: string;
  isVisible: boolean;
  autoApproved: boolean;
}

// ─── Role vocabulary bridge ────────────────────────────────────────────

/**
 * Map the served DB role onto the dialog's `PermissionLevel`.
 *
 * `owner` has no counterpart in the UI vocabulary (SPEC-API-06 §10.2 rejects
 * `owner` as a PATCH target — ownership is not transferable through this
 * route), so it reads as `admin`, the highest level the dialog can express.
 * Rows are rendered read-only when `role === 'owner'`; the mapping only
 * decides which badge/select value is shown.
 */
export function roleToPermission(role: string): PermissionLevel {
  switch (role) {
    case 'owner':
    case 'admin':
      return 'admin';
    case 'member':
    case 'editor':
      return 'editor';
    default:
      // `viewer`, and anything unrecognised: least privilege. The badge then
      // shows the level the backend actually grants at worst.
      return 'viewer';
  }
}

/**
 * Map the dialog's `PermissionLevel` onto the role sent to PATCH.
 *
 * The route accepts BOTH vocabularies and normalizes `editor` → `member`
 * (SPEC-API-06 §10.2 / normalizeMemberRole). Sending the DB spelling keeps
 * what the client asked for unambiguous in the request body.
 */
export function permissionToRole(permission: PermissionLevel): string {
  switch (permission) {
    case 'admin':
      return 'admin';
    case 'editor':
      return 'member';
    case 'viewer':
      return 'viewer';
  }
}

// ─── Mapping ───────────────────────────────────────────────────────────

/** First UUID group — the stable short label used when no name is served. */
function shortId(value: string): string {
  return value.slice(0, 8);
}

/**
 * Map one served entry onto the dialog's roster row.
 *
 * Name resolution follows the row's own participant type: a human row names
 * the person (`display_name`), a profile row names the agent
 * (`profile_name`). When the server resolves neither (the PATCH path returns
 * an empty `display_name` for agent memberships — documented deviation), the
 * row still gets a stable, factual label from its id rather than a blank line.
 */
export function memberFromWire(entry: TreeMemberWire): TreeMember {
  const participantType = entry.participant_type ?? 'human';
  const isProfile = participantType === 'profile';

  const participantId =
    (isProfile ? entry.profile_id : entry.user_id) ?? entry.id;
  const servedName = (isProfile ? entry.profile_name : entry.display_name)
    ?? entry.display_name
    ?? '';

  const short = shortId(participantId);
  const fallback = isProfile ? `Agent ${short}` : `User ${short}`;
  const userName = servedName.trim() !== '' ? servedName : fallback;
  const detail = isProfile ? `Agent profile · ${short}` : `User ${short}`;

  const avatarColor =
    entry.avatar_url !== null && entry.avatar_url !== ''
      ? getColorForUser(entry.avatar_url)
      : getColorForUser(participantId);

  return {
    memberId: entry.id,
    participantId,
    userName,
    detail: detail === userName ? '' : detail,
    role: entry.role,
    permission: roleToPermission(entry.role),
    avatarColor,
    participantType,
    isVisible: entry.is_visible,
    autoApproved: entry.auto_approved,
  };
}

/** Map a full §9.3 envelope onto roster rows (tolerates a partial page). */
export function membersFromWire(body: unknown): TreeMember[] {
  const list = (body as TreeMemberListResponse | null)?.members;
  if (!Array.isArray(list)) {
    throw new Error('malformed members response');
  }
  return list.map(memberFromWire);
}

// ─── Errors ────────────────────────────────────────────────────────────

/**
 * `<status> — <reason>` for a failed call, preferring the backend's coded
 * error envelope (`{"error": {"code", "message"}}`). A non-JSON body falls
 * back to the status line so the dialog can always name a cause.
 */
async function errorReason(res: Response): Promise<string> {
  const status = `HTTP ${res.status}`;
  let detail = '';
  try {
    const body = (await res.json()) as {
      error?: { code?: string; message?: string };
    };
    const error = body?.error;
    if (error?.message) {
      detail = error.code ? `${error.code}: ${error.message}` : error.message;
    }
  } catch {
    // Non-JSON error body — keep the status line.
  }
  if (detail === '') detail = res.statusText || 'request failed';
  return `${status} — ${detail}`;
}

/** Wrap a transport rejection (server down / DNS / abort) in one message. */
function networkReason(err: unknown): string {
  if (err instanceof Error && err.message !== '') return err.message;
  return 'network error';
}

function memberPath(treeId: string, memberId?: string): string {
  const base = `/trees/${encodeURIComponent(treeId)}/members`;
  return memberId === undefined
    ? base
    : `${base}/${encodeURIComponent(memberId)}`;
}

// ─── Calls ─────────────────────────────────────────────────────────────

/**
 * GET the tree's roster. One request, no retries — the caller renders the
 * failure and offers the retry.
 */
export async function fetchTreeMembers(treeId: string): Promise<TreeMember[]> {
  let res: Response;
  try {
    res = await fetch(apiUrl(memberPath(treeId)), authInit());
  } catch (err) {
    throw new Error(networkReason(err));
  }
  if (!res.ok) throw new Error(await errorReason(res));

  let body: unknown;
  try {
    body = await res.json();
  } catch {
    throw new Error(`HTTP ${res.status} — malformed members response`);
  }
  return membersFromWire(body);
}

/**
 * PATCH a member's role, then let the caller re-read the roster — the write
 * response is not merged into local state (no optimistic rows; §10's response
 * is a GET-entry shape but the roster is the authority for the visible list).
 */
export async function updateTreeMember(
  treeId: string,
  memberId: string,
  permission: PermissionLevel,
): Promise<void> {
  let res: Response;
  try {
    res = await fetch(apiUrl(memberPath(treeId, memberId)), authInit({
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ role: permissionToRole(permission) }),
    }));
  } catch (err) {
    throw new Error(networkReason(err));
  }
  if (!res.ok) throw new Error(await errorReason(res));
}

/** DELETE a member's membership (§11 hard-deletes the row). */
export async function removeTreeMember(
  treeId: string,
  memberId: string,
): Promise<void> {
  let res: Response;
  try {
    res = await fetch(apiUrl(memberPath(treeId, memberId)), authInit({
      method: 'DELETE',
    }));
  } catch (err) {
    throw new Error(networkReason(err));
  }
  if (!res.ok) throw new Error(await errorReason(res));
}
