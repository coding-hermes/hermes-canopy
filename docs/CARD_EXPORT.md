# Card export — deterministic JSONL snapshots

`canopyd card export` reads the per-type card stores and writes **one compact
JSON object per card**, so card history can live in git and a human can diff two
exports. It is the export half of board row **GAP-083** (owner ruling
2026-09-16): append-log / export-class data gets JSONL + periodic snapshots,
while the mutable graph with write-time invariants stays in the queryable
storage it already has.

This phase is **export only** — there is no import path, no HTTP route, and no
change to how the runtime stores cards.

## Command

```bash
canopyd card export [flags]
```

| Flag | Meaning |
|------|---------|
| `--data-dir <dir>` | Card data directory. Default `$CANOPY_CARD_DATA_DIR`, else `~/.hermes/canopy/cards` |
| `--out <file>` | Write the export to `<file>` instead of stdout (same bytes) |
| `--snapshot-dir <dir>` | Write `<dir>/cards-<YYYY-MM-DD>.jsonl` (UTC date) atomically, then print the path on stdout |
| `-h`, `--help` | Print usage and exit `0` |

`--out` and `--snapshot-dir` are mutually exclusive.

Unlike `tree` / `session` / `topic`, `card export` is **not** an HTTP client: it
opens the local SQLite card stores in-process and never contacts
`CANOPY_SERVER_URL` or PostgreSQL.

## Record shape

One line per card, `\n`-terminated, no header and no blank lines:

```
{"record":"card","card_type":"compact","card":{…},"events":[{…}]}
```

* `card` uses the `card.Card` JSON tags (`id`, `tree_id`, `node_id`, `app_id`,
  `card_type`, `data`, `actions`, `status`, `context_hash`, `revision`,
  `created_at`, `updated_at`, and `dismissed_at` / `archived_at` when set).
* `events` uses the `card.CardEvent` JSON tags (`sequence`, `event_id`,
  `card_id`, `event_type`, `actor_kind`, `actor_id`, `payload`, `created_at`),
  ordered by `sequence` ascending. A card with no events carries `"events":[]`.
* `card_type` at the **record** level is the database **filename stem**, not a
  whitelist of the three built-in types: every `*.db` in the data directory is
  exported, so a store called `notes.db` appears as `"card_type":"notes"` rather
  than disappearing. The nested `card.card_type` is the value stored in the row.

### A real captured line

