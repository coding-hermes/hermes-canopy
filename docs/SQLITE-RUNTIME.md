# SQLite runtime operator note (GAP-097)

The runtime remains PostgreSQL by default. Unset `CANOPY_DB_DRIVER` is equivalent to
`CANOPY_DB_DRIVER=postgres`.

## SQLite boot self-check

```bash
CANOPY_DB_DRIVER=sqlite \
CANOPY_SQLITE_PATH=/srv/canopy/canopy.sqlite \
canopyd serve
```

`CANOPY_SQLITE_PATH` is optional; its default is `~/.canopy/canopy.sqlite` (see
`config.DefaultSQLitePath()`). SQLite boot opens the file with WAL, foreign keys, a 5-second
busy timeout, and `synchronous=NORMAL`, then runs `PRAGMA quick_check`: a truncated or
otherwise corrupted file is refused at boot with an error naming the path
(`sqlite: QuickCheck <path>: ...`; see `sqlite.IsCorruptionRefusal`), while a zero-byte file
is treated as a fresh store and rebuilt cleanly from the embedded migrations.
It applies the embedded core migrations and checks the live table/column inventory. The
wave-1 binary then refuses full server startup because non-core PostgreSQL repositories
are not silently substituted. This refusal is intentional; it protects operators from a
partially SQLite-backed server.

Check the resolved configuration **without** booting — no database is opened, no
migration runs, no port is bound and no file is created:

```bash
CANOPY_DB_DRIVER=sqlite canopyd config check
```

It prints `driver=sqlite sqlite_path=<file>` (the path boot would use), the rest of the
resolved values, and `serve=refused` — so a passing `config check` on the sqlite backend
is never mistaken for a bootable server. Exit 0 means `serve` would accept the
configuration; exit 1 names the validation failure on stderr.

## One-time PostgreSQL export

Configure the PostgreSQL source with `CANOPY_DB_URL`, or with the normal `DB_HOST`,
`DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`, and `DB_SSLMODE` variables. Export to a
new destination:

```bash
canopyd db export-sqlite --sqlite-path /srv/canopy/canopy.sqlite
```

`--dsn` is accepted as an alias for `--sqlite-path` for compatibility with the original
operator proposal. The exporter copies `users`, `trees`, `nodes`, and `edges` in foreign-key
safe order, checks source/target row counts, verifies SHA-256 content hashes for every
node, and refuses a non-empty destination instead of merging or overwriting it.

**Snapshot consistency (DF-HERMES-CANOPY-97).** Every source read — the four table
copies, the row-count checks, and the hash verification — runs inside one PostgreSQL
transaction at `REPEATABLE READ` isolation, so all reads observe the same snapshot even
while a writer keeps committing. This means a run against a live instance publishes
either a complete, referentially closed snapshot (the exporter re-checks
`PRAGMA foreign_key_check` and orphan parent/edge counts before renaming the file into
place) or fails with an error naming concurrent source writes as the cause
(`source changed due to concurrent PostgreSQL writes`); it never surfaces the old bare
`FOREIGN KEY constraint failed` / `row count mismatch` symptoms. Quiescing the source
(a write-stop during the export) remains the recommended operational practice for a
production migration; the snapshot guarantee makes a live export safe to attempt, and
a fidelity comparison against PostgreSQL after the fact assumes a quiesced source.

Exit codes:

* `0` — export completed and verification passed.
* `1` — PostgreSQL, SQLite, migration, row-count, content-hash, referential-integrity,
  or concurrent-source-write failure.
* `2` — command-line misuse or invalid configuration.

The exporter does not set `CANOPY_DB_DRIVER`; it always reads PostgreSQL as its source.
The destination must be a separate path from any live SQLite runtime file.
