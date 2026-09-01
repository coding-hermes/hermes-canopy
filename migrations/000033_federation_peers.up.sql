-- SPEC-FTR-02 §6.1, Phase P1: tree-scoped federation peer links.
CREATE TABLE federation_peers (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_url        TEXT NOT NULL,
    signing_key_fp    TEXT NOT NULL,
    ecdhe_public_key  BYTEA,
    role              INT NOT NULL DEFAULT 0,
    state             INT NOT NULL DEFAULT 0,
    tree_id           UUID NOT NULL REFERENCES trees(id) ON DELETE CASCADE,
    created_by        UUID NOT NULL REFERENCES profiles(id),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    connected_at      TIMESTAMPTZ,
    last_heartbeat    TIMESTAMPTZ,
    revoked_at        TIMESTAMPTZ,
    revoke_reason     TEXT,
    UNIQUE (server_url, tree_id)
);

CREATE INDEX idx_fed_peers_state ON federation_peers(state);
CREATE INDEX idx_fed_peers_tree ON federation_peers(tree_id);
