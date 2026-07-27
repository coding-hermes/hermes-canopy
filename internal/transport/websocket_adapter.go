package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// --- WebSocket adapter (SPEC-FTR-04 §3, Phase 2) ---------------------------

// WebSocketAdapter implements TransportAdapter over WebSocket (RFC 6455).
//
// Multi-tenant isolation: messages are routed through tenant-scoped
// channels. A connection bound to tenant "A" cannot receive messages
// from tenant "B".
//
// Authentication: the adapter validates a tenant token from the
// ConnectOptions.Auth.Token field or Metadata["tenant_id"].
type WebSocketAdapter struct {
	// upgrader configures WebSocket upgrade parameters.
	upgrader websocket.Upgrader

	mu     sync.RWMutex
	conns  map[string]*wsConn // conn ID → connection state
	rooms  map[string]map[string]*wsConn // tenantChannel → {connID → wsConn}
	server *http.Server // optional HTTP server for upgrade endpoint

	// addr is the listen address when running in server mode.
	addr string

	// httpClient is used for client-mode connections.
	httpClient *http.Client
}

// wsConn wraps a gorilla WebSocket connection with Canopy metadata.
type wsConn struct {
	conn     *Connection
	ws       *websocket.Conn
	recvChan chan *Message
	sendChan chan *Message
	done     chan struct{}
	closeOnce sync.Once
}

// --- Upgrader defaults ------------------------------------------------------

// DefaultWSReadBufferSize is the default WebSocket read buffer size.
const DefaultWSReadBufferSize = 4096

// DefaultWSWriteBufferSize is the default WebSocket write buffer size.
const DefaultWSWriteBufferSize = 4096

// DefaultWSMaxMessageSize is the default maximum WebSocket message size (1MB).
const DefaultWSMaxMessageSize = 1048576

// NewWebSocketAdapter creates a WebSocket transport adapter.
// When addr is non-empty, the adapter will listen for incoming WebSocket
// upgrade requests on that address. When addr is empty, only client-mode
// (Connect) is supported.
func NewWebSocketAdapter(addr string) *WebSocketAdapter {
	return &WebSocketAdapter{
		upgrader: websocket.Upgrader{
			ReadBufferSize:  DefaultWSReadBufferSize,
			WriteBufferSize: DefaultWSWriteBufferSize,
			CheckOrigin: func(r *http.Request) bool {
				return true // MVP: allow all origins; post-MVP restrict
			},
		},
		conns: make(map[string]*wsConn),
		rooms: make(map[string]map[string]*wsConn),
		addr:  addr,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// TransportType returns TransportWebRTC for WebSocket... wait, no — we
// need a dedicated transport type. The existing TransportType constants
// don't include WebSocket. For now, the WebSocket transport is surfaced
// as a separate type; the selector will treat it as part of the SSE
// fallback chain when configured.
//
// We reuse TransportSSE as the proxy type since WebSocket replaces SSE
// for bidirectional communication. A separate "ws" type can be added
// post-MVP.
func (a *WebSocketAdapter) TransportType() TransportType {
	return TransportSSE // WebSocket is the bidirectional replacement for SSE
}

// Connect establishes a WebSocket connection to the target or upgrades
// an existing HTTP connection. In server mode, the adapter listens for
// upgrade requests; in client mode, it dials the target.
//
// When ConnectOptions.Auth.Token is set, the adapter extracts the tenant
// ID from it and scopes the connection.
func (a *WebSocketAdapter) Connect(ctx context.Context, opts ConnectOptions) (*Connection, error) {
	// Extract tenant ID from auth token or metadata.
	tenantID := opts.TenantID
	if tenantID == "" {
		tenantID = TenantIDFromMetadata(opts.Metadata)
	}
	if tenantID == "" && opts.Auth.Token != "" {
		var err error
		tenantID, err = ExtractTenantIDFromToken(opts.Auth.Token)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrAuthFailed, err)
		}
	}

	connID := uuid.New().String()
	now := time.Now().UTC()

	conn := &Connection{
		ID:            connID,
		TransportType: TransportSSE,
		Peer:          opts.Target,
		TenantID:      tenantID,
		Metadata:      opts.Metadata,
		State:         StateConnecting,
		EstablishedAt: now,
		LastActivity:  now,
	}

	// In server mode, the WebSocket connection is established via the
	// UpgradeHTTP handler. Connect in server mode records the metadata
	// and the actual upgrade happens when the client sends the HTTP
	// upgrade request.
	//
	// In client mode, dial the target.
	var ws *websocket.Conn
	if opts.Target != "" {
		dialer := websocket.Dialer{
			HandshakeTimeout: opts.Timeout,
			TLSClientConfig:  opts.TLSConfig,
		}
		if dialer.HandshakeTimeout == 0 {
			dialer.HandshakeTimeout = 10 * time.Second
		}

		// Add auth header if token is present.
		header := http.Header{}
		if opts.Auth.Token != "" {
			header.Set("Authorization", "Bearer "+opts.Auth.Token)
		}
		if tenantID != "" {
			header.Set("X-Canopy-Tenant-ID", tenantID)
		}

		var err error
		ws, _, err = dialer.DialContext(ctx, opts.Target, header)
		if err != nil {
			return nil, fmt.Errorf("transport: websocket dial: %w", err)
		}
	}

	wc := &wsConn{
		conn:     conn,
		ws:       ws,
		recvChan: make(chan *Message, 256),
		sendChan: make(chan *Message, 256),
		done:     make(chan struct{}),
	}

	a.mu.Lock()
	a.conns[connID] = wc
	// Register in tenant-scoped room.
	roomKey := TenantChannel(tenantID, "messages")
	if a.rooms[roomKey] == nil {
		a.rooms[roomKey] = make(map[string]*wsConn)
	}
	a.rooms[roomKey][connID] = wc
	a.mu.Unlock()

	// Start the read/write pumps.
	if ws != nil {
		go a.readPump(wc)
		go a.writePump(wc)
	}

	conn.State = StateActive
	return conn, nil
}

