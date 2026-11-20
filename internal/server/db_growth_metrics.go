package server

import (
	"context"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"

	"github.com/coding-hermes/hermes-canopy/internal/relay"
	"github.com/coding-hermes/hermes-canopy/internal/telemetry"
)

// dbGrowthMetricsInterval is how often the data-growth and relay-certificate
// gauges are refreshed. Row estimates move on the scale of days, and the relay
// certificate on the scale of weeks, so 60s is far inside the resolution either
// question needs while keeping the query off the interactive path.
const dbGrowthMetricsInterval = 60 * time.Second

// dbGrowthSampleTimeout bounds one sampling cycle so a slow catalog query or a
// wedged certificate read can never pin the sampler goroutine.
const dbGrowthSampleTimeout = 5 * time.Second

// dbGrowthTables is the CLOSED set of table names canopy_db_largest_table_rows
// may ever carry as a `table` label value.
//
// The brief for OBS-5 named cards/bodies/edges/events/messages, but only `edges`
// exists in canopy's PostgreSQL schema — `cards`/`events` live in the separate
// card-export SQLite database, and there is no bodies or messages table. The
// list below is the real content set instead (all verified against
// migrations/*.up.sql): `nodes` holds what the product calls cards (id, type,
// content body, metadata), `edges` the graph links, `tree_events` the append-only
// mutation log, `tree_snapshots` the versioned snapshot payloads, and
// `federation_events` the durable per-peer outbound message queue.
//
// Nothing outside this list may ever reach the gauge. Add a table here (and to
// the guard test's expectations) — never interpolate a name from the query.
var dbGrowthTables = []string{
	"nodes",
	"edges",
	"tree_events",
	"tree_snapshots",
	"federation_events",
}

// isDBGrowthTable reports whether name is one of the allowlisted tables.
func isDBGrowthTable(name string) bool {
	for _, table := range dbGrowthTables {
		if table == name {
			return true
		}
	}
	return false
}

// dbRowEstimateQuery reads PostgreSQL's own planner statistics. reltuples is
// maintained by VACUUM/ANALYZE and is an estimate, which is exactly right for a
// growth gauge: counting rows of a large append-only table on every scrape would
// put a full scan on the interactive database for a number that only needs to be
// roughly right. relkind = 'r' keeps views/partitions out; the namespace filter
// keeps same-named tables from another schema out.
const dbRowEstimateQuery = `SELECT c.relname, c.reltuples::float8
	FROM pg_class c
	JOIN pg_namespace n ON n.oid = c.relnamespace
	WHERE n.nspname = 'public' AND c.relkind = 'r' AND c.relname = ANY($1)`

// tableRowEstimate is one row of the catalog result.
type tableRowEstimate struct {
	Table string
	Rows  float64
}

// queryTableRowEstimates runs the catalog query for tables. It returns only the
// rows PostgreSQL reported; filtering to the allowlist happens in
// dblargestTableRows so no caller can widen the label set.
func queryTableRowEstimates(ctx context.Context, pool *pgxpool.Pool, tables []string) ([]tableRowEstimate, error) {
	if pool == nil {
		return nil, nil
	}
	rows, err := pool.Query(ctx, dbRowEstimateQuery, tables)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var estimates []tableRowEstimate
	for rows.Next() {
		var estimate tableRowEstimate
		if err := rows.Scan(&estimate.Table, &estimate.Rows); err != nil {
			return nil, err
		}
		estimates = append(estimates, estimate)
	}
	return estimates, rows.Err()
}

// dblargestTableRows maps catalog rows onto the gauge's label set.
//
// Every allowlisted table gets an entry — a table the query did not report is
// published as 0 rather than left to the stale value from an earlier cycle, so
// "the table is gone/empty" and "the sampler stopped seeing it" cannot look the
// same. Anything outside the allowlist is dropped even if the query returned it,
// which is what keeps the `table` label bounded.
//
// reltuples is -1 (and can be any negative sentinel) for a table that has never
// been analyzed, and can arrive as NaN from a degenerate statistic. Neither is a
// row count: they clamp to 0 so a dashboard never plots -1 or NaN.
func dblargestTableRows(estimates []tableRowEstimate) map[string]float64 {
	out := make(map[string]float64, len(dbGrowthTables))
	for _, table := range dbGrowthTables {
		out[table] = 0
	}
	for _, estimate := range estimates {
		if !isDBGrowthTable(estimate.Table) {
			continue
		}
		out[estimate.Table] = clampRowEstimate(estimate.Rows)
	}
	return out
}

// clampRowEstimate turns a raw reltuples reading into a publishable value.
func clampRowEstimate(rows float64) float64 {
	if math.IsNaN(rows) || math.IsInf(rows, 0) || rows < 0 {
		return 0
	}
	return rows
}

// publishTableRowEstimates maps a catalog result onto the gauge and publishes it.
// It is the sampler's single write path for the growth series, so the allowlist
// filter and the clamping are unavoidable for any caller.
func publishTableRowEstimates(metrics *telemetry.Metrics, estimates []tableRowEstimate) {
	for table, rows := range dblargestTableRows(estimates) {
		metrics.SetDBLargestTableRows(table, rows)
	}
}

