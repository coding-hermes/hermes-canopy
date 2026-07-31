# SPEC-IMPL-GAP-001 — Context Compiler Implementation Spec

> **Task:** GAP-001 — Context compiler (Critical, Cpx 5)
> **Status:** Implementation-ready (this spec)
> **Sources:** ARCHITECTURE.md §7.5, AGENTS.md Core Concepts §2, SPEC-TM-04 §2/§3, SPEC-TM-03
> **Worker target:** A worker reading this spec must produce correct, compilable code with zero clarifying questions.

## 1. Purpose

Create `internal/context/` — a package that transparently assembles a budgeted, auditable context payload for every model call. Given a node ID, it walks the node's ancestry chain, resolves `#references`, applies a token budget, and produces a JSON manifest documenting exactly what was included (and what was truncated). This is the "visible context manifest" core promise of Canopy (AGENTS.md Core Concepts §2).

## 2. Interface (exact — worker copies these)

```go
package context

// Compiler assembles budgeted context for a model call.
type Compiler interface {
    // Compile builds the context payload for the conversation ending at nodeID.
    // Returns the assembled context + its manifest. Never returns a nil
    // Context even on partial failure — degraded results are valid results
    // (see Error Catalog).
    Compile(ctx context.Context, req CompileRequest) (*CompiledContext, error)
}

type CompileRequest struct {
    TreeID     uuid.UUID `json:"treeId"`
    NodeID     uuid.UUID `json:"nodeId"`     // current node (end of thread)
    TokenBudget int      `json:"tokenBudget"` // max tokens for the payload
    MaxAncestors int     `json:"maxAncestors"` // default 50 when 0
    IncludeCards bool    `json:"includeCards"` // attach card data
    ResolveRefs bool     `json:"resolveRefs"`  // default true
}

// CompiledContext is the final payload + manifest.
type CompiledContext struct {
    // Content is the assembled context text (ancestry + references + cards),
    // ready to be placed in the model prompt.
    Content string `json:"content"`

    // Manifest is the auditable record of what was included.
    Manifest *Manifest `json:"manifest"`
}

// Manifest documents exactly what the compiler did. This is the
// user-visible artifact.
type Manifest struct {
    RequestID      string       `json:"requestId"`
    NodeID         uuid.UUID    `json:"nodeId"`
    CompiledAt     time.Time    `json:"compiledAt"`
    TokenBudget    int          `json:"tokenBudget"`
    TokensUsed     int          `json:"tokensUsed"`
    Ancestry       []ManifestItem `json:"ancestry"`
    References     []ManifestItem `json:"references"`
    Cards          []ManifestItem `json:"cards"`
    OmittedCount   int          `json:"omittedCount"`   // nodes dropped by budget
    OmittedReason  string       `json:"omittedReason"`  // "budget" | "depth" | ""
    TruncationMarkers []string  `json:"truncationMarkers"` // e.g. "3 messages omitted"
    Warnings       []string     `json:"warnings"`        // e.g. "5+ references: context becoming unfocused"
}

type ManifestItem struct {
    ID        uuid.UUID `json:"id"`
    Kind      string    `json:"kind"`      // "node" | "topic" | "card"
    Title     string    `json:"title"`     // node: content preview (120 chars); topic: slug; card: card type
    TokenCount int      `json:"tokenCount"`
    Truncated bool      `json:"truncated"` // true if item content was elided
}

// TokenEstimator estimates tokens for a string. Injectable for tests.
type TokenEstimator interface {
    Estimate(s string) int
}

// NewTokenEstimator returns a deterministic estimator:
// ceil(len([]rune(s)) / 4) — a conservative 4-chars-per-token rule.
// No external dependency; deterministic across runs (SPEC-TM-004 determinism requirement).
func NewTokenEstimator() TokenEstimator

// NewCompiler wires repositories + estimator into a Compiler.
func NewCompiler(
    nodes NodeReader,
    topics TopicReader,
    cards CardReader,
    est TokenEstimator,
) Compiler

// --- reader interfaces (implemented by existing repos; worker does NOT reimplement) ---

// NodeReader is satisfied by *db.PGNodeRepo.
type NodeReader interface {
    GetByID(ctx context.Context, id uuid.UUID) (*db.Node, error)
    GetAncestors(ctx context.Context, nodeID uuid.UUID) ([]db.Node, error)
}

// TopicReader is satisfied by *db.PGTopicRepo.
type TopicReader interface {
    GetBySlug(ctx context.Context, treeID uuid.UUID, slug string) (*db.Topic, error)
    GetTopicsForNode(ctx context.Context, nodeID uuid.UUID) ([]db.Topic, error)
}

// CardReader is satisfied by *card.SQLiteCardRepo.
type CardReader interface {
    GetByContextHash(ctx context.Context, contextHash string) ([]card.Card, error)
}
```