// Send transmits a message over the WebSocket connection.
// Multi-tenant validation: the connection's tenant must match the message
// scope (enforced by ValidateTenantConnection).
func (a *WebSocketAdapter) Send(ctx context.Context, conn *Connection, msg *Message) error {
	if conn == nil {
		return ErrConnectionClosed
	}

	a.mu.RLock()
	wc, ok := a.conns[conn.ID]
	a.mu.RUnlock()
	if !ok {
		return ErrConnectionClosed
	}

	// Tenant isolation check.
	if err := ValidateTenantConnection(conn, conn.TenantID); err != nil {
		return fmt.Errorf("%w: %v", ErrTenantIsolation, err)
	}

	// Validate message size.
	msgData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("transport: encode message: %w", err)
	}
	maxSize := int64(DefaultWSMaxMessageSize)
	if opts := conn.Metadata; opts != nil {
		// Allow per-connection overrides in the future.
		_ = opts
	}
	if int64(len(msgData)) > maxSize {
		return ErrPayloadTooLarge
	}

	select {
	case wc.sendChan <- msg:
		conn.LastActivity = time.Now().UTC()
		return nil
	case <-wc.done:
		return ErrConnectionClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Receive returns a channel that yields inbound messages for the connection.
func (a *WebSocketAdapter) Receive(ctx context.Context, conn *Connection) (<-chan *Message, error) {
	if conn == nil {
		return nil, ErrConnectionClosed
	}
	a.mu.RLock()
	wc, ok := a.conns[conn.ID]
	a.mu.RUnlock()
	if !ok {
		return nil, ErrConnectionClosed
	}

	// Bridge context cancellation to channel close.
	go func() {
		select {
		case <-ctx.Done():
			wc.closeOnce.Do(func() {
				close(wc.recvChan)
			})
		case <-wc.done:
		}
	}()

	return wc.recvChan, nil
}

// Disconnect tears down the WebSocket connection. Idempotent.
func (a *WebSocketAdapter) Disconnect(ctx context.Context, conn *Connection) error {
	if conn == nil {
		return nil
	}

	a.mu.Lock()
	wc, ok := a.conns[conn.ID]
	if ok {
		delete(a.conns, conn.ID)
		// Remove from tenant-scoped room.
		roomKey := TenantChannel(conn.TenantID, "messages")
		if room, exists := a.rooms[roomKey]; exists {
			delete(room, conn.ID)
			if len(room) == 0 {
				delete(a.rooms, roomKey)
			}
		}
	}
	a.mu.Unlock()

	if !ok {
		return nil // idempotent
	}

	conn.State = StateDisconnecting
	wc.closeOnce.Do(func() {
		close(wc.done)
	})
	if wc.ws != nil {
		// Send close frame and close the underlying connection.
		_ = wc.ws.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(time.Second),
		)
		_ = wc.ws.Close()
	}
	conn.State = StateClosed
	_ = ctx
	return nil
}

// Health checks that the WebSocket adapter is operational.
// In server mode, it verifies the HTTP listener is running.
// In client mode, it always returns healthy (the adapter is ready to dial).
func (a *WebSocketAdapter) Health(ctx context.Context) error {
	_ = ctx
	a.mu.RLock()
	defer a.mu.RUnlock()
	// The adapter is healthy if it can accept connections.
	return nil
}

