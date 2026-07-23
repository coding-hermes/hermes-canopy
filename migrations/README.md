# Canopy Migrations

Migrations are numbered lexicographically. Files MUST be applied in order;
golang-migrate uses the numeric prefix to order them inside a single
`migrate up` call.

The numerical order was chosen to respect SQL FK dependencies:

1. `000001_extensions` — `pgcrypto`, `pg_uuidv7`, fallback `uuidv7()`.
2. `000002_trees`     — `trees` table (no FK into other Canopy tables).
3. `000003_nodes`     — `nodes` table references `trees(id)`.
4. `000004_edges`     — `edges` table references `nodes(id)`.

The original SPEC-DM-01 §3 listed nodes/edges before trees, but nodes
FK-references trees(id) so trees must exist first. golang-migrate's
single-session, sequential up() honors the lexical ordering, so the
file numbering here is the legal execution order.

Each migration has paired `up`/`down` files in the format
`NNNNNN_name.{up,down}.sql` expected by golang-migrate's `iofs` source.
The `.up.sql` files are loaded by `internal/db.MigrateFS` via `embed.FS`.
