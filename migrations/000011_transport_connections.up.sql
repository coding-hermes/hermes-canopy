-- Transport connections table.
-- Tracks every active and historical transport connection across all transport types.
-- SPEC-FTR-04 §4.1.
CREATE TABLE transport_connections (
    id              UUID PRIMARY KEY DEFAULT uuidv7(),
    peer_id         TEXT NOT NULL,
    transport_type  TEXT NOT NULL CHECK (transport_type IN ('sse', 'webrtc', 'nats', 'redis', 'relay')),
    state           TEXT NOT NULL DEFAULT 'init'
                        CHECK (state IN ('init', 'connecting', 'active', 'degraded', 'disconnecting', 'closed')),
    target          TEXT NOT NULL,
    established_at  TIMESTAMPTZ,
    last_activity   TIMESTAMPTZ NOT NULL DEFAULT now(),
    sequence_high   BIGINT NOT NULL DEFAULT 0,
    metadata        JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_transport_connections_peer ON transport_connections(peer_id, transport_type);
CREATE INDEX idx_transport_connections_state ON transport_connections(state)
    WHERE state IN ('active', 'degraded');
CREATE INDEX idx_transport_connections_transport ON transport_connections(transport_type);