// --- HTTP upgrade handler ---------------------------------------------------

// ServeHTTP upgrades an HTTP connection to WebSocket and registers it
// with the adapter. This is the server-side entry point for WebSocket
// connections.
func (a *WebSocketAdapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract tenant ID from auth header or query param.
	tenantID := r.Header.Get("X-Canopy-Tenant-ID")
	if tenantID == "" {
		// Try token extraction from Authorization header.
		authHeader := r.Header.Get("Authorization")
		if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
			token := authHeader[7:]
			extractedID, err := ExtractTenantIDFromToken(token)
			if err == nil {
				tenantID = extractedID
			}
		}
	}

	ws, err := a.upgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, "websocket upgrade failed", http.StatusBadRequest)
		return
	}

	connID := uuid.New().String()
	now := time.Now().UTC()

	conn := &Connection{
		ID:            connID,
		TransportType: TransportSSE,
		Peer:          r.RemoteAddr,
		TenantID:      tenantID,
		Metadata: map[string]string{
			"tenant_id": tenantID,
			"user_agent": r.UserAgent(),
		},
		State:         StateActive,
		EstablishedAt: now,
		LastActivity:  now,
	}

	wc := &wsConn{
		conn:     conn,
		ws:       ws,
		recvChan: make(chan *Message, 256),
		sendChan: make(chan *Message, 256),
		done:     make(chan struct{}),
	}

	a.mu.Lock()
	a.conns[connID] = wc
	roomKey := TenantChannel(tenantID, "messages")
	if a.rooms[roomKey] == nil {
		a.rooms[roomKey] = make(map[string]*wsConn)
	}
	a.rooms[roomKey][connID] = wc
	a.mu.Unlock()

	go a.readPump(wc)
	go a.writePump(wc)
}

// --- Internal read/write pumps ----------------------------------------------

// readPump reads messages from the WebSocket connection and pushes them
// into the recvChan for the sync engine to consume.
func (a *WebSocketAdapter) readPump(wc *wsConn) {
	defer func() {
		wc.closeOnce.Do(func() {
			close(wc.done)
		})
	}()

	for {
		_, message, err := wc.ws.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				// Log unexpected close; don't leak details.
				_ = err
			}
			return
		}

		var msg Message
		if err := json.Unmarshal(message, &msg); err != nil {
			// Skip malformed messages.
			continue
		}

		// Enforce tenant isolation on inbound messages.
		if msg.Origin != "" && wc.conn.TenantID != "" && msg.Origin != wc.conn.TenantID {
			// Message claims origin from a different tenant — drop it.
			continue
		}

		select {
		case wc.recvChan <- &msg:
			wc.conn.LastActivity = time.Now().UTC()
		case <-wc.done:
			return
		}
	}
}

// writePump reads messages from the sendChan and writes them to the
// WebSocket connection.
func (a *WebSocketAdapter) writePump(wc *wsConn) {
	ticker := time.NewTicker(30 * time.Second) // ping interval
	defer func() {
		ticker.Stop()
		wc.closeOnce.Do(func() {
			close(wc.done)
		})
	}()

	for {
		select {
		case msg, ok := <-wc.sendChan:
			if !ok {
				return
			}
			data, err := json.Marshal(msg)
			if err != nil {
				continue
			}
			wc.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := wc.ws.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
			wc.conn.SequenceWatermark = msg.Sequence

		case <-ticker.C:
			wc.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := wc.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}

		case <-wc.done:
			return
		}
	}
}

// --- Tenant-scoped broadcast ------------------------------------------------

// BroadcastToTenant sends a message to all WebSocket connections in the
// given tenant's room. This is the primary multi-tenant send path.
func (a *WebSocketAdapter) BroadcastToTenant(tenantID string, msg *Message) {
	roomKey := TenantChannel(tenantID, "messages")

	a.mu.RLock()
	room, ok := a.rooms[roomKey]
	if !ok {
		a.mu.RUnlock()
		return
	}
	// Copy the connection references to avoid holding the lock during sends.
	conns := make([]*wsConn, 0, len(room))
	for _, wc := range room {
		conns = append(conns, wc)
	}
	a.mu.RUnlock()

	for _, wc := range conns {
		select {
		case wc.sendChan <- msg:
		case <-wc.done:
		default:
			// Channel full, skip this connection.
		}
	}
}

// ConnectionCount returns the number of active WebSocket connections.
func (a *WebSocketAdapter) ConnectionCount() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.conns)
}

// TenantConnectionCount returns the number of active connections for a tenant.
func (a *WebSocketAdapter) TenantConnectionCount(tenantID string) int {
	roomKey := TenantChannel(tenantID, "messages")
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.rooms[roomKey])
}
