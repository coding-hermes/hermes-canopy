package relay

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"
)

const reconnectBackoffMax = 30 * time.Second

const (
	ClientDisconnected = "disconnected"
	ClientConnecting   = "connecting"
	ClientConnected    = "connected"
	ClientBackoff      = "backoff"
)

// RelayClient maintains the outbound-only side of the symmetric relay protocol.
type RelayClient struct {
	mu         sync.RWMutex
	conn       net.Conn
	state      string
	lastError  error
	auth       *FrameAuthenticator
	keyID      uint16
	handler    func(Frame)
	cancel     context.CancelFunc
	done       chan struct{}
	writeMu    sync.Mutex
	backoffMax time.Duration
	stopping   bool
}

func NewRelayClient(cfg DeploymentConfig) *RelayClient {
	return &RelayClient{
		state: ClientDisconnected, auth: NewFrameAuthenticator(uint16(cfg.HMACKeyID), cfg.HMACKey, uint16(cfg.HMACKeyPrevID), cfg.HMACKeyPrev),
		keyID: uint16(cfg.HMACKeyID), backoffMax: reconnectBackoffMax,
	}
}

func (c *RelayClient) SetDataHandler(handler func(Frame)) {
	c.mu.Lock()
	c.handler = handler
	c.mu.Unlock()
}

func (c *RelayClient) Start(ctx context.Context, cfg DeploymentConfig) error {
	u, err := url.Parse(cfg.ConnectAddr)
	if err != nil || (u.Scheme != "tcp" && u.Scheme != "tls" && u.Scheme != "https" && u.Scheme != "wss") {
		return fmt.Errorf("relay: client requires tcp, tls, https, or wss connect address")
	}
	useTLS := cfg.TLSEnabled || u.Scheme == "tls" || u.Scheme == "https" || u.Scheme == "wss"
	var tlsCfg *tls.Config
	if useTLS {
		host, _, splitErr := net.SplitHostPort(u.Host)
		if splitErr != nil {
			return splitErr
		}
		tlsCfg, err = relayClientTLSConfig(cfg, host)
		if err != nil {
			return err
		}
	}
	c.mu.Lock()
	if c.cancel != nil {
		c.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	c.cancel, c.done = cancel, make(chan struct{})
	c.mu.Unlock()
	go c.run(runCtx, u.Host, cfg, tlsCfg, c.done)
	return nil
}

func (c *RelayClient) run(ctx context.Context, addr string, cfg DeploymentConfig, tlsCfg *tls.Config, done chan struct{}) {
	defer close(done)
	backoff := time.Second
	for {
		c.setState(ClientConnecting, nil)
		var conn net.Conn
		var err error
		if tlsCfg != nil {
			conn, err = (&tls.Dialer{NetDialer: &net.Dialer{}, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
		} else {
			conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		}
		if err == nil {
			err = c.serve(ctx, conn, cfg)
		}
		c.mu.RLock()
		stopping := c.stopping
		c.mu.RUnlock()
		if errors.Is(err, errServerBye) || ctx.Err() != nil || stopping {
			c.setState(ClientDisconnected, nil)
			return
		}
		c.setState(ClientBackoff, err)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			c.setState(ClientDisconnected, nil)
			return
		case <-timer.C:
		}
		backoff *= 2
		if backoff > c.backoffMax {
			backoff = c.backoffMax
		}
	}
}

var errServerBye = errors.New("relay: server sent BYE")

func (c *RelayClient) serve(ctx context.Context, conn net.Conn, cfg DeploymentConfig) error {
	c.setConn(conn)
	defer func() { c.setConn(nil); _ = conn.Close() }()
	if err := c.write(conn, Frame{Type: FrameHello, Payload: cfg.InstanceID[:]}); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(helloTimeout))
	ack, err := ReadFrame(conn)
	if err != nil {
		return err
	}
	if ack.Type != FrameHelloAck || c.auth.Verify(ack) != nil {
		return ErrAuthFailed
	}
	_ = conn.SetReadDeadline(time.Time{})
	c.setState(ClientConnected, nil)

	frames := make(chan Frame)
	readErr := make(chan error, 1)
	go func() {
		for {
			frame, err := ReadFrame(conn)
			if err != nil {
				readErr <- err
				return
			}
			if err := c.auth.Verify(frame); err != nil {
				readErr <- err
				return
			}
			select {
			case frames <- frame:
			case <-ctx.Done():
				return
			}
		}
	}()
	ticker := time.NewTicker(time.Duration(cfg.HeartbeatSecs) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			return err
		case frame := <-frames:
			switch frame.Type {
			case FrameBye:
				return errServerBye
			case FrameData:
				c.mu.RLock()
				handler := c.handler
				c.mu.RUnlock()
				if handler != nil {
					handler(frame)
				}
			}
		case <-ticker.C:
			if err := c.write(conn, Frame{Type: FramePing}); err != nil {
				return err
			}
		}
	}
}

func (c *RelayClient) write(conn net.Conn, frame Frame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	frame.KeyID = c.keyID
	signed, err := c.auth.Sign(frame)
	if err != nil {
		return err
	}
	b, err := EncodeFrame(signed)
	if err != nil {
		return err
	}
	_, err = conn.Write(b)
	return err
}

func (c *RelayClient) setConn(conn net.Conn) { c.mu.Lock(); c.conn = conn; c.mu.Unlock() }
func (c *RelayClient) setState(state string, err error) {
	c.mu.Lock()
	c.state, c.lastError = state, err
	c.mu.Unlock()
}
func (c *RelayClient) ClientHealth() (string, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	last := ""
	if c.lastError != nil {
		last = c.lastError.Error()
	}
	return c.state, last
}

func (c *RelayClient) StopAccepting() error {
	c.mu.Lock()
	c.stopping = true
	c.mu.Unlock()
	return nil
}
func (c *RelayClient) NotifyShutdown() error {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()
	if conn == nil {
		return nil
	}
	return c.write(conn, Frame{Type: FrameBye})
}
func (c *RelayClient) ActiveSessions() int {
	state, _ := c.ClientHealth()
	if state == ClientConnected {
		return 1
	}
	return 0
}
func (c *RelayClient) DrainDone() <-chan struct{} {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.done != nil {
		return c.done
	}
	done := make(chan struct{})
	close(done)
	return done
}
func (c *RelayClient) Close() error {
	c.mu.RLock()
	cancel, conn, done := c.cancel, c.conn, c.done
	c.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.Close()
	}
	if done != nil {
		<-done
	}
	c.mu.Lock()
	c.cancel = nil
	c.mu.Unlock()
	return nil
}
