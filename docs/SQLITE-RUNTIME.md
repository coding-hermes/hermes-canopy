# SQLite runtime operator note (GAP-097)

The runtime remains PostgreSQL by default. Unset `CANOPY_DB_DRIVER` is equivalent to
`CANOPY_DB_DRIVER=postgres`.

## SQLite boot self-check

```bash
CANOPY_DB_DRIVER=sqlite \
CANOPY_SQLITE_PATH=/srv/canopy/canopy.sqlite \
canopyd serve
```

`CANOPY_SQLITE_PATH` is optional; its default is `~/.canopy/canopy.sqlite`. SQLite boot
opens the file with WAL, foreign keys, a 5-second busy timeout, and `synchronous=NORMAL`.
It applies the embedded core migrations and checks the live table/column inventory. The
wave-1 binary then refuses full server startup because non-core PostgreSQL repositories
are not silently substituted. This refusal is intentional; it protects operators from a
partially SQLite-backed server.

## One-time PostgreSQL export

Configure the PostgreSQL source with `CANOPY_DB_URL`, or with the normal `DB_HOST`,
`DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`, and `DB_SSLMODE` variables. Export to a
new destination:

```bash
canopyd db export-sqlite --sqlite-path /srv/canopy/canopy.sqlite
```

`--dsn` is accepted as an alias for `--sqlite-path` for compatibility with the original
operator proposal. The exporter copies `users`, `trees`, `nodes`, and `edges` in foreign-key
safe order, checks source/target row counts, and verifies SHA-256 content hashes for every
node. It refuses a non-empty destination instead of merging or overwriting it.

Exit codes:

* `0` — export completed and verification passed.
* `1` — PostgreSQL, SQLite, migration, row-count, or content-hash failure.
* `2` — command-line misuse or invalid configuration.

The exporter does not set `CANOPY_DB_DRIVER`; it always reads PostgreSQL as its source.
The destination must be a separate path from any live SQLite runtime file.
