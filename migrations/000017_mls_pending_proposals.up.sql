CREATE TABLE mls_pending_proposals (
    id             UUID PRIMARY KEY,
    group_id       BYTEA NOT NULL REFERENCES mls_groups(group_id) ON DELETE CASCADE,
    proposal_bytes BYTEA NOT NULL,
    proposal_type  TEXT NOT NULL CHECK (proposal_type IN ('add', 'remove', 'update', 'external_add')),
    proposer_id    UUID NOT NULL REFERENCES profiles(id) ON DELETE RESTRICT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_mls_pending_proposals_group_created
    ON mls_pending_proposals(group_id, created_at);