Captured 2026-09-17 19:35 local (UTC−05, so `2026-09-18T00:35Z`) by running the
`canopyd` binary built from this branch against the scratch data directory
described under [How the example was produced](#how-the-example-was-produced).
Long line, no wrapping in the file itself:

```json
{"record":"card","card_type":"compact","card":{"id":"11111111-1111-4111-8111-111111111111","tree_id":"aaaaaaaa-0000-4000-8000-000000000001","node_id":"bbbbbbbb-0000-4000-8000-000000000002","app_id":"http-files","card_type":"compact","data":{"file":"README.md","size":512,"kind":"markdown"},"actions":[{"label":"Open in viewer","handler":"open"}],"status":"active","context_hash":"ctx-2f9c1a","revision":1,"created_at":"2026-09-18T00:35:29.138491621Z","updated_at":"2026-09-18T00:35:29.138491621Z"},"events":[{"sequence":1,"event_id":"f64b3b8d-7b30-520c-ae6b-6019c563ad66","card_id":"11111111-1111-4111-8111-111111111111","event_type":"agent_progress","actor_kind":"agent","actor_id":"canopyd","payload":{"step":1,"label":"decorate"},"created_at":"2026-09-18T00:35:29.138737887Z"},{"sequence":2,"event_id":"62c18cd5-5b18-5f6d-8140-4fa5f63ca2a8","card_id":"11111111-1111-4111-8111-111111111111","event_type":"agent_progress","actor_kind":"agent","actor_id":"canopyd","payload":{"step":2,"label":"decorate"},"created_at":"2026-09-18T00:35:29.138839725Z"}]}
```

The same record, pretty-printed for reading only (the exporter never emits this
form):

```json
{
  "record": "card",
  "card_type": "compact",
  "card": {
    "id": "11111111-1111-4111-8111-111111111111",
    "app_id": "http-files",
    "card_type": "compact",
    "data": { "file": "README.md", "size": 512, "kind": "markdown" },
    "actions": [{ "label": "Open in viewer", "handler": "open" }],
    "status": "active",
    "context_hash": "ctx-2f9c1a",
    "revision": 1
  },
  "events": [
    { "sequence": 1, "event_type": "agent_progress", "actor_kind": "agent", "actor_id": "canopyd" }
  ]
}
```

Note the excerpt in the `expanded` store's data:
`"excerpt":"<b>Canopy</b> & the graph"` — `<`, `>` and `&` are emitted as
themselves. Go's encoder escapes them to `\u003c`, `\u003e`, `\u0026` by
default, and that churn on every line containing markup is exactly the diff
noise this format exists to remove, so the exporter encodes with
`SetEscapeHTML(false)`.

## Determinism guarantee

**Two exports of an unchanged store are byte-identical.** The format carries no
export timestamp, no hostname, no tool version and no absolute paths: the only
top-level keys are `record`, `card_type`, `card` and `events`.

Measured on the scratch store:

```
$ canopyd card export --data-dir /tmp/gap083-scratch | sha256sum
047c24090155cc7242247f4a38ce52e75f0b9774cdad55d5e62ed48335dc2681  -
$ canopyd card export --data-dir /tmp/gap083-scratch | sha256sum
047c24090155cc7242247f4a38ce52e75f0b9774cdad55d5e62ed48335dc2681  -
```

`--out` writes exactly the bytes stdout would have carried (`cmp` equal), and a
snapshot file has those same bytes.

### Ordering

* Databases are visited by filename stem, cards inside each by `id`
  — i.e. `(card_type, id)` overall.
* Each card's events are ordered by `sequence` ascending.

The order never depends on directory iteration order or on a map.

## Read-only guarantee

Every store is opened read-only, and the exporter never creates a database,
never migrates, and never leaves a file behind. Two measured details make that
true rather than aspirational (driver `modernc.org/sqlite`, SQLite 3.53.4):

1. **The `file:` prefix is load-bearing.** The Go driver hands the whole DSN to
   SQLite only when it starts with `file:`; otherwise it splits at `?` and
   consumes the query string itself, and `mode=ro` is silently dropped — the
   connection is then *read-write*. With the prefix, an `INSERT` and a
   `CREATE TABLE` on the exporter's connection both fail with
   `attempt to write a readonly database (8)`.
2. **`mode=ro` alone still writes.** Against a store whose WAL was already
   checkpointed — the shape after a clean close, `<type>.db` on its own — a
   `mode=ro` connection *creates* `<type>.db-shm` (32 KiB) and a zero-length
   `<type>.db-wal` and leaves them behind. So a store with no WAL in use is read
   with `immutable=1` instead: nothing is created, nothing is locked.
3. **`immutable=1` is not a general substitute.** Against a store whose
   committed rows are still in the WAL it is a *silent stale read*: opening the
   same store immutably answers `no such table: cards` because `immutable`
   promises the file can never change and SQLite then ignores the WAL entirely.

So the DSN is chosen from the store's own state: a store with a `-shm` or a
`-wal` holding frames is read through the WAL with `mode=ro` (its sidecars
already exist, so nothing is added), and a quiescent store is read immutably.
The only case that still creates a file is a leftover `-wal` holding frames with
no `-shm` (an unclean exit): the WAL must be read and SQLite rebuilds the index —
a repair of SQLite's own bookkeeping, never a change to card data.

What is *not* claimed: the `-shm` file is SQLite's shared-memory WAL index, not
card data, and **any** connection — read-only included — rewrites it while a WAL
store is open. On a store with a live WAL the main database and the `-wal` are
byte-identical across an export (verified on the live
`~/.hermes/canopy/cards` store), while `-shm` churns. On a quiescent store
nothing at all changes: same files, same bytes.

## Failure modes

| Situation | Behaviour |
|-----------|-----------|
| Store with no `cards` table, or not a SQLite database | exit `1`, naming the path and the reason — never an empty file standing in for a broken store |
| Data directory missing, or a path that is not a directory | exit `1`, naming the path |
| Snapshot directory missing | exit `1` (the exporter never creates directories) |
| Unknown flag, stray positional argument, `--out` with `--snapshot-dir` | usage on stderr, exit `2` |
| Empty data directory | zero bytes on stdout, exit `0` (no header, no trailing newline) |
| `--out` run that fails mid-export | the partial file is removed, exit `1` |

Two conditions the JSONL format cannot represent are reported on **stderr**
(never mixed into stdout, which carries export bytes only):

* an event whose `card_id` has no matching card row — excluded from the export
  (a record is keyed by its card) and counted in the warning;
* a timestamp that matches no known layout — exported as the zero time
  (`0001-01-01T00:00:00Z`) and counted, so the substitution is visible.

## Periodic snapshots

`--snapshot-dir` is the piece a timer uses: it writes
`cards-<YYYY-MM-DD>.jsonl` (UTC) through a `*.tmp` file in the same directory and
renames it into place, so a reader sees either the previous snapshot or the
complete new one — never a half-written file.

Into a git-tracked directory, daily at 03:17 local:

```cron
17 3 * * *  canopyd card export --snapshot-dir /srv/canopy-snapshots >>/var/log/canopy-export.log 2>&1
```

or as a systemd timer:

```ini
# /etc/systemd/system/canopy-card-snapshot.service
[Service]
Type=oneshot
ExecStart=/usr/local/bin/canopyd card export --snapshot-dir /srv/canopy-snapshots
# /etc/systemd/system/canopy-card-snapshot.timer
[Timer]
OnCalendar=*-*-* 03:17:00
Persistent=true
```

```bash
mkdir -p /srv/canopy-snapshots        # the exporter creates no directories
cd /srv/canopy-snapshots && git init
```

**The trade-off to decide deliberately.** The snapshot name is per UTC day, so a
store that changes every day produces one new file per day (a growing directory)
while a store that changes rarely produces a file whose diff is small — the
filename is a fixed key and a re-run overwrites that day's file with the newer
content, which is what makes "one file per day" a diffable history. Pick one:

* **Commit each day's file** (track them all): a complete, dated history in git;
  the repository grows by roughly the export size per day.
* **Commit only the current snapshot** (`echo 'cards-*.jsonl' > .gitignore`
  plus `git add -f`, or delete older snapshots in the same script): small
  repository, no long history — the file's diff shows what changed today, and
  yesterday's state is gone.

Either way the export is add-only from the store's perspective: reading it never
mutates the cards the running `canopyd` writes.

## How the example was produced

The scratch store was seeded **through the repository's own writer**
(`internal/card`: `NewCardDBManager` → `Repository` → `Create` → `AppendEvent`,
the same code path the server uses) with three cards — two `compact`, one
`expanded` — and four events, then the writer was closed (a clean close
checkpoints the WAL, leaving `<type>.db` alone). Then:

```bash
go build -o /tmp/canopyd-gap083 ./cmd/canopyd
/tmp/canopyd-gap083 card export --data-dir /tmp/gap083-scratch
```

The line quoted above is the first line of that output, verbatim. The same
command against the live `~/.hermes/canopy/cards` store (read-only, nothing
written into the repository) exported 5 cards and 5 events from 3 databases,
exit `0` — the live sanity read, not the example.

## Tests

```bash
go test -count=1 ./internal/cardexport/... ./internal/card/... ./cmd/canopyd/...
```

`internal/cardexport` covers determinism (two exports compared byte-for-byte),
the total order, an unknown-type stem, an empty and a missing data directory, a
database without a `cards` table, a non-database file, the two read-only
guarantees above (write refusal, plus the store's files and hashes before/after),
URI metacharacters in the data path, orphan events, empty JSON columns and the
timestamp layouts. The fixtures are built through the real writer on purpose:
that is what keeps the exporter's hand-written column list and row scanning in
sync with the schema the server actually writes.
