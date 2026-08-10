-- 000030_topic_detection.up.sql
-- SPEC-TM-02 §8.1 — topic_proposals, topic_detection_config, subject_cooldowns.
-- Backs the auto-topic-detection engine: stores pending proposals, per-tree
-- configuration, and subject-key rejection cooldowns.

-- ── topic_proposals ──────────────────────────────────────────────────────

CREATE TABLE topic_proposals (
    id             uuid        PRIMARY KEY DEFAULT uuidv7(),
    tree_id        uuid        NOT NULL,
    root_node_id   uuid        NOT NULL,
    title          text        NOT NULL,
    description    text        NOT NULL DEFAULT '',
    detection_type text        NOT NULL,
    confidence     real        NOT NULL,
    subject_key    text        NOT NULL,
    status         text        NOT NULL DEFAULT 'pending',
    expires_at     timestamptz NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    resolved_at    timestamptz,
    evidence       jsonb       NOT NULL DEFAULT '{}'::jsonb,

    CONSTRAINT fk_proposal_tree
        FOREIGN KEY (tree_id) REFERENCES trees(id) ON DELETE CASCADE,
    CONSTRAINT fk_proposal_root_node
        FOREIGN KEY (root_node_id) REFERENCES nodes(id) ON DELETE CASCADE,
    CONSTRAINT chk_proposal_status
        CHECK (status IN ('pending', 'confirmed', 'dismissed', 'expired')),
    CONSTRAINT chk_proposal_detection_type
        CHECK (detection_type IN ('explicit', 'implicit', 'structural')),
    CONSTRAINT chk_proposal_confidence
        CHECK (confidence >= 0.0 AND confidence <= 1.0),
    CONSTRAINT chk_proposal_title_length
        CHECK (char_length(title) BETWEEN 1 AND 200)
);

CREATE INDEX idx_proposals_tree        ON topic_proposals(tree_id);
CREATE INDEX idx_proposals_tree_status ON topic_proposals(tree_id, status);
CREATE INDEX idx_proposals_root_node   ON topic_proposals(root_node_id);
CREATE INDEX idx_proposals_subject     ON topic_proposals(tree_id, subject_key);
CREATE INDEX idx_proposals_expires     ON topic_proposals(expires_at) WHERE status = 'pending';

-- ── topic_detection_config ──────────────────────────────────────────────

CREATE TABLE topic_detection_config (
    tree_id                uuid        PRIMARY KEY,
    auto_create            boolean     NOT NULL DEFAULT false,
    always_ask             boolean     NOT NULL DEFAULT true,
    detection_level        text        NOT NULL DEFAULT 'full',
    min_messages_per_topic integer     NOT NULL DEFAULT 3,
    proposal_cooldown      integer     NOT NULL DEFAULT 10,
    last_proposal_seq      bigint      NOT NULL DEFAULT 0,
    messages_since_proposal integer    NOT NULL DEFAULT 0,
    updated_at             timestamptz NOT NULL DEFAULT clock_timestamp(),

    CONSTRAINT fk_detection_config_tree
        FOREIGN KEY (tree_id) REFERENCES trees(id) ON DELETE CASCADE,
    CONSTRAINT chk_detection_level
        CHECK (detection_level IN ('off', 'explicit_only', 'full')),
    CONSTRAINT chk_min_messages
        CHECK (min_messages_per_topic >= 1),
    CONSTRAINT chk_proposal_cooldown
        CHECK (proposal_cooldown >= 0)
);

-- Seed default config rows for all existing trees (idempotent).
INSERT INTO topic_detection_config (tree_id)
SELECT id FROM trees
ON CONFLICT (tree_id) DO NOTHING;

-- ── subject_cooldowns ────────────────────────────────────────────────────
-- Records that a subject_key was rejected for a tree, suppressing repeated
-- proposals for the same subject until cooldown_until passes.

CREATE TABLE subject_cooldowns (
    tree_id        uuid        NOT NULL,
    subject_key    text        NOT NULL,
    cooldown_until timestamptz NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),

    PRIMARY KEY (tree_id, subject_key),
    CONSTRAINT fk_cooldown_tree
        FOREIGN KEY (tree_id) REFERENCES trees(id) ON DELETE CASCADE
);

CREATE INDEX idx_cooldown_until ON subject_cooldowns(cooldown_until);
