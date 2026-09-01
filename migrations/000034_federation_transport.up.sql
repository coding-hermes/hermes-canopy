-- SPEC-FTR-02 P2. The server signing identity is durable; ECDH secrets remain
-- process-memory-only per section 4.3 and are renegotiated after restart.
CREATE TABLE federation_identity (
    singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
    public_key BYTEA NOT NULL,
    private_key BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE federation_peers ADD COLUMN signing_public_key BYTEA;
