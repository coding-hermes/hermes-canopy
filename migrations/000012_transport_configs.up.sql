-- Transport configuration table.
-- Stores per-transport defaults and admin-overridable settings.
-- SPEC-FTR-04 §4.2.
CREATE TABLE transport_configs (
    transport_type   TEXT PRIMARY KEY CHECK (transport_type IN ('sse', 'webrtc', 'nats', 'redis', 'relay')),
    enabled          BOOLEAN NOT NULL DEFAULT true,
    max_message_size BIGINT NOT NULL,
    heartbeat_secs   INTEGER NOT NULL,
    connect_timeout  INTEGER NOT NULL DEFAULT 30,
    retry_max        INTEGER NOT NULL DEFAULT 3,
    config_json      JSONB NOT NULL DEFAULT '{}',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Seed default transport configurations.
INSERT INTO transport_configs (transport_type, max_message_size, heartbeat_secs, connect_timeout, retry_max) VALUES
    ('sse',    1048576, 15, 30, 3),
    ('webrtc', 262144,  30, 60, 2),
    ('nats',   1048576, 30, 30, 3),
    ('redis',  1048576, 30, 30, 3),
    ('relay',  1048576, 30, 30, 3)
ON CONFLICT (transport_type) DO NOTHING;
