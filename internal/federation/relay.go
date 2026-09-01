package federation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

const (
	DefaultRelayChunkSize = 512 * 1024
	DefaultReplayLimit    = 100
	MaxReplayLimit        = 500
	DefaultFailureLimit   = 5
	MaxPeerQueueDepth     = 10000
)

type RelayStatus string

const (
	RelayPending   RelayStatus = "pending"
	RelayDelivered RelayStatus = "delivered"
	RelayFailed    RelayStatus = "failed"
)

// RelayEvent is the durable logical event. Payload is an encoded FTLEnvelope.
type RelayEvent struct {
	EventID          uuid.UUID       `json:"event_id"`
	TreeID           uuid.UUID       `json:"tree_id"`
	SenderProfileID  uuid.UUID       `json:"sender_profile_id"`
	TargetPeerID     uuid.UUID       `json:"target_peer_id"`
	SequenceNo       int64           `json:"sequence_no"`
	Payload          json.RawMessage `json:"payload"`
	Status           RelayStatus     `json:"status"`
	DeliveryAttempts int             `json:"delivery_attempts"`
	LastError        *string         `json:"last_error,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	DeliveredAt      *time.Time      `json:"delivered_at,omitempty"`
}

// RelayFrame is the HTTP wire unit. Small events use one frame; large events
// carry the same event ID and are reassembled before signature verification.
type RelayFrame struct {
	EventID      uuid.UUID `json:"event_id"`
	PeerID       uuid.UUID `json:"peer_id"`
	SequenceNo   int64     `json:"sequence_no"`
	ChunkSeq     int       `json:"chunk_seq"`
	ChunkTotal   int       `json:"chunk_total"`
	ChunkPayload []byte    `json:"chunk_payload"`
}

type RelayRepository interface {
	Enqueue(context.Context, *RelayEvent) (*RelayEvent, error)
	Replay(context.Context, uuid.UUID, int64, int) ([]RelayEvent, error)
	Pending(context.Context, uuid.UUID, int) ([]RelayEvent, error)
	MarkAttempt(context.Context, uuid.UUID, error, bool) error
	Cursor(context.Context, uuid.UUID) (int64, error)
	AdvanceCursor(context.Context, uuid.UUID, int64) error
	RecordReceipt(context.Context, uuid.UUID, uuid.UUID, int64) (bool, error)
}

type queueDepthRepository interface {
	QueueDepth(context.Context, uuid.UUID) (int, error)
}

type PGRelayRepository struct{ pool *pgxpool.Pool }

func NewPGRelayRepository(pool *pgxpool.Pool) *PGRelayRepository {
	return &PGRelayRepository{pool: pool}
}

const relayColumns = `event_id, tree_id, sender_profile_id, target_peer_id, sequence_no,
payload, status, delivery_attempts, last_error, created_at, delivered_at`

func scanRelay(row pgx.Row) (*RelayEvent, error) {
	var event RelayEvent
	if err := row.Scan(&event.EventID, &event.TreeID, &event.SenderProfileID, &event.TargetPeerID,
		&event.SequenceNo, &event.Payload, &event.Status, &event.DeliveryAttempts, &event.LastError,
		&event.CreatedAt, &event.DeliveredAt); err != nil {
		return nil, err
	}
	return &event, nil
}

func (r *PGRelayRepository) Enqueue(ctx context.Context, event *RelayEvent) (*RelayEvent, error) {
	if event.EventID == uuid.Nil {
		event.EventID = uuid.New()
	}
	result, err := scanRelay(r.pool.QueryRow(ctx, `INSERT INTO federation_events
		(event_id,tree_id,sender_profile_id,target_peer_id,sequence_no,payload)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING `+relayColumns,
		event.EventID, event.TreeID, event.SenderProfileID, event.TargetPeerID, event.SequenceNo, event.Payload))
	if err != nil {
		return nil, err
	}
	_, err = r.pool.Exec(ctx, `DELETE FROM federation_events WHERE event_id IN (
		SELECT event_id FROM federation_events WHERE target_peer_id=$1
		ORDER BY (status <> 'delivered') DESC, sequence_no DESC OFFSET $2)`, event.TargetPeerID, MaxPeerQueueDepth)
	return result, err
}

func (r *PGRelayRepository) QueueDepth(ctx context.Context, peerID uuid.UUID) (int, error) {
	var depth int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM federation_events
		WHERE target_peer_id=$1 AND status IN ('pending','failed')`, peerID).Scan(&depth)
	return depth, err
}

