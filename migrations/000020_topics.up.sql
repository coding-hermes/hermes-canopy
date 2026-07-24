-- 000020_topics.up.sql
-- Spec: SPEC-TM-01 §3

CREATE TABLE topics (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    tree_id         uuid        NOT NULL,
    root_node_id    uuid        NOT NULL,
    title           text        NOT NULL,
    description     text        NOT NULL DEFAULT '',
    slug            text        NOT NULL,
    parent_topic_id uuid        REFERENCES topics(id) ON DELETE SET NULL,
    status          text        NOT NULL DEFAULT 'active',
    topic_tags      text[]      NOT NULL DEFAULT '{}',
    search_vector   tsvector,
    node_count      integer     NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    archived_at     timestamptz,
    deleted_at      timestamptz,
    CONSTRAINT fk_topics_tree
        FOREIGN KEY (tree_id) REFERENCES trees(id) ON DELETE CASCADE,
    CONSTRAINT fk_topics_root_node
        FOREIGN KEY (root_node_id, tree_id) REFERENCES nodes(id, tree_id) ON DELETE CASCADE,
    CONSTRAINT uq_topic_tree_slug UNIQUE (tree_id, slug),
    CONSTRAINT uq_topic_tree_title UNIQUE (tree_id, LOWER(title)),
    CONSTRAINT chk_topic_status CHECK (status IN ('active', 'archived', 'deleted')),
    CONSTRAINT chk_topic_title_length CHECK (char_length(title) BETWEEN 1 AND 200),
    CONSTRAINT chk_topic_slug_length CHECK (char_length(slug) BETWEEN 1 AND 256)
);

CREATE INDEX idx_topics_tree_id         ON topics(tree_id);
CREATE INDEX idx_topics_tree_status     ON topics(tree_id, status);
CREATE INDEX idx_topics_root_node       ON topics(root_node_id);
CREATE INDEX idx_topics_parent          ON topics(parent_topic_id);
CREATE INDEX idx_topics_status          ON topics(status);
CREATE INDEX idx_topics_created         ON topics(tree_id, created_at DESC);
CREATE INDEX idx_topics_tags            ON topics USING gin(topic_tags);
CREATE INDEX idx_topics_search          ON topics USING gin(search_vector);

CREATE OR REPLACE FUNCTION generate_topic_slug(title text) RETURNS text AS $$
BEGIN
    RETURN lower(
        regexp_replace(
            regexp_replace(trim(title), '[^a-zA-Z0-9\s-]', '', 'g'),
            '\s+', '-', 'g'
        )
    );
END;
$$ LANGUAGE plpgsql IMMUTABLE;

CREATE OR REPLACE FUNCTION update_topic_search_vector() RETURNS trigger AS $$
BEGIN
    NEW.search_vector := to_tsvector('english', COALESCE(NEW.title, '') || ' ' || COALESCE(NEW.description, ''));
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_topic_search_vector
    BEFORE INSERT OR UPDATE OF title, description ON topics
    FOR EACH ROW EXECUTE FUNCTION update_topic_search_vector();

CREATE VIEW topic_member_nodes AS
WITH RECURSIVE topic_scope AS (
    SELECT t.id AS topic_id, n.id AS node_id, n.tree_id, 0 AS depth
    FROM topics t JOIN nodes n ON n.id = t.root_node_id AND n.tree_id = t.tree_id
    WHERE n.deleted_at IS NULL
    UNION ALL
    SELECT ts.topic_id, n.id AS node_id, n.tree_id, ts.depth + 1
    FROM topic_scope ts
    JOIN edges e ON e.source_id = ts.node_id AND e.deleted_at IS NULL AND e.edge_type IN ('reply', 'fork')
    JOIN nodes n ON n.id = e.target_id AND n.deleted_at IS NULL
)
SELECT topic_id, node_id, tree_id, depth FROM topic_scope;

CREATE OR REPLACE FUNCTION refresh_topic_node_count(topic_id uuid) RETURNS integer AS $$
DECLARE
    cnt integer;
BEGIN
    SELECT COUNT(*) INTO cnt FROM topic_member_nodes WHERE topic_id = refresh_topic_node_count.topic_id;
    UPDATE topics SET node_count = cnt WHERE id = topic_id;
    RETURN cnt;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE topic_members (
    topic_id    uuid        NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    profile_id  uuid        NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    role        text        NOT NULL DEFAULT 'viewer',
    joined_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (topic_id, profile_id),
    CONSTRAINT chk_topic_member_role CHECK (role IN ('viewer', 'contributor', 'manager'))
);

CREATE INDEX idx_topic_members_profile ON topic_members(profile_id);
