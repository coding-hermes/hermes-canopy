# SQLite pivot — wave 1 translation contract, inventory and wave plan

**Board row:** GAP-076 · **Owner ruling:** 2026-09-16 — SQLite (`modernc.org/sqlite`, pure Go, WAL) becomes
the authoritative graph store. **This document is the wave-2 contract**: it is what the repo-layer swap is
built against, and it is the record of every PostgreSQL clause that wave 1 could *not* turn into SQLite DDL.

**Wave 1 artifact set**

| Artifact | What it is |
| --- | --- |
| `migrations/sqlite/NNNNNN_<name>.up.sql` / `.down.sql` | SQLite translation of PG migrations `000001`–`000010` (10 pairs, 20 files, 644 lines) |
| `migrations/embed.go` | + `//go:embed sqlite/*.sql` and `SQLiteFS() iofs.FS` (rooted at `sqlite/`); the PostgreSQL `FS()` and its `*.sql` scope are untouched |
| `migrations/sqlite_parity_test.go` | The parity harness — inventory, index, enum, FK, syntax, no-silent-drop and round-trip checks + two recorded falsifications |
| this file | rules, all-47 inventory, open decisions, wave plan |

**Measured, not assumed:** the batch is 714 lines of PostgreSQL DDL (`000001`–`000010`, up + down), not the 805
quoted in the brief; `modernc.org/sqlite` v1.58.0 bundles SQLite **3.53.4**; **75** Go files import
`github.com/jackc/pgx` (40 of them non-test), spread over `internal/db` (17 `*_repo.go` + `db.go`/`bootstrap.go`),
`internal/service` (9), `internal/federation` (4), `internal/relay` (3), `internal/fileviewer` (2),
`internal/plugin`, `internal/mls`, `internal/hermes`, `internal/testutil`.

## 1. What wave 1 is NOT

* **No behaviour change.** No Go file outside `migrations/embed.go` is touched; no existing test changes.
* **No boot change.** `cmd/canopyd/main.go:198` still prints *"PostgreSQL required/unreachable"* and the
  mandatory-PostgreSQL path is exactly as it was. `internal/db/migrations.go:4,13` still wires golang-migrate's
  `database/postgres` driver against `canopy.MigrationFiles`.
* **No repo swap.** Not one of the 75 pgx importers changed.
* **No data migration path.** How existing PG rows reach SQLite is a wave-2 decision, not an assumption here.

The PostgreSQL path is byte-identical: `migrations/*.sql` is a single-directory pattern, so adding the `sqlite/`
subdirectory cannot change what `FS()` matches — the parity test asserts the structural property that makes
that true (every entry of `FS()` is a regular `NNNNNN_*.sql` file, no directory entry).

## 2. Translation rules actually applied

