# Hermes Canopy — Security Audit Report (TEST-04)

**Date:** 2026-07-27  
**Auditor:** Automated security audit via `go test -run TestSEC`  
**Scope:** Full Go backend (handlers, middleware, services, repositories, MLS)  
**Tests:** 17 runnable security tests in `internal/handler/security_audit_test.go`

## Executive Summary

Found **9 vulnerabilities** (1 Critical, 5 High, 3 Medium) across 6 audit areas.  
The MLS encryption layer is entirely non-functional (plaintext pass-through).  
Authorization is missing for cross-user tree/node access.  
SQL injection surface is clean — all repos use parameterized queries correctly.

---

## Results by Severity

| Severity  | Count | Tests                                                                    |
| --------- | ----- | ------------------------------------------------------------------------ |
| CRITICAL  | 1     | SEC01: MLS Encrypt is a NO-OP (plaintext pass-through)                   |
| HIGH      | 5     | SEC02, SEC03, SEC08, SEC09, SEC09b                                       |
| MEDIUM    | 3     | SEC06b, SEC11b, SEC12b                                                   |
| PASSING   | 8     | SEC04, SEC05, SEC06, SEC07, SEC10, SEC11, SEC11c, SEC12                  |

---

## 1. MLS Key Rotation & Encryption

### CRITICAL — SEC01: MLS Encrypt is a NO-OP
**File:** `internal/mls/service.go:145-147`
```go
ciphertext := make([]byte, len(plaintext))
copy(ciphertext, plaintext)
```
All "encrypted" data is transmitted as literal plaintext. The Encrypt function performs no cryptographic transformation. Decrypt similarly copies ciphertext bytes directly back. Anyone intercepting MLS traffic can read the data.

**Impact:** All MLS-secured communication is effectively unencrypted.

### HIGH — SEC02: Key Reuse (Encryption == Signing Key)
**File:** `internal/mls/service.go:55-56`
```go
EncryptionPublicKey: adminKeyPair.PublicKey,
SignaturePublicKey:  adminKeyPair.PublicKey,
```
The same Ed25519 key material is used for both encryption and signature operations, violating RFC 9420's cryptographic separation requirement. This enables cross-protocol attacks.

### HIGH — SEC03: No Key Rotation
**File:** `internal/mls/service.go:105,118,131`
```go
s.groups.UpdateEpoch(ctx, grp.ID, grp.Epoch+1, grp.TreeHash)
```
When members join, leave, or are removed, the epoch counter increments but the TreeHash is passed through unchanged from the pre-operation state. No actual key rotation occurs — only the counter advances. Forward secrecy is not provided.

### HIGH — SEC03 (additional): GetEpochSecret Non-Deterministic
**File:** `internal/mls/service.go:214-223`
`GetEpochSecret()` generates fresh random bytes on every call instead of deriving a deterministic epoch secret from group state as RFC 9420 requires.

---

## 2. JWT Expiry & Authentication

### PASS — SEC04, SEC05, SEC06: Core JWT Validation
- Expired tokens are correctly rejected by the `jwt` library
- Tokens signed with wrong keys are rejected
- Unsigned tokens (alg=none) are rejected via `jwt.WithValidMethods`

### MEDIUM — SEC06b: Non-Standard `user_id` Claim Fallback
**File:** `internal/handler/auth.go:46-48`
```go
if raw, exists := claims["user_id"].(string); exists {
    subject = raw
}
```
The auth middleware accepts a `user_id` claim as an alternative to the standard `sub` claim. This is non-standard JWT behavior and may cause interoperability issues with OIDC-compliant identity providers. If `user_id` were set to a different value than `sub`, it could create confusion about which claim takes precedence.

---

## 3. Auth Bypass

### HIGH — SEC08: Cross-User Tree & Node Access
**No ownership enforcement exists.** Any authenticated user can access any tree or node by knowing its UUID. User B can read and potentially modify User A's trees and nodes. The `GetTree` and node handlers perform no ownership check.

**Exploit:** `GET /api/v1/trees/{any-tree-id}` with any valid JWT → returns the tree data.

### HIGH — SEC09: AuthorID Hardcoded to Sentinel
**Files:** `internal/handler/tree_handler.go:75`, `internal/handler/node_handler.go:73`
```go
authorID := uuid.Nil // MVP: sentinel author — real user from JWT ignored
```
All create/update/delete operations are attributed to `uuid.Nil` (the sentinel user) instead of the authenticated user from the JWT context. The audit trail is useless — every operation appears to come from the same user.

### HIGH — SEC09b: TreeMembershipMiddleware Not Wired
**File:** `internal/handler/middleware.go:157-188`
`TreeMembershipMiddleware` exists and is correctly implemented, but it is **not mounted** in any of the test server routing setups (`newTestServer`, `newTestServerWithApprovals`, `newTestServerWithFullAPI`). Any authenticated user can create nodes in trees they don't belong to.

---

## 4. SQL Injection