// dbGrowthRelayHealth is the narrow seam for the key-rotation counter: the same
// RelayHealth value /health serves. Nothing else about the relay is needed.
type dbGrowthRelayHealth interface {
	RelayHealth() relay.RelayHealth
}

// relayCertExpiryFunc reads the relay TLS leaf certificate's notAfter. It is a
// function rather than an interface so the sampler stays independent of which
// relay handle the composition root could reach.
type relayCertExpiryFunc func(context.Context) (time.Time, error)

// relayRotationCounter converts a monotonic-per-process source value into
// counter deltas.
//
// The first observation publishes the value already accumulated (rotations that
// happened before the first tick are still rotations). A source value that goes
// BACKWARDS means the relay state was replaced — a counter cannot decrease, so
// the baseline rebases and nothing is published for that cycle.
type relayRotationCounter struct {
	last   uint64
	seeded bool
}

func (c *relayRotationCounter) observe(current uint64) uint64 {
	if !c.seeded {
		c.seeded = true
		c.last = current
		return current
	}
	if current >= c.last {
		delta := current - c.last
		c.last = current
		return delta
	}
	c.last = current
	return 0
}

// relayCertExpiryFrom picks the certificate source for the sampler: the health
// probe when it exposes the live relay service, otherwise the persisted relay
// deployment config through the pool.
//
// cmd/canopyd's healthProbe exposes RelayHealth() but not the relay service
// itself, and the composition root is outside this change's file scope, so the
// pool-backed source is what production actually uses: main persists
// relay_config before constructing the relay service, so the row the sampler
// reads is the configuration the running relay was started from.
func relayCertExpiryFrom(probe any, pool *pgxpool.Pool) relayCertExpiryFunc {
	if src, ok := probe.(interface{ CertificateExpiry() (time.Time, error) }); ok {
		return func(context.Context) (time.Time, error) { return src.CertificateExpiry() }
	}
	if pool != nil {
		return relayConfigCertExpiry(pool)
	}
	return nil
}

// relayConfigCertExpiry reads the persisted relay deployment config and reports
// the leaf certificate expiry from it. It reuses relay's own config loader and
// certificate parser — the sampler never re-implements either.
//
// This is the source production actually uses. cmd/canopyd's healthProbe adapts
// the relay service but exposes only RelayHealth(), and the composition root is
// outside this change's file scope. Reading the row is not a guess: main loads
// relay_config, applies the relay-* flag overrides and SAVES it before
// constructing the relay service (cmd/canopyd/main.go), and nothing else
// rewrites relay_config at runtime except PersistHMACRotation (which touches
// rotation metadata only). So the certificate the sampler reads is the one the
// running relay presents.
func relayConfigCertExpiry(pool *pgxpool.Pool) relayCertExpiryFunc {
	return func(ctx context.Context) (time.Time, error) {
		cfg, err := relay.NewDeploymentConfigManager().Load(ctx, pool)
		if err != nil {
			return time.Time{}, err
		}
		return relay.CertificateExpiry(cfg)
	}
}

// sampleDBGrowthOnce runs one sampling cycle: row estimates, rotation delta and
// certificate expiry. Pulled out of the loop so tests can drive a single cycle
// without waiting on the ticker.
func sampleDBGrowthOnce(
	ctx context.Context,
	metrics *telemetry.Metrics,
	pool *pgxpool.Pool,
	rotations dbGrowthRelayHealth,
	certExpiry relayCertExpiryFunc,
	rotationState *relayRotationCounter,
) {
	sampleCtx, cancel := context.WithTimeout(ctx, dbGrowthSampleTimeout)
	defer cancel()

	if pool != nil {
		estimates, err := queryTableRowEstimates(sampleCtx, pool, dbGrowthTables)
		if err != nil {
			log.Warn().Err(err).Msg("metrics: db growth sample failed")
		} else {
			publishTableRowEstimates(metrics, estimates)
		}
	}

	if rotations != nil {
		metrics.AddRelayKeyRotations(rotationState.observe(rotations.RelayHealth().Rotations))
	}

	if certExpiry != nil {
		notAfter, err := certExpiry(sampleCtx)
		if err != nil {
			// No cert configured, or an unreadable/unparseable one: skip the
			// gauge instead of publishing a zero expiry. /health already
			// reports the degraded reason for the broken cases.
			log.Debug().Err(err).Msg("metrics: relay certificate expiry unavailable")
		} else {
			metrics.SetRelayCertExpiry(notAfter)
		}
	}
}

// runDBGrowthMetrics samples data growth and relay certificate state until ctx
// is cancelled.
//
// It mirrors runSaturationMetrics: one narrow source per question, published
// through telemetry, and a failed read leaves the previous value in place rather
// than zeroing a dashboard. The certificate gauge is the exception — "no
// certificate configured" is an absent series, never a zero.
func runDBGrowthMetrics(
	ctx context.Context,
	metrics *telemetry.Metrics,
	pool *pgxpool.Pool,
	rotations dbGrowthRelayHealth,
	certExpiry relayCertExpiryFunc,
) {
	var rotationState relayRotationCounter

	sampleDBGrowthOnce(ctx, metrics, pool, rotations, certExpiry, &rotationState)
	ticker := time.NewTicker(dbGrowthMetricsInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sampleDBGrowthOnce(ctx, metrics, pool, rotations, certExpiry, &rotationState)
		}
	}
}
