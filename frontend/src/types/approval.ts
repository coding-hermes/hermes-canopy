/**
 * Hermes Canopy — Approval Types
 *
 * Types for the approval workflow (FE-06).
 * Aligned with BE-07 approval and audit endpoints.
 */

// ─── Status types ──────────────────────────────────────────────────────

export type ApprovalStatus = 'pending' | 'approved' | 'denied';

export type ChangeType = 'create' | 'update' | 'delete' | 'move';

// ─── Core entities ─────────────────────────────────────────────────────

/** An approval request returned by GET /api/v1/approvals */
export interface ApprovalItem {
  id: string;
  treeId: string;
  nodeId: string;
  authorId: string;
  changeType: ChangeType;
  title: string;
  description: string;
  status: ApprovalStatus;
  proposedChanges: Record<string, unknown>;
  previousState: Record<string, unknown> | null;
  createdAt: string;
  updatedAt: string;
  reviewedAt: string | null;
  reviewedBy: string | null;
}

/** An audit trail entry returned by GET /api/v1/approvals/{id}/audit */
export interface AuditEntry {
  id: string;
  approvalId: string;
  action: string;
  actorId: string;
  details: Record<string, unknown>;
  timestamp: string;
}

// ─── Request payloads ──────────────────────────────────────────────────

/** POST /api/v1/approvals/{id}/approve or /deny */
export interface ApprovalActionPayload {
  reviewerId?: string;
  comment?: string;
}

// ─── Diff types ────────────────────────────────────────────────────────

/** A single field change within a diff */
export interface DiffField {
  field: string;
  oldValue: unknown;
  newValue: unknown;
  kind: 'added' | 'removed' | 'modified' | 'unchanged';
}

/** Tree-aware diff for node changes */
export interface TreeDiff {
  nodeId: string;
  nodeLabel: string;
  changeType: ChangeType;
  fields: DiffField[];
  /** For move operations: old vs new parent information */
  parentChange?: {
    oldParentId: string | null;
    newParentId: string | null;
    oldParentLabel?: string;
    newParentLabel?: string;
  };
}

// ─── API helper ────────────────────────────────────────────────────────

const API_BASE =
  import.meta.env.VITE_API_BASE_URL ?? 'http://localhost:8080/api/v1';

export function apiUrl(path: string): string {
  return `${API_BASE}${path}`;
}
