package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/gateway"
	"github.com/coding-hermes/hermes-canopy/internal/service"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// gatewayOutputSink is the composition-root adapter between the gateway's
// plain-data output contract and Canopy's node service. Profile resolution is
// cached after the first call so every reply uses the same dev-hermes author.
type gatewayOutputSink struct {
	nodeSvc service.NodeService
	pool    *pgxpool.Pool

	profileOnce sync.Once
	profileID   uuid.UUID
	profileErr  error
}

func newGatewayOutputSink(nodeSvc service.NodeService, pool *pgxpool.Pool) gateway.RunOutputSink {
	if nodeSvc == nil || pool == nil {
		return nil
	}
	return &gatewayOutputSink{nodeSvc: nodeSvc, pool: pool}
}

func (s *gatewayOutputSink) devProfileID(ctx context.Context) (uuid.UUID, error) {
	s.profileOnce.Do(func() {
		s.profileErr = s.pool.QueryRow(ctx, `
			SELECT id
			FROM profiles
			WHERE owner_id = $1 AND name = $2 AND deleted_at IS NULL
			LIMIT 1`, db.DevJWTUserID, db.DevProfileName).Scan(&s.profileID)
		if s.profileErr != nil {
			s.profileErr = fmt.Errorf("lookup %q profile: %w", db.DevProfileName, s.profileErr)
		}
	})
	return s.profileID, s.profileErr
}

func (s *gatewayOutputSink) PersistRunOutput(ctx context.Context, in gateway.PersistRunOutputInput) error {
	parentID, err := uuid.Parse(in.SourceNodeID)
	if err != nil {
		return fmt.Errorf("parse source node id %q: %w", in.SourceNodeID, err)
	}
	profileID, err := s.devProfileID(ctx)
	if err != nil {
		return err
	}

	replyMetadata, err := json.Marshal(map[string]string{
		"run_id": in.RunID,
		"origin": "gateway_run",
	})
	if err != nil {
		return fmt.Errorf("marshal reply metadata: %w", err)
	}
	if _, err := s.nodeSvc.Reply(ctx, parentID, service.ReplyInput{
		Content:       in.Output,
		ContentFormat: string(service.NodeFormatMarkdown),
		NodeType:      string(service.NodeKindMessage),
		AuthorID:      profileID,
		Metadata:      replyMetadata,
	}); err != nil {
		return fmt.Errorf("create gateway reply node: %w", err)
	}

	source, err := s.nodeSvc.GetByID(ctx, parentID)
	if err != nil {
		return fmt.Errorf("load source node metadata: %w", err)
	}
	metadata := make(map[string]json.RawMessage)
	if len(source.Metadata) > 0 {
		if err := json.Unmarshal(source.Metadata, &metadata); err != nil {
			return fmt.Errorf("decode source node metadata: %w", err)
		}
	}
	if metadata == nil {
		metadata = make(map[string]json.RawMessage)
	}
	completedAt := time.Now().UTC().Format(time.RFC3339Nano)
	lastRun, err := json.Marshal(map[string]string{
		"run_id":       in.RunID,
		"completed_at": completedAt,
	})
	if err != nil {
		return fmt.Errorf("marshal source run metadata: %w", err)
	}
	metadata["last_gateway_run"] = lastRun
	mergedMetadata, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshal source node metadata: %w", err)
	}
	if _, err := s.nodeSvc.Update(ctx, parentID, service.UpdateNodeInput{
		Metadata: func() *json.RawMessage {
			raw := json.RawMessage(mergedMetadata)
			return &raw
		}(),
	}); err != nil {
		return fmt.Errorf("link source node to gateway run: %w", err)
	}
	return nil
}

var _ gateway.RunOutputSink = (*gatewayOutputSink)(nil)
