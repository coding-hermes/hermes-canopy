package server

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/coding-hermes/hermes-canopy/internal/federation"
	"github.com/coding-hermes/hermes-canopy/internal/telemetry"
)

const saturationMetricsInterval = 15 * time.Second

// federationQueueSource is the narrow seam needed to compute an aggregate
// backlog without exposing peer identifiers as metric labels.
type federationQueueSource interface {
	ListAllPeers(context.Context) ([]*federation.FederationPeer, error)
	QueueDepth(context.Context, uuid.UUID) (int, error)
}

func aggregateFederationQueueDepth(ctx context.Context, source federationQueueSource) (int, error) {
	peers, err := source.ListAllPeers(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, peer := range peers {
		if peer == nil {
			continue
		}
		depth, err := source.QueueDepth(ctx, peer.ID)
		if err != nil {
			return 0, err
		}
		total += depth
	}
	return total, nil
}

func runSaturationMetrics(ctx context.Context, metrics *telemetry.Metrics, pool *pgxpool.Pool, federation federationQueueSource) {
	refresh := func() {
		if pool != nil {
			stat := pool.Stat()
			metrics.SetDBPoolSnapshot(int(stat.AcquiredConns()), int(stat.MaxConns()))
		}
		if federation != nil {
			sampleCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			total, err := aggregateFederationQueueDepth(sampleCtx, federation)
			if err == nil {
				metrics.SetFederationQueueDepth(total)
			}
		}
	}

	refresh()
	ticker := time.NewTicker(saturationMetricsInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}
