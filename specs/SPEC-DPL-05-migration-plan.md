# SPEC-DPL-05 — Hermes Data Migration Plan

> **Status:** Planning | **Priority:** Low | **Phase:** 8 (Deployment)  
> **Dependencies:** BE-04 (Full Backend API), all 20 migrations  
> **Target audience:** Operator deploying canopyd in an existing Hermes environment

---

## 1. Purpose

Define the strategy, data mapping, and tooling required to migrate existing Hermes agent conversation data into Canopy's tree-based data model. This plan covers three migration paths:

1. **Hermes agent session data** → Canopy trees/nodes/edges
2. **Hermes DuckBrain memory namespaces** → Canopy topic-aware nodes
3. **Hermes chat history (JSONL/conversation store)** → Canopy tree imports

---

## 2. Data Model Mapping

### 2.1 Hermes Session → Canopy Tree

| Hermes Concept | Canopy Model | Notes |
|---------------|-------------|-------|
| Session (conversation) | `Tree` | Each session becomes a named tree. Session ID → Tree ID. |
| User message | `Node` (type=message, format=markdown) | Content preserved as-is. |
| Assistant message | `Node` (type=message, format=markdown) | Author ID defaults to a system/agent UUID. |
| System/Tool output | `Node` (type=system, format=plain) | Tool calls, results, error traces. |
| Reply (message order) | `Edge` (type=reply) | Sequential ordering within a session. |
| Fork (branching) | `Edge` (type=fork) | Hermes `/new` creates a new session → Canopy fork from the parent context. |

### 2.2 DuckBrain → Canopy Topics

| DuckBrain Concept | Canopy Model | Notes |
|-------------------|-------------|-------|
| Namespace | `Topic` | Each DuckBrain namespace becomes a topic. |
| Memory entry (key+attributes+embedding) | `Node` (type=message, format=markdown) + metadata in `metadata` JSONB | Key maps to a structured metadata field. |
| Semantic embedding vector | `Node.Metadata` | Stored as metadata for future vector index. |
| Cross-references between namespaces | `Edge` (type=reference) | Links topics (and their nodes) together. |

### 2.3 Hermes Chat History → Canopy Event Log

| Hermes Store | Canopy Model | Notes |
|-------------|-------------|-------|
| JSONL conversation log | `TreeEvent` | Each event in the log becomes a TreeEvent on the target tree. |
| `/new` command | New `Tree` + fork `Edge` from prior tree | Links the old tree's root node to the new tree via a fork edge. |

---

## 3. Migration Strategy

### 3.1 Phase A: Export (Hermes-side tooling)

Create a CLI export command (either as a `hermes export` subcommand or a standalone Python/Go script) that:

```
hermes export --format canopy-jsonl [--since 2026-06-01] [--namespace <ns>] > export.jsonl
```

**Output format** — newline-delimited JSON with Canopy-compatible shapes:

```jsonl
{"type":"tree","id":"uuid-v4","title":"My Session","createdAt":"2026-06-01T00:00:00Z","ownerId":"uuid-v4"}
{"type":"node","id":"uuid-v4","treeId":"uuid-v4","parentId":null,"content":"User message","contentFormat":"markdown","nodeType":"message","authorId":"uuid-v4","sequenceNum":1,"metadata":{"source":"hermes","sessionId":"..."}}
{"type":"node","id":"uuid-v4","treeId":"uuid-v4","parentId":"prev-uuid","content":"Assistant reply","contentFormat":"markdown","nodeType":"message","authorId":"system-uuid","sequenceNum":2}
{"type":"edge","treeId":"uuid-v4","sourceId":"prev-uuid","targetId":"node-uuid","edgeType":"reply","sequenceNum":1}
{"type":"topic","name":"my-namespace","description":"DuckBrain namespace","treeIds":["tree-uuid-1","tree-uuid-2"]}
```

### 3.2 Phase B: Import (Canopy-side ingestion)

Create a `canopyd import` subcommand:

```
canopyd import --file export.jsonl [--dry-run] [--batch-size 100]
```

**Import pipeline:**
1. Read JSONL, validate each record against Canopy's schema
2. Within a database transaction per tree:
   - Create Tree record (if not exists)
   - Bulk-insert nodes with generated UUIDv7 IDs
   - Bulk-insert edges (resolving source/target IDs)
   - Create topic records (if DuckBrain namespace present)
   - Create reference edges between topics and nodes
3. Commit or rollback on error

**Dry-run mode:** Parse and validate without writing to DB. Report record count, errors, estimated row counts.

### 3.3 Phase C: Verification

