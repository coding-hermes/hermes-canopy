-- FTR-04 Phase 3. The IF NOT EXISTS clauses support installations that
-- already received the Phase 1 transport schema in 000011/000012.
CREATE TABLE IF NOT EXISTS transport_connections (
    id UUID PRIMARY KEY DEFAULT uuidv7(), peer_id TEXT NOT NULL,
    transport_type TEXT NOT NULL CHECK (transport_type IN ('sse','webrtc','nats','redis','relay')),
    state TEXT NOT NULL DEFAULT 'init' CHECK (state IN ('init','connecting','active','degraded','disconnecting','closed')),
    target TEXT NOT NULL, established_at TIMESTAMPTZ,
    last_activity TIMESTAMPTZ NOT NULL DEFAULT now(), sequence_high BIGINT NOT NULL DEFAULT 0,
    metadata JSONB NOT NULL DEFAULT '{}', created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_transport_connections_peer ON transport_connections(peer_id,transport_type);
CREATE INDEX IF NOT EXISTS idx_transport_connections_state ON transport_connections(state) WHERE state IN ('active','degraded');
CREATE INDEX IF NOT EXISTS idx_transport_connections_transport ON transport_connections(transport_type);

CREATE TABLE IF NOT EXISTS transport_configs (
    transport_type TEXT PRIMARY KEY CHECK (transport_type IN ('sse','webrtc','nats','redis','relay')),
    enabled BOOLEAN NOT NULL DEFAULT true, max_message_size BIGINT NOT NULL,
    heartbeat_secs INTEGER NOT NULL, connect_timeout INTEGER NOT NULL DEFAULT 30,
    retry_max INTEGER NOT NULL DEFAULT 3, config_json JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO transport_configs (transport_type,max_message_size,heartbeat_secs,connect_timeout,retry_max,config_json) VALUES
 ('sse',1048576,15,30,3,'{"priority":10}'::jsonb),
 ('nats',1048576,30,30,3,'{"priority":20}'::jsonb)
ON CONFLICT (transport_type) DO UPDATE SET config_json=transport_configs.config_json || EXCLUDED.config_json;