| PostgreSQL | SQLite | Rationale |
| --- | --- | --- |
| `uuid` (column / PK) | `TEXT NOT NULL` | No native UUID type; 36-char canonical text is what the Go layer already reads/writes. |
| `DEFAULT uuidv7()` / `gen_random_uuid()` | **no default** | No extension, no stored function, no bundled UUID generator. Go generates the id; a missing id fails loudly on NOT NULL rather than getting a random value. |
| `text`, `varchar(n)` | `TEXT` | SQLite has no length-typed strings; the declared length is not re-derived as a CHECK (PG does not enforce it either — see open decision 6). |
| `boolean` + `DEFAULT true/false` | `INTEGER NOT NULL DEFAULT 1/0 CHECK (col IN (0,1))` | SQLite accepts `true`/`false` literals but stores 0/1; the CHECK keeps the domain closed so a stray `2` cannot enter. |
| `jsonb` | `TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(col))` | `jsonb` validates on write in PG; `json_valid` restores that guarantee. |
| `jsonb_typeof(x) = 'object'` | `json_type(x) = 'object'` | Same JSON1 family, same meaning. |
| `bytea` | `BLOB` | Native binary storage. |
| `timestamptz` | `TEXT` RFC3339 UTC, millisecond precision | Sortable, human-readable, timezone-free; `strftime('%Y-%m-%dT%H:%M:%fZ', ...)` matches Go's `time.RFC3339Nano` prefix. Open decision 1. |
| `clock_timestamp()` / `now()` | `(strftime('%Y-%m-%dT%H:%M:%fZ','now'))` | Wall clock at statement time in both engines (SQLite's `now` is statement-stable). |
| `now() + INTERVAL '7 days'` | `strftime(...,'now','+7 days')` | Same instant; keeps the RFC3339 shape so `expires_at <= now()` comparisons stay lexicographic. |
| `bigint`, `integer`, `int`, `real` | `INTEGER`, `INTEGER`, `INTEGER`, `REAL` | Type affinity. |
| enum (`CREATE TYPE .. AS ENUM`) | `TEXT NOT NULL DEFAULT '…' CHECK (col IN ('…'))` | Identical value list, enforced at the same layer. |
| `char_length(x)` | `length(x)` | Character count in both engines. |
| `x IS DISTINCT FROM y` | `x IS NOT y` | SQLite's null-safe inequality. |
| partial index `WHERE deleted_at IS NOT NULL` / `WHERE is_active = true` | same, `WHERE` clause preserved | SQLite supports partial indexes. |
| `CREATE UNIQUE INDEX … WHERE status='pending'` | same | Partial unique constraints work verbatim. |
| `CREATE INDEX … (created_at DESC)` | same | Supported. |
| `CREATE INDEX … (a DESC NULLS LAST)` | same | SQLite ≥ 3.30. |
| expression index `((details->>'tree_id'), created_at)` | `(json_extract(details,'$.tree_id'), created_at)` | `json_extract` is deterministic, so it is index-legal (verified). |
| `INSERT … ON CONFLICT (k) DO NOTHING` | same | Supported since 3.24. |
| seeded `''::jsonb` literals | plain `'…'` TEXT literals | The cast has no SQLite form; the value is already a JSON string. |
| **`ALTER TABLE t ADD CONSTRAINT … FOREIGN KEY`** (000009:118) | FK declared **inline** on the referencing column | SQLite cannot add constraints by ALTER; a reference to a table created later in the same batch is accepted and resolved at DML time (verified). |
| **column definitions after table constraints** | **reordered**: all columns first, then all constraints | SQLite's grammar rejects a column definition that follows a table constraint (`near "role": syntax error`); PG allows arbitrary interleaving. Applied once, in `sqlite/000008_users_profiles.up.sql` (`tree_members`). |
| `DROP TABLE … CASCADE` | `DROP TABLE …` | No CASCADE keyword (verified: syntax error); SQLite drops the table's indexes and triggers automatically. |
| `DROP TRIGGER … ON <table>` | `DROP TRIGGER IF EXISTS <name>` | No `ON <table>` clause (verified: syntax error). |
| `ALTER TABLE … ADD COLUMN … SET NOT NULL` (000006:2–4) | single `ADD COLUMN … NOT NULL` | SQLite cannot `ALTER COLUMN`; the direct form is accepted on an empty table (the state the chain runs in) and refused loudly on a populated one. |
| `ALTER TABLE … ALTER COLUMN SET DEFAULT` | 12-step rebuild (or deliberate no-op) | Not expressible. See 000039 in §4. |

## 3. SQLite semantics encountered and verified (on SQLite 3.53.4 / modernc v1.58.0)

These were measured while writing the translation; each one changed a decision.

1. `SET NEW.<col> = …` inside a trigger is a **syntax error**. A `BEFORE` trigger therefore cannot rewrite the
   row being inserted, and because a `NOT NULL` column is checked *before* an `AFTER` trigger can repair it,
   no trigger can fill a `NOT NULL` column after the fact. → the two "fill the row" PG triggers become Go
   obligations (§5).
2. `ALTER TABLE t ADD COLUMN c TEXT NOT NULL` (no default) is **accepted on an empty table** and **rejected**
   (`Cannot add a NOT NULL column with default value NULL`) on a populated one. The migration chain runs on an
   empty store, so 000006 keeps the strict form instead of inventing a `DEFAULT ''` that would let a missing
   content hash pass as a real one.
3. A column definition may **not** follow a table constraint (rule table above).
4. `DROP TABLE … CASCADE` and `DROP TRIGGER … ON t` are syntax errors.
5. There is **no hash function**: `SELECT sha3('a',256)` → `no such function: sha3`. Content hashing cannot
   live in SQLite DDL at all (and SHA3-256 would not match PG's SHA-256 values anyway).
6. `json_valid()`, `json_type()`, `json_extract()` are present; `json_extract` is usable in an index.
7. `AFTER UPDATE` triggers that re-`UPDATE` their own row do **not** recurse (`recursive_triggers` defaults to
   OFF), so the `edited_at` / `updated_at` triggers translate faithfully; a no-op update leaves the timestamp
   untouched, matching the PG `WHEN` clause.
8. `true`/`false` literals work in `DEFAULT`, `CHECK` and partial-index predicates.

## 4. Inventory — all 47 migrations

Classification: **mechanical** = the wave-1 rule table translates it, no decision needed.
**needs-decision** = translatable, but a clause needs an owner decision (open decisions §6) or a mechanical
rewrite that changes a PG statement's shape. **blocked** = the migration is about a PostgreSQL subsystem that
SQLite does not have. Every non-mechanical clause names its PG file:line.

| # | Migration | Class | Non-mechanical clause(s) — PG file:line |
| --- | --- | --- | --- |
| 000001 | extensions | **blocked** (no-op pair) | `pgcrypto` :7, `pg_uuidv7` :10, `uuidv7()` :21-43 → Go id generation |
| 000002 | trees | mechanical | — |
| 000003 | nodes | **needs-decision** | GIN FTS index :35; `set_node_sequence`/`trg_node_sequence` :38-52 → Go |
| 000004 | edges | mechanical | — |
| 000005 | snapshots | mechanical | — |
| 000006 | node_content_hash | **needs-decision** | sha256 backfill :3; `set_content_hash`/`trg_node_content_hash` :5-13 → Go |
| 000007 | tree_events | mechanical | — |
| 000008 | users_profiles | **needs-decision** | email `~*` regex CHECK :68 (weakened to GLOB); shared `trigger_set_updated_at` :208-222 (inlined into 2 triggers) |
| 000009 | approvals | **needs-decision** | `ALTER … ADD CONSTRAINT fk_approvals_rule` :118 (inline FK); `REVOKE … FROM PUBLIC` :144 (no privilege model); `expire_pending_approvals()` :166-178 (RETURNING, Go) |
| 000010 | profile_route | mechanical | (FK to `workspaces` deferred to 000018) |
| 000011 | transport_connections | mechanical | — |
| 000012 | transport_configs | mechanical | seed `ON CONFLICT DO NOTHING` :23 supported |
| 000013 | transport_events | mechanical | — |
| 000014 | mls_groups | mechanical | `jsonb_typeof` CHECK :10 → `json_type` |
| 000015 | mls_group_members | mechanical | — |
| 000016 | mls_key_packages | mechanical | `CHECK (expires_at > created_at)` :9 holds on RFC3339 TEXT |
| 000017 | mls_pending_proposals | mechanical | — |
| 000018 | workspaces | **needs-decision** | `ALTER … ADD CONSTRAINT` FK :16 (profile_route) and :20 (mls_groups) — SQLite cannot add FKs by ALTER |
| 000019 | canopy_role | **blocked** | `CREATE ROLE canopy_app` :4-10, `REVOKE … FROM canopy_app` :14 — SQLite has no roles/privileges |
| 000020 | topics | **needs-decision** | `text[] topic_tags` :13; `tsvector search_vector` :14; GIN indexes :35-36; `generate_topic_slug()` :42-51 → Go; search-vector trigger :53-62; recursive view `topic_member_nodes` :64-75; `refresh_topic_node_count()` :77-85 → Go |
| 000021 | node_resolved_refs | **needs-decision** | slug regex CHECK `~` :24 |
| 000022 | plugin_registry | **needs-decision** | `text[] permissions` :13; `version ~` :32 and `slug ~` :34 regex CHECKs |
| 000023 | plugin_instances | **needs-decision** | `text[] granted_permissions` :12; expression unique index with `'…'::uuid` :27 (mechanical without the cast) |
| 000024 | plugin_audit_log | mechanical | — |
| 000025 | node_content_hash_utf8 | **needs-decision** | `set_content_hash()` redefinition :7-12 → Go (the 000006 obligation, corrected for UTF-8) |
| 000026 | topic_search | **needs-decision** | weighted `tsvector` trigger :14-28; backfill UPDATE :31-33; `ADD COLUMN IF NOT EXISTS` :38 (no SQLite form); `tsvector` column :56; GIN indexes :71-72; `refresh_topic_node_content_index(uuid[])` :78-125 (arrays + `ON CONFLICT DO UPDATE`) → Go; `'{}'::jsonb` :135; `USING hash(query_text)` :143 (no hash index in SQLite) |
| 000027 | reference_resolution_cache | mechanical | `clock_timestamp() + interval '24 hours'` :15 → `strftime(...,'+24 hours')` |
| 000028 | reference_resolution_log | mechanical | — |
| 000029 | reference_triggers_and_counts | **needs-decision** | cache-invalidation trigger with `uuid[]`/`ANY` :10-36; `ADD COLUMN IF NOT EXISTS` :41,:66; ref-count triggers with `TG_OP`/`IF`/`GREATEST` :43-83 (splittable into per-op triggers); backfill UPDATEs :89-103 (mechanical) |
| 000030 | topic_detection | mechanical | seed `ON CONFLICT DO NOTHING` :69 supported |
| 000031 | tree_events_yjs_update | **needs-decision** | `DROP CONSTRAINT chk_event_type` :8 + `ADD CONSTRAINT … CHECK` :9 — SQLite needs a 12-step rebuild |
| 000032 | workspace_collaboration | **needs-decision** | three `ADD COLUMN IF NOT EXISTS` :14-16 (SQLite: plain ADD COLUMN, tolerate the duplicate error) |
| 000033 | federation_peers | mechanical | — |
| 000034 | federation_transport | mechanical | `ADD COLUMN signing_public_key BYTEA` :10 → plain ADD COLUMN BLOB |
| 000035 | profile_routes | mechanical | — |
| 000036 | federation_events | mechanical | — |
| 000037 | federation_conflicts | mechanical | `jsonb_typeof` CHECK :11 → `json_type` |
| 000038 | transport_selector | **needs-decision** | `ON CONFLICT … DO UPDATE SET config_json = transport_configs.config_json \|\| EXCLUDED.config_json` :26 — PG's `\|\|` is a **jsonb merge**, SQLite's `\|\|` is **string concat**; must become `json_patch` |
| 000039 | plugin_registry | **needs-decision** | three `ALTER COLUMN id SET DEFAULT gen_random_uuid()` :5,:8,:11 — no `ALTER COLUMN` in SQLite; recommendation: no-op (ids are Go-generated) |
| 000040 | relay_config | mechanical | — |
| 000041 | relay_registry | mechanical | — |
| 000042 | tenants | **needs-decision** | `ALTER … ADD CONSTRAINT` FK :17 (relay_instances), :21 (relay_sessions) |
| 000043 | file_metadata | mechanical | partial unique index :52-55, `DESC NULLS LAST` :62 both native |
| 000044 | viewer_registry | mechanical | partial unique index :39-41 native |
| 000045 | file_access_log | mechanical | `reject_file_access_log_mutation()` :34-47 → two `BEFORE UPDATE`/`BEFORE DELETE` triggers with `RAISE(ABORT, …)`, expressible in SQLite DDL |
| 000046 | viewer_config_overrides | mechanical | — |
| 000047 | multi_message_reference | **needs-decision** | `ADD COLUMN parent_mode` :15-16 (mechanical); two `ADD CONSTRAINT … CHECK` :18-25 — no ALTER for checks, needs a table rebuild |

Totals: **16 mechanical**, **26 needs-decision**, **3 blocked/no-op-ish** (000001 no-op pair, 000019 role/REVOKE,
000025 function-only) — counted per row above as 47 rows; 000025 is a function-only migration whose whole
content is a Go obligation.

## 5. Wave-1 objects that became Go-critical obligations

Nothing in this table is "dropped"; each row is a PG object with an owner. The parity harness fails if any of
these names disappears from the SQLite comments (`no-silent-object-drop`).

| PG object | PG file:line | Why it cannot exist in SQLite | Wave-2 owner |
| --- | --- | --- | --- |
| `pgcrypto` / `pg_uuidv7` extensions | 000001_extensions.up.sql:7,10 | no extension catalogue | none (nothing depended on them but id generation) |
| `uuidv7()` + every `DEFAULT uuidv7()` | 000001_extensions.up.sql:21 | no stored functions, no UUID generator | repo layer: generate UUIDv7 on insert |
| `gen_random_uuid()` defaults | 000010:7, 000018:6, 000032:34, 000033:3, 000035:3, 000036:3, 000040:2, 000041:13,30, 000042:9 | same | repo layer (same code path) |
| `set_node_sequence()` / `trg_node_sequence` | 000003_nodes.up.sql:38-52 | no `NEW` assignment in triggers; column is NOT NULL | repo layer: `SELECT MAX(sequence_num)+1` in the insert transaction |
| `set_content_hash()` / `trg_node_content_hash` | 000006_node_content_hash.up.sql:5,11 (redefined 000025:7) | no hash function | repo layer: sha256 over UTF-8 content on write + backfill on first open |
| `expire_pending_approvals()` | 000009_approvals.up.sql:166 | no stored functions | expiry job issues `UPDATE … WHERE status='pending' AND expires_at <= … RETURNING id, tree_id` (SQLite supports `RETURNING`) |
| `REVOKE` on `approval_audit_log` | 000009_approvals.up.sql:144 (role: 000019:14) | no privilege model — and the PG clause is a no-op anyway (a new table grants nothing to PUBLIC) | audit repo, **only if** real immutability is wanted (a `RAISE(ABORT)` trigger) |
| `idx_nodes_content_fts` (GIN) | 000003_nodes.up.sql:35 | no GIN, no tsvector | search layer — open decision 2 |
| `topic_member_nodes` view | 000020_topics.up.sql:64-75 | *translates* (recursive CTE views are supported); listed here because it is the input to `ANY`-style triggers in 000020/000029 |

## 6. Open decisions for the owner

**D1 — timestamp representation.** (a) `TEXT` RFC3339 UTC, millisecond precision, `Z` suffix *(recommended —
what wave 1 uses)*; (b) `INTEGER` epoch milliseconds; (c) `REAL` Julian day.
→ (a) keeps values inspectable and sortable with plain string comparison, matches what the JS/PWA side already
re-parses, and needs no conversion in Go (`time.RFC3339Nano` is a prefix-match). (b) is faster to compare and
smaller on disk but every ad-hoc inspection needs a converter. (c) is a trap; avoid.

**D2 — full-text search.** (a) FTS5 external-content virtual table (`content='nodes'`, triggers to sync) +
`bm25()` ranking; (b) `LIKE '%term%'` scans with `LIKE`-optimizable indexes; (c) FTS5 with a `rank`-free
"simple" tokenizer only for the `nodes.content` case.
→ (a) is the only option that preserves PG's `to_tsvector('english', content)` capability (phrase and prefix
queries, weighted ranking) and it stays pure-SQLite; the cost is a second set of sync triggers and a rebuild
step on first open. (b) is a behaviour regression that will hurt at >10⁵ nodes. Recommendation: (a), scoped to
`nodes.content` first; the FTS table must NOT be created in wave 1 so the decision stays open.

**D3 — id generation.** (a) UUIDv7 in Go on every insert *(recommended)*; (b) a SQLite `DEFAULT` built from
`randomblob`/`hex` (UUIDv4-shaped, not time-ordered); (c) a table-backed sequence + `hex` composition.
→ (a): time-ordered ids matter for the graph's insert locality and for `tree_events` ordering, and PG's
behaviour (a DB-side `DEFAULT`) is exactly what we can no longer have. (b) loses ordering; (c) adds a
bottleneck and can drift under concurrency. If (b) is chosen, the DDL gains
`DEFAULT (lower(hex(randomblob(4))) || '-' || …)`, which is still *not* a foreign key to time.

**D4 — trigger versus Go for sequence / hash / `updated_at`.** (a) Keep the translated `updated_at` and
`edited_at` triggers in DDL and move only the hash + sequence to Go *(recommended — what wave 1 ships)*;
(b) move everything to Go for symmetry; (c) keep everything in DDL where possible and accept `''` placeholders.
→ (a) keeps the invariants that SQLite can enforce inside the database (no Go path can forget them) while
giving the two impossible ones an explicit owner. (b) means a `psql`-style session or any future writer can
corrupt `updated_at` silently.

**D5 — JSON storage and validation.** (a) `TEXT` + `CHECK (json_valid(col))` *(recommended — same guarantee as
`jsonb`)*; (b) `TEXT` with no CHECK, validation in Go only; (c) `BLOB` with the JSON1 functions applied to it.
→ (a) preserves the write-time rejection PG gave us for free and costs nothing at read time; (b) lets
malformed JSON in through any path that forgets to validate; (c) has no benefit and breaks `json_extract`
indexing ergonomics. Related: PG's `~*`/`~` regex CHECKs (000008:68, 000021:24, 000022:32,34) have no SQLite
form — either register a Go `REGEXP` function at connection setup or move that validation into the repo layer.
The wave-1 GLOB approximation in `sqlite/000008_users_profiles.up.sql` is explicitly *weaker* and must be
upgraded (or replaced) when the decision lands.

**D6 — pragma set at connection open.** (a) `journal_mode=WAL`, `foreign_keys=ON`, `busy_timeout=5000`,
`synchronous=NORMAL` *(recommended)*; (b) WAL + `foreign_keys=OFF` (PG-like "constraints are declarative");
(c) `journal_mode=DELETE`, `foreign_keys=ON`.
→ `foreign_keys=ON` is required to get any value from the 40+ FK clauses that survive translation — with it
OFF they are documentation. `busy_timeout` is mandatory for a server process. Recommendation: (a), set once in
the DSN used by the wave-2 store; note that FK enforcement means insert order matters (parents first), which
is already how the repo layer writes.

## 7. Wave plan

**Wave 1 (this deliverable — DONE, additive only):** core-graph DDL translation `000001`–`000010`, the second
embed + `SQLiteFS()`, the parity harness, and this contract. Nothing is wired; PostgreSQL is untouched.

**Wave 2 — repo-layer swap (17 `internal/db/*_repo.go` files + `db.go`/`bootstrap.go`; 75 files import pgx
repo-wide).** Introduce a store interface, add a `modernc.org/sqlite` implementation behind it, and land the
four Go obligations from §5 (id generation, node sequence, content hashing, approval expiry) *with* the pragma
set from D6. Translate the remaining migrations as far as the batch allows — the wave-2 store only needs
`000001`–`000010` for the core graph; the other 37 arrive with their own feature waves. Acceptance for wave 2
is behavioural: the existing repo tests must run against SQLite, including the FK/ordering paths, and
`content_hash` must match the PG-computed values for the same content (a cross-engine fixture test).

**Wave 3 — boot path.** Replace the mandatory-PostgreSQL branch at `cmd/canopyd/main.go:198`, wire
golang-migrate's `database/sqlite3`-equivalent driver (or apply the SQLite files directly through
`migrations.SQLiteFS()`) and pick the DSN/pragma defaults. Note the root embed (`embed.go:6`,
`canopy.MigrationFiles`) is what `internal/db/migrations.go:13` consumes today; wave 3 must decide whether the
SQLite FS comes from `migrations.SQLiteFS()` (recommended — one canonical accessor, already tested) or from a
second embed in the root package. Then run the full test suite on a SQLite-only host with no PostgreSQL
available, and re-run this parity harness as part of CI.