## 3. Data Model

No new DB tables. The compiler reads existing tables:
- `nodes` (ancestry chain via `NodeRepo.GetAncestors`)
- `topics` + `node_resolved_refs` (SPEC-TM-04 §3.1 — resolved refs; `GetTopicsForNode`)
- cards via `CardRepository.GetByContextHash`

`node_resolved_refs` already exists from SPEC-TM-04 DDL (`000040_node_resolved_refs.up.sql`). **Verify it exists** in `migrations/`; if absent, add the migration from SPEC-TM-04 §3.1 verbatim (it is the authoritative DDL).

## 4. Wiring

### 4.1 Config (env vars, internal/config/config.go)

Add to `Config` struct:

```go
// Context compiler
ContextMaxAncestors int // CONTEXT_MAX_ANCESTORS, default 50
ContextMaxRefs      int // CONTEXT_MAX_REFS, default 5 (soft) — hard cap is 2x this
ContextDefaultBudget int // CONTEXT_DEFAULT_BUDGET, default 8000 tokens
```

`FromEnv()` reads `CONTEXT_MAX_ANCESTORS`, `CONTEXT_MAX_REFS`, `CONTEXT_DEFAULT_BUDGET` with those defaults. Validation: negative values → error at startup; zero → default.

### 4.2 HTTP routes (internal/server/server.go)

Mount under the existing `/api/v1` chi router:

```go
ctxCompiler := context.NewCompiler(nodeRepo, topicRepo, cardRepo, context.NewTokenEstimator())
r.Route("/api/v1/context", func(r chi.Router) {
    r.With(authMW).Get("/{node_id}", handler.NewContextHandler(ctxCompiler, cfg.ContextDefaultBudget).Compile)
})
```

Route contract:
- `GET /api/v1/context/{node_id}?budget=8000&includeCards=true`
- Auth: requires valid JWT (same `authMW` as other routes; extract UserID via `UserIDFromContext`)
- Response 200: `{"content": "...", "manifest": {...}}`
- Response 404: node not found or node not in a tree the user can read
- Response 400: malformed budget param
- Response 401: missing/invalid JWT

Handler lives at `internal/handler/context_handler.go` (pattern: copy `internal/handler/graph_handler.go` structure — NewXHandler(x) with Routes()/single-method handler).

### 4.3 main.go wiring

- Build `ctxCompiler` in `main.go` where `graphSvc` is built (same repos available)
- Pass into `server.New(...)` (add parameter `ctxCompiler context.Compiler`)

## 5. Compile Algorithm (exact, ordered)

