CREATE TABLE mls_group_members (
    group_id               BYTEA NOT NULL REFERENCES mls_groups(group_id) ON DELETE CASCADE,
    profile_id             UUID NOT NULL REFERENCES profiles(id) ON DELETE RESTRICT,
    mls_identity           BYTEA NOT NULL,
    encryption_pubkey      BYTEA NOT NULL,
    signature_pubkey       BYTEA NOT NULL,
    credential_type        TEXT NOT NULL,
    leaf_index             INTEGER NOT NULL CHECK (leaf_index >= 0),
    added_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_active            TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, profile_id),
    UNIQUE (group_id, leaf_index)
);

CREATE INDEX idx_mls_group_members_profile ON mls_group_members(profile_id);
CREATE INDEX idx_mls_group_members_inactive ON mls_group_members(group_id, last_active);