After import, run verification queries:

```sql
-- Verify all nodes have valid edges
SELECT t.id, t.title, n_count, e_count
FROM trees t
LEFT JOIN (SELECT tree_id, count(*) AS n_count FROM nodes WHERE deleted_at IS NULL GROUP BY tree_id) n ON n.tree_id = t.id
LEFT JOIN (SELECT tree_id, count(*) AS e_count FROM edges WHERE deleted_at IS NULL GROUP BY tree_id) e ON e.tree_id = t.id;

-- Check for orphan nodes (no incoming edges, no parent)
SELECT n.id, n.content::text
FROM nodes n
LEFT JOIN edges e ON e.target_id = n.id
WHERE e.id IS NULL AND n.parent_id IS NULL AND n.deleted_at IS NULL;
```

---

## 4. Implementation Plan

### 4.1 Tasks

| ID | Task | Complexity | Effort | Depends On |
|----|------|-----------|--------|-----------|
| DPL-05-01 | Write `hermes export` subcommand (canopy-jsonl format) | Medium | 2-3h | None (Hermes-side) |
| DPL-05-02 | Write `canopyd import` subcommand (JSONL parser + DB writer) | High | 4-6h | BE-04 API stable |
| DPL-05-03 | DuckBrain namespace → topic mapping logic | Medium | 2h | DPL-05-02 |
| DPL-05-04 | Dry-run validation + error reporting | Low | 1h | DPL-05-02 |
| DPL-05-05 | Verification queries + import health check | Low | 1h | DPL-05-02 |
| DPL-05-06 | Integration test: export 100 sessions → import → verify round-trip | Medium | 2h | DPL-05-01 + DPL-05-02 |
| DPL-05-07 | Documentation: migration runbook in deploy/ | Low | 1h | DPL-05-01→05 |

### 4.2 Priority Ordering

1. **DPL-05-01** (Hermes export) — can be done independently of Canopy codebase
2. **DPL-05-02** (Canopy import) — core ingestion pipeline
3. **DPL-05-06** (Round-trip test) — validates both export + import before production use
4. **DPL-05-03/04/05/07** — edge cases, error handling, docs

### 4.3 Risk Assessment

| Risk | Severity | Mitigation |
|------|----------|-----------|
| UUID mismatch (Hermes uses UUIDv4, Canopy uses UUIDv7) | Medium | Export step generates fresh UUIDv7 for all records; stores original Hermes UUID in `metadata.hermesSessionId` |
| Large sessions (1000+ messages) | Low | Batch import (default 100 records/transaction) with progress reporting |
| DuckBrain embeddings don't map cleanly | Low | Store embeddings as metadata JSON; vector index is a post-MVP concern |
| Data volume (months of Hermes sessions) | Low | Streaming JSONL reader (no full-file load into memory) |
| Concurrent Hermes sessions writing during migration | Low | Snapshot-based export; run export against a read-replica or at low-traffic time |

---

## 5. Service Impact

| Concern | Details |
|---------|---------|
| Canopy uptime | No downtime required — import runs as canopyd subcommand against the same DB |
| PG load | Import is INSERT-heavy. Run at low-load hours. Batch with COMMIT every 100 rows to avoid long-running transactions. |
| Hermes uptime | No downtime. Export reads Hermes storage (JSONL files or DuckDB) with no locks. |
| Rollback | Simple: `DELETE FROM trees WHERE id IN (<imported-ids>); CASCADE handles nodes/edges.` |

---

## 6. Rollout Plan

```
Step 1: Deploy hermes export subcommand (independent release)
    └── Verify output format against a sample session

Step 2: Deploy canopyd import subcommand (canopyd release)
    └── Run import against a PG test database with 10 session export
    └── Verify all trees/nodes/edges render in Canopy UI

Step 3: Dry-run the full migration on staging data
    └── Export 100% of Hermes sessions
    └── Import into staging PG
    └── Run verification queries

Step 4: Production migration (scheduled window)
    └── Snapshot Hermes data
    └── Run export
    └── Run import
    └── Verification
    └── Switch users to Canopy
```

---

## 7. Future Considerations (Post-MVP)

- **Incremental sync**: After initial bulk migration, support incremental sync (new Hermes sessions → Canopy on a schedule)
- **Bidirectional sync**: Allow edits in Canopy to propagate back to Hermes stores (requires conflict resolution)
- **DuckBrain continuous sync**: Map new DuckBrain memories to Canopy topics in near-real-time
- **Multi-tenant migration**: Per-user tree isolation consistent with DIST-01