1. **Load current node** via `NodeReader.GetByID(req.NodeID)`. Not found → `ErrNodeNotFound` (404).
2. **Ancestry chain** via `NodeReader.GetAncestors(req.NodeID)`. Reverse to oldest→newest. If `len > MaxAncestors`, keep the NEWEST `MaxAncestors`, set `OmittedReason="depth"`, `OmittedCount = dropped`.
3. **Render ancestry** newest-first (per ARCHITECTURE.md §7.5: "most recent first"). Each node: `--- node <id> (<author_id>) ---\n<content>`. Preview title = first 120 chars of content, single line.
4. **Budget application** (iterative, in order):
   - Estimate full ancestry tokens. While `tokensUsed + nextItem > budget`, drop the OLDEST remaining item, increment `OmittedCount`, set `OmittedReason="budget"` (only if not already "depth"), append `"N messages omitted"` to `TruncationMarkers` (one marker total, N = total omitted).
   - If budget remains after ancestry: process references (step 5), then cards (step 6). Each is budget-gated identically (drop oldest-first).
5. **References**: if `ResolveRefs` (default true):
   - `TopicReader.GetTopicsForNode(req.NodeID)` → resolved topic IDs
   - For each topic, render `--- topic boundary: <slug> ---\n<title>\n<description preview 200 chars>`
   - Soft cap: `ContextMaxRefs` (default 5). Over soft cap → append warning `"N references: context becoming unfocused"` to `Warnings`. Hard cap: `2x ContextMaxRefs` — beyond hard cap, stop adding, warning `"reference limit reached; N references omitted"`.
   - Each reference rendered only if budget allows (oldest-first drop rule).
6. **Cards**: if `IncludeCards`:
   - `CardReader.GetByContextHash(hashOfNodeContent)` — context hash = SHA-256 of node content hex string (match existing card context-hash semantics; check `internal/card/` for the existing hash convention and reuse it — do NOT invent a new one).
   - Render `--- card <type> ---\n<title>\n<summary preview 200 chars>`.
7. **Assemble** `Content` = joined sections with blank lines. Fill `Manifest` fields. `TokensUsed` = estimate(Content). If `TokensUsed > TokenBudget`, note in `Warnings` (should not happen — budget loop prevents it, but defensive).

## 6. Error Catalog

| Error | Condition | HTTP | Notes |
|-------|-----------|------|-------|
| `ErrNodeNotFound` | `GetByID` returns nil/ErrNotFound | 404 | wrapped: `"context: node not found"` |
| `ErrInvalidBudget` | budget < 1 in request or config | 400 | `"context: budget must be >= 1"` |
| `ErrDatabaseUnavailable` | repo returns DB-unavailable sentinel | 503 | reuse `service.ErrDatabaseUnavailable` mapping |
| Internal repo error | any other repo error | 500 | generic `"internal server error"`; real error via zerolog (matches BUG-020 pattern) |
| Partial failure | reference resolution fails for ONE topic | 200 (degraded) | skip that topic, add warning `"reference <slug> unavailable"`. NEVER fail the whole compile. |

**Degradation rule:** The compiler never returns 500 for a single bad reference or card. Only total failure (node missing, DB down) errors out. Partial results always carry `Warnings`.

## 7. Edge Cases

- **Empty ancestry** (node is root): Content = just the node itself. Manifest ancestry = [node]. Not an error.
- **Node content empty**: render empty node section; no error (input validation already prevents empty-content nodes per BUG-019).
- **Budget smaller than one node**: include the single newest node regardless, `TokensUsed` may exceed budget — append warning `"budget too small for single node"`. Never return empty Content when the node exists.
- **Duplicate references**: dedupe by topic ID before rendering.
- **Node has 0 references**: empty References array, no warnings.
- **Concurrent compiles**: Compiler is stateless (all state in request + repos). Safe for concurrent use. Document on the struct.
- **Very deep tree**: `GetAncestors` returns 10k nodes — `MaxAncestors` cap applies BEFORE rendering (step 2), so rendering cost is bounded.
- **Card hash collision**: `GetByContextHash` may return multiple cards — render all (budget-gated).
- **Large content**: single node with 1MB content — truncate node preview to 120 chars in manifest only; full content in Content (budget will drop it if over).
- **Malicious budget param** (`budget=999999999`): clamp to `ContextDefaultBudget * 10` upper bound (defensive).

