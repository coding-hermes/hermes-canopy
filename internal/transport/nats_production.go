package transport

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

// ProductionBusConfig maps the NATS transport_configs row and process secrets
// to nats.go. Durations in the database are expressed in seconds.
type ProductionBusConfig struct {
	URL            string
	Credentials    string
	ConnectTimeout time.Duration
	RetryMax       int
	Heartbeat      time.Duration
}

type productionNATSBus struct {
	cfg      ProductionBusConfig
	options  []nats.Option
	conn     *nats.Conn
	onStatus func(ConnectionState, error)
}

// NewProductionBus validates configuration without opening a network
// connection. The adapter invokes Connect when the transport is first used.
func NewProductionBus(cfg ProductionBusConfig) (NATSClient, error) {
	if err := validateNATSURL(cfg.URL); err != nil {
		return nil, err
	}
	if cfg.Credentials != "" {
		info, err := os.Stat(cfg.Credentials)
		if err != nil {
			return nil, fmt.Errorf("transport: NATS credentials: %w", err)
		}
		if info.IsDir() {
			return nil, errors.New("transport: NATS credentials path is a directory")
		}
	}
	b := &productionNATSBus{cfg: cfg}
	b.options = buildNATSOptions(cfg, b.disconnected, b.reconnected)
	return b, nil
}

func buildNATSOptions(cfg ProductionBusConfig, disconnected nats.ConnErrHandler, reconnected nats.ConnHandler) []nats.Option {
	timeout := cfg.ConnectTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	heartbeat := cfg.Heartbeat
	if heartbeat <= 0 {
		heartbeat = 30 * time.Second
	}
	reconnectWait := heartbeat
	if reconnectWait > 32*time.Second {
		reconnectWait = 32 * time.Second
	}
	if reconnectWait < time.Second {
		reconnectWait = time.Second
	}
	opts := []nats.Option{
		nats.Timeout(timeout),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(cfg.RetryMax),
		nats.ReconnectWait(reconnectWait),
		nats.PingInterval(heartbeat),
		nats.DisconnectErrHandler(disconnected),
		nats.ReconnectHandler(reconnected),
	}
	if cfg.Credentials != "" {
		opts = append(opts, nats.UserCredentials(cfg.Credentials))
	}
	return opts
}

func (b *productionNATSBus) SetStatusHandler(handler func(ConnectionState, error)) {
	b.onStatus = handler
}

func (b *productionNATSBus) Connect(ctx context.Context, serverURL, bearer string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if serverURL == "" {
		serverURL = b.cfg.URL
	}
	if err := validateNATSURL(serverURL); err != nil {
		return err
	}
	opts := append([]nats.Option(nil), b.options...)
	if b.cfg.Credentials == "" && bearer != "" {
		opts = append(opts, nats.Token(bearer))
	}
	nc, err := nats.Connect(serverURL, opts...)
	if err != nil {
		return err
	}
	b.conn = nc
	log.Info().Str("transport", "nats").Str("endpoint_fingerprint", endpointFingerprint(serverURL)).Msg("NATS connected")
	return nil
}

func (b *productionNATSBus) Publish(_ context.Context, subject string, data []byte) error {
	if b.conn == nil || b.conn.IsClosed() {
		return ErrConnectionClosed
	}
	return b.conn.Publish(subject, data)
}

func (b *productionNATSBus) Subscribe(_ context.Context, subject string, handler func([]byte)) (NATSSubscription, error) {
	if b.conn == nil || b.conn.IsClosed() {
		return nil, ErrConnectionClosed
	}
	return b.conn.Subscribe(subject, func(msg *nats.Msg) { handler(msg.Data) })
}

func (b *productionNATSBus) Ping(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if b.conn == nil || b.conn.IsClosed() {
		return ErrConnectionClosed
	}
	return b.conn.FlushTimeout(b.cfg.ConnectTimeout)
}

func (b *productionNATSBus) Drain() error {
	if b.conn == nil || b.conn.IsClosed() {
		return nil
	}
	err := b.conn.Drain()
	log.Info().Str("transport", "nats").Msg("NATS drained")
	return err
}

func (b *productionNATSBus) disconnected(_ *nats.Conn, err error) {
	log.Warn().Err(err).Str("transport", "nats").Msg("NATS disconnected")
	if b.onStatus != nil {
		b.onStatus(StateDegraded, err)
	}
}

func (b *productionNATSBus) reconnected(nc *nats.Conn) {
	log.Info().Str("transport", "nats").Str("endpoint_fingerprint", endpointFingerprint(nc.ConnectedUrl())).Msg("NATS reconnected")
	if b.onStatus != nil {
		b.onStatus(StateActive, nil)
	}
}

func endpointFingerprint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "invalid"
	}
	sum := sha256.Sum256([]byte(u.Scheme + "://" + u.Host))
	return fmt.Sprintf("%x", sum[:8])
}

var _ NATSClient = (*productionNATSBus)(nil)
