CREATE TABLE mls_groups (
    group_id          BYTEA PRIMARY KEY,
    workspace_id      UUID NOT NULL UNIQUE REFERENCES workspaces(id) ON DELETE CASCADE,
    cipher_suite      TEXT NOT NULL,
    epoch             BIGINT NOT NULL DEFAULT 0 CHECK (epoch >= 0),
    tree_hash_bytes   BYTEA NOT NULL,
    encrypted_state   JSONB NOT NULL DEFAULT '{}',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ck_mls_groups_state_object CHECK (jsonb_typeof(encrypted_state) = 'object')
);

CREATE INDEX idx_mls_groups_workspace ON mls_groups(workspace_id);
CREATE INDEX idx_mls_groups_epoch ON mls_groups(workspace_id, epoch);