## 8. Testing (exact scenarios — all in `internal/context/`)

Use stub readers (no PG): `stubNodeReader`, `stubTopicReader`, `stubCardReader` with canned data.

| # | Scenario | Expected |
|---|----------|----------|
| 1 | Ancestry chain 3 nodes, budget 10000 | Content contains all 3 nodes newest-first; Manifest.ancestry len 3; omitted 0 |
| 2 | Budget 500, chain 10 nodes | Oldest dropped; OmittedCount ≥ 1; TruncationMarkers contains "N messages omitted"; newest node always present |
| 3 | Node with 2 #references resolved | References rendered with topic boundary markers; manifest.references len 2 |
| 4 | 12 references, MaxRefs=5 | Only 5 rendered; warning "context becoming unfocused" present; hard cap enforced (≤10) |
| 5 | Reference topic missing (stub returns ErrNotFound) | Compile succeeds; warning "reference <slug> unavailable"; no 500 |
| 6 | IncludeCards=true + card found by hash | Card section rendered; manifest.cards len 1 |
| 7 | IncludeCards=false | No card section; manifest.cards empty |
| 8 | Node not found | ErrNodeNotFound |
| 9 | Budget < 1 | ErrInvalidBudget |
| 10 | Root node (no ancestors) | Content = node only; no error |
| 11 | Determinism: compile same request twice | Byte-identical Content and Manifest (token estimator is deterministic) |
| 12 | Empty content node | Renders; no error |
| 13 | MaxAncestors=2, chain 10 | Only 2 newest; OmittedReason="depth"; OmittedCount=8 |
| 14 | Duplicate refs | Deduped; manifest.references len 1 |
| 15 | Budget too small for one node | Content non-empty; warning "budget too small for single node" |

Handler tests (`internal/handler/context_handler_test.go`, stub compiler): 200 with manifest JSON, 400 bad budget, 401 no JWT, 404 unknown node, 503 DB-down sentinel.

## 9. Hilo Impact

- **Depends on:** `internal/db` (Node/Topic models + readers — unchanged), `internal/card` (CardRepository — unchanged), `internal/handler` (pattern), `internal/server` (wiring), `internal/config` (env vars).
- **Depends on this (after wiring):** `internal/server/server.go` (route), `cmd/canopyd/main.go` (construction), future consumers: SPEC-TM-04 agent-side fetch, FE-08 agent context visualization (frontend calls `/api/v1/context/{id}`), FTR-07 Hermes gateway (agent context assembly).
- **Blast radius:** LOW — new package, additive wiring. No existing package's behavior changes. `server.New` signature changes (one new param) — update the 2 call sites (main.go + server tests) in the same commit.

## 10. Acceptance Criteria (from board GAP-001)

1. `internal/context/` package exists with `Compiler`, `CompileRequest`, `CompiledContext`, `Manifest`, `TokenEstimator` per §2
2. `GET /api/v1/context/{node_id}` returns content + manifest (200)
3. Manifest shows ancestry, references, cards, truncation markers, token counts
4. Budget enforcement works (oldest-first drop, truncation markers)
5. #references resolved via existing topic readers (soft cap 5, hard cap 10, warnings)
6. All 15 unit tests + 5 handler tests pass
7. `go build ./...`, `go vet ./...`, `gofmt` clean
8. `gitreins guard` passes
9. No new TODOs; no changes to existing package behavior except additive `server.New` param

## 11. Out of Scope (explicitly NOT in this task)

- Token counting via external library (tiktoken etc.) — deterministic local estimator only
- Auto-topic detection (TM-02), FTS search (TM-03) — separate tasks
- Frontend visualization of manifest (FE-08 consumes the API later)
- Model-call integration (nothing calls Compiler yet beyond the HTTP handler — that is FTR-07)