### PASS — SEC10: All Queries Parameterized
All 7 repository files (`tree_repo.go`, `node_repo.go`, `edge_repo.go`, `approval_repo.go`, `topic_repo.go`, `user_repo.go`, `mls_repo.go`) consistently use `$1, $2, ...` parameterized placeholders. No string concatenation of user input into SQL statements.

**Exception noted:** `topic_repo.go:Update()` uses `fmt.Sprintf` to build SET clauses, but only for column names derived from code constants, not user input. Values remain parameterized.

**Verdict:** No SQL injection vulnerabilities detected.

---

## 5. Input Validation

### MEDIUM — SEC11b: Empty Content Accepted
The node creation endpoint (`POST /trees/{id}/nodes`) accepts nodes with empty string content. No minimum-content-length validation exists at the handler level.

### PASS — SEC11: Large Content Rejected
500KB content is correctly rejected (400 Bad Request). Service-layer validation enforces a 100KB limit.

### PASS — SEC11c: Invalid UUIDs Rejected
Non-UUID tree IDs return 400 Bad Request.

---

## 6. Error Message Leakage

### MEDIUM — SEC12b: User Input Echoed in Error Messages
**File:** `internal/handler/approval_handler.go:75`
```go
writeError(w, http.StatusBadRequest, "INVALID_TREE_ID", "tree_id is not a valid UUID: "+treeIDStr)
```
User-provided values are echoed back verbatim in error messages. While not directly exploitable through the JSON API, this creates a vector for injection in log analysis tools that parse error responses.

### PASS — SEC12: Internal Errors Are Generic
500-level errors return generic `"internal server error"` messages. No stack traces, database connection strings, file paths, or memory addresses leak in error responses.

---

## Detailed Test Results

```
Test                                     Result    Area
────────────────────────────────────────────────────────────
TestSEC01_MLS_EncryptNoOp                FAIL      MLS - CRITICAL: encrypt=pass-through
TestSEC02_MLS_KeyReuse                   FAIL      MLS - HIGH: enc+sig same key
TestSEC03_MLS_NoKeyRotation              FAIL      MLS - HIGH: TreeHash never changes
TestSEC04_JWT_ExpiredToken               PASS      JWT - expiry enforced
TestSEC05_JWT_WrongSigningKey            PASS      JWT - wrong key rejected
TestSEC06_JWT_NoSignature                PASS      JWT - alg=none rejected
TestSEC06b_JWT_UserIdFallback            FAIL      JWT - MEDIUM: non-standard claim
TestSEC07_AuthBypass_NoTokenAccess       PASS      Auth - unauthenticated rejected
TestSEC08_AuthBypass_CrossUserAccess     FAIL      Auth - HIGH: no ownership check
TestSEC09_AuthBypass_AuthorNotFromCtx    FAIL      Auth - HIGH: authorID = uuid.Nil
TestSEC09b_AuthBypass_MembershipNotEnf   FAIL      Auth - HIGH: middleware not wired
TestSEC10_SQLInjection_Parameterized     PASS      SQL - all queries safe
TestSEC11_InputValidation_LargeContent   PASS      Input - large content rejected
TestSEC11b_InputValidation_EmptyContent  FAIL      Input - MEDIUM: empty accepted
TestSEC11c_InputValidation_InvalidUUID   PASS      Input - UUID validated
TestSEC12_ErrorLeakage_NoInternalInfo    PASS      Error - no internal leaks
TestSEC12b_ErrorLeakage_ApprovalEcho     FAIL      Error - MEDIUM: input echoed
```

---

## Remediation Recommendations

### Immediate (P0)
1. **Replace MLS Encrypt/Decrypt with real cryptographic operations.** The current no-op implementation is a placeholder — it must be replaced with an actual RFC 9420 MLS library before any production use.

2. **Wire `UserIDFromContext(r.Context())` into all handlers.** Replace all `authorID := uuid.Nil` with `UserIDFromContext(r.Context())`.

3. **Add ownership checks to tree/node access handlers.** `GetTree`, `GetNode`, `UpdateNode`, `DeleteNode` must verify the authenticated user owns or is a member of the target tree.

### High Priority (P1)
4. **Mount `TreeMembershipMiddleware` in the actual server routing** (not just define it in middleware.go).

5. **Use separate keys for encryption and signing** in MLS (per RFC 9420).

6. **Implement proper key rotation** — recompute `TreeHash` when group membership changes, ratchet key material forward.

### Medium Priority (P2)
7. **Remove the `user_id` claim fallback** or document it explicitly as an intentional non-standard extension.

8. **Add minimum-content-length validation** for node creation (reject empty strings).

9. **Sanitize user input in error messages** — use a static message like `"tree_id must be a valid UUID"` without echoing the invalid value.

---

## How to Run

```bash
cd /home/kara/hermes-canopy
go test ./internal/handler/ -run TestSEC -v -count=1
```

Tests that PASS indicate the security control is working correctly.  
Tests that FAIL indicate a vulnerability that needs remediation.

---

## Files Modified
- `internal/handler/security_audit_test.go` — 17 security tests (NEW)
- `SECURITY_AUDIT.md` — This report (NEW)