func (r *PGRelayRepository) list(ctx context.Context, query string, args ...any) ([]RelayEvent, error) {
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]RelayEvent, 0)
	for rows.Next() {
		event, err := scanRelay(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, *event)
	}
	return events, rows.Err()
}

func (r *PGRelayRepository) Replay(ctx context.Context, peerID uuid.UUID, cursor int64, limit int) ([]RelayEvent, error) {
	return r.list(ctx, `SELECT `+relayColumns+` FROM federation_events
		WHERE target_peer_id=$1 AND sequence_no>$2 ORDER BY sequence_no LIMIT $3`, peerID, cursor, limit)
}

func (r *PGRelayRepository) Pending(ctx context.Context, peerID uuid.UUID, limit int) ([]RelayEvent, error) {
	return r.list(ctx, `SELECT `+relayColumns+` FROM federation_events
		WHERE target_peer_id=$1 AND status IN ('pending','failed') ORDER BY sequence_no LIMIT $2`, peerID, limit)
}

func (r *PGRelayRepository) MarkAttempt(ctx context.Context, eventID uuid.UUID, deliveryErr error, delivered bool) error {
	if delivered {
		_, err := r.pool.Exec(ctx, `UPDATE federation_events SET status='delivered', delivery_attempts=delivery_attempts+1,
			last_error=NULL, delivered_at=now() WHERE event_id=$1`, eventID)
		return err
	}
	message := "delivery failed"
	if deliveryErr != nil {
		message = deliveryErr.Error()
	}
	_, err := r.pool.Exec(ctx, `UPDATE federation_events SET status='failed', delivery_attempts=delivery_attempts+1,
		last_error=$2 WHERE event_id=$1`, eventID, message)
	return err
}

func (r *PGRelayRepository) Cursor(ctx context.Context, peerID uuid.UUID) (int64, error) {
	var cursor int64
	err := r.pool.QueryRow(ctx, `SELECT sequence_no FROM federation_replay_cursors WHERE peer_id=$1`, peerID).Scan(&cursor)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return cursor, err
}

