-- 000027_reference_resolution_cache.up.sql
-- SPEC-TM-04 §3.2: Caches compiled TopicContext payloads for referenced topics.
-- Invalidated by trigger (000029) when nodes inside the topic scope change.
-- Entries expire after 24h even without changes (spec §8.4).

CREATE TABLE reference_resolution_cache (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    topic_id        uuid        NOT NULL UNIQUE,
    tree_id         uuid        NOT NULL,
    context_hash    text        NOT NULL,
    node_count      integer     NOT NULL,
    -- JSONB payload mirrors the search.TopicContext structure.
    payload         jsonb       NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at      timestamptz NOT NULL DEFAULT clock_timestamp() + interval '24 hours',
    hit_count       integer     NOT NULL DEFAULT 0,
    CONSTRAINT fk_rrc_topic
        FOREIGN KEY (topic_id) REFERENCES topics(id)
        ON DELETE CASCADE,
    CONSTRAINT fk_rrc_tree
        FOREIGN KEY (tree_id) REFERENCES trees(id)
        ON DELETE CASCADE
);

CREATE INDEX idx_rrc_tree_id        ON reference_resolution_cache(tree_id);
CREATE INDEX idx_rrc_expires_at     ON reference_resolution_cache(expires_at);
CREATE INDEX idx_rrc_topic_lookup   ON reference_resolution_cache(topic_id, context_hash);
