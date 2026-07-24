-- Transport events table (append-only audit log).
-- Records connection state transitions, errors, degradation events, and rate-limit hits.
-- SPEC-FTR-04 §4.3.
CREATE TABLE transport_events (
    id              UUID PRIMARY KEY DEFAULT uuidv7(),
    connection_id   UUID REFERENCES transport_connections(id) ON DELETE SET NULL,
    transport_type  TEXT NOT NULL CHECK (transport_type IN ('sse', 'webrtc', 'nats', 'redis', 'relay')),
    event_type      TEXT NOT NULL CHECK (event_type IN (
                        'connected', 'disconnected', 'degraded', 'recovered',
                        'auth_failed', 'sequence_gap', 'rate_limited',
                        'fallback_activated', 'message_dropped', 'health_failed'
                    )),
    peer_id         TEXT,
    details         JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_transport_events_connection ON transport_events(connection_id, created_at);
CREATE INDEX idx_transport_events_type ON transport_events(event_type, created_at);
CREATE INDEX idx_transport_events_transport ON transport_events(transport_type, created_at);