func (r *PGRelayRepository) AdvanceCursor(ctx context.Context, peerID uuid.UUID, cursor int64) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO federation_replay_cursors(peer_id,sequence_no) VALUES($1,$2)
		ON CONFLICT(peer_id) DO UPDATE SET sequence_no=GREATEST(federation_replay_cursors.sequence_no,$2),updated_at=now()`, peerID, cursor)
	return err
}

func (r *PGRelayRepository) RecordReceipt(ctx context.Context, eventID, peerID uuid.UUID, sequence int64) (bool, error) {
	tag, err := r.pool.Exec(ctx, `INSERT INTO federation_event_receipts(event_id,peer_id,sequence_no)
		VALUES($1,$2,$3) ON CONFLICT(event_id) DO NOTHING`, eventID, peerID, sequence)
	return err == nil && tag.RowsAffected() == 1, err
}

type RelayService struct {
	queue        RelayRepository
	peers        Repository
	client       *http.Client
	token        func(context.Context, *FederationPeer) (string, error)
	chunkSize    int
	failureLimit int
}

func NewRelayService(queue RelayRepository, peers Repository, client *http.Client,
	token func(context.Context, *FederationPeer) (string, error)) *RelayService {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &RelayService{queue: queue, peers: peers, client: client, token: token,
		chunkSize: DefaultRelayChunkSize, failureLimit: DefaultFailureLimit}
}

func (s *RelayService) Enqueue(ctx context.Context, peerID uuid.UUID, envelope *FTLEnvelope) (*RelayEvent, error) {
	if envelope == nil || peerID == uuid.Nil {
		return nil, ErrInvalidInput
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	return s.queue.Enqueue(ctx, &RelayEvent{EventID: uuid.New(), TreeID: envelope.TreeID,
		SenderProfileID: envelope.SenderProfileID, TargetPeerID: peerID,
		SequenceNo: envelope.Sequence, Payload: payload})
}

// Dispatch persists before attempting the network delivery.
func (s *RelayService) Dispatch(ctx context.Context, peerID uuid.UUID, envelope *FTLEnvelope) (*RelayEvent, error) {
	event, err := s.Enqueue(ctx, peerID, envelope)
	if err != nil {
		return nil, err
	}
	if err := s.DeliverPending(ctx, peerID); err != nil {
		return event, fmt.Errorf("%w: %v", ErrPeerOffline, err)
	}
	return event, nil
}

func (s *RelayService) Replay(ctx context.Context, peerID uuid.UUID, cursor int64, limit int) ([]RelayEvent, error) {
	Metrics().IncReplay()
	if limit <= 0 {
		limit = DefaultReplayLimit
	}
	if limit > MaxReplayLimit {
		limit = MaxReplayLimit
	}
	return s.queue.Replay(ctx, peerID, cursor, limit)
}

func (s *RelayService) RecordReceipt(ctx context.Context, eventID, peerID uuid.UUID, sequence int64) (bool, error) {
	return s.queue.RecordReceipt(ctx, eventID, peerID, sequence)
}

func (s *RelayService) QueueDepth(ctx context.Context, peerID uuid.UUID) (int, error) {
	repo, ok := s.queue.(queueDepthRepository)
	if !ok {
		return 0, nil
	}
	return repo.QueueDepth(ctx, peerID)
}

func (s *RelayService) Frames(event RelayEvent) []RelayFrame {
	size := s.chunkSize
	if size <= 0 {
		size = DefaultRelayChunkSize
	}
	total := (len(event.Payload) + size - 1) / size
	if total == 0 {
		total = 1
	}
	frames := make([]RelayFrame, 0, total)
	for i := 0; i < total; i++ {
		start, end := i*size, (i+1)*size
		if end > len(event.Payload) {
			end = len(event.Payload)
		}
		frames = append(frames, RelayFrame{event.EventID, event.TargetPeerID, event.SequenceNo, i + 1, total, event.Payload[start:end]})
	}
	return frames
}

func (s *RelayService) DeliverPending(ctx context.Context, peerID uuid.UUID) error {
	peer, err := s.peers.Get(ctx, peerID)
	if err != nil {
		return err
	}
	events, err := s.queue.Pending(ctx, peerID, DefaultReplayLimit)
	if err != nil {
		return err
	}
	if len(events) > 0 {
		log.Info().Str("peer_id", peerID.String()).Int("replay_volume", len(events)).Msg("federation replay batch")
	}
	for _, event := range events {
		if err = s.deliver(ctx, peer, event); err != nil {
			_ = s.queue.MarkAttempt(ctx, event.EventID, err, false)
			if event.DeliveryAttempts+1 >= s.failureLimit {
				_ = s.peers.SetState(ctx, peerID, PeerDisconnected, nil, nil)
				log.Warn().Str("peer_id", peerID.String()).Int("failure_count", event.DeliveryAttempts+1).Msg("federation peer unreachable")
			}
			return err // causal order: never pass a failed sequence
		}
		if err = s.queue.MarkAttempt(ctx, event.EventID, nil, true); err != nil {
			return err
		}
		if err = s.queue.AdvanceCursor(ctx, peerID, event.SequenceNo); err != nil {
			return err
		}
	}
	return nil
}

func (s *RelayService) deliver(ctx context.Context, peer *FederationPeer, event RelayEvent) error {
	token, err := s.token(ctx, peer)
	if err != nil {
		return err
	}
	for _, frame := range s.Frames(event) {
		body, _ := json.Marshal(frame)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(peer.ServerURL, "/")+"/api/v1/federation/events", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := s.client.Do(req)
		if err != nil {
			Metrics().RecordError()
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
			Metrics().RecordError()
			return fmt.Errorf("federation relay: peer ACK status %d", resp.StatusCode)
		}
	}
	Metrics().IncRelayed()
	return nil
}

// Run retries all active peers with capped exponential backoff and jitter.
func (s *RelayService) Run(ctx context.Context) {
	backoff := time.Second
	for {
		peers, err := s.peers.List(ctx, nil, true)
		failed := err != nil
		if err == nil {
			for _, peer := range peers {
				if peer.State == PeerRevoked || peer.State == PeerQuarantined {
					continue
				}
				if deliveryErr := s.DeliverPending(ctx, peer.ID); deliveryErr != nil {
					failed = true
				}
			}
		}
		if !failed {
			backoff = time.Second
		} else if backoff < 30*time.Second {
			backoff *= 2
		}
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
		jitter := time.Duration(rand.Int64N(int64(backoff/2 + 1)))
		timer := time.NewTimer(backoff + jitter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