**Wave 4 — retire PostgreSQL.** Drop the pgx dependency, the `database/postgres` migrate driver, the PG
migrations directory (after the historical record is archived), `docker compose` Postgres references and
`docs/INTEGRATION.md §2`. Only after wave 3 has run green for a full release cycle.

## 8. Wave-1 evidence

`go test ./migrations/ -run TestSQLiteParity -count=1 -v` — eight sub-checks:
`table-inventory-parity` (table/column/nullability/PK), `index-parity` (47 of 48 PG indexes by name; the one
exemption is `idx_nodes_content_fts`), `enum-check-parity` (6 PG enums, 10 enum columns, incl. the one
deliberate narrowing), `inline-fk-parity`, `no-postgres-only-syntax` (21 forbidden constructs),
`no-silent-object-drop` (every PG trigger/function/extension accounted for, with a file:line + owner
justification that the test itself validates), `migration-000001-is-a-documented-noop`, and
`down-migrations-clean-up` (reverse-order down pass leaves zero objects).

Both falsifications were run and are reproducible: deleting `trees.description` fails with
`COLUMN MISSING: trees.description (PG 000002_trees.up.sql:13) is absent from the SQLite table`; appending an
unknown `zz_orphan_probe` table fails with `ORPHAN TABLE: …` **and** `DOWN MIGRATIONS INCOMPLETE: 1 object(s)
survive the reverse pass: [table:zz_orphan_probe]`. The harness deliberately refuses an exemption without a
`<file>.up.sql:<line>` reference **and** a named wave-2 owner — adding one without both fails
`JUSTIFICATION MISSING` / `JUSTIFICATION INCOMPLETE`.
