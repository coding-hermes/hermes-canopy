-- SPEC-FTR-02 P4: durable, per-peer outbound event relay queue.
CREATE TABLE federation_events (
    event_id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tree_id           UUID NOT NULL REFERENCES trees(id) ON DELETE CASCADE,
    sender_profile_id UUID NOT NULL REFERENCES profiles(id),
    target_peer_id    UUID NOT NULL REFERENCES federation_peers(id) ON DELETE CASCADE,
    sequence_no       BIGINT NOT NULL,
    payload           JSONB NOT NULL,
    status            TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending', 'delivered', 'failed')),
    delivery_attempts INT NOT NULL DEFAULT 0,
    last_error        TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at      TIMESTAMPTZ,
    UNIQUE (target_peer_id, sequence_no)
);

CREATE INDEX idx_federation_events_delivery
    ON federation_events(target_peer_id, status, sequence_no);

CREATE TABLE federation_replay_cursors (
    peer_id       UUID PRIMARY KEY REFERENCES federation_peers(id) ON DELETE CASCADE,
    sequence_no   BIGINT NOT NULL DEFAULT 0,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Receiver-side event IDs make at-least-once transport exactly-once at the
-- local SSE boundary. The envelope itself remains in the sender's queue.
CREATE TABLE federation_event_receipts (
    event_id      UUID PRIMARY KEY,
    peer_id       UUID NOT NULL REFERENCES federation_peers(id) ON DELETE CASCADE,
    sequence_no   BIGINT NOT NULL,
    received_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
