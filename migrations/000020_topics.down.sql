DROP TABLE IF EXISTS topic_members CASCADE;
DROP FUNCTION IF EXISTS refresh_topic_node_count;
DROP VIEW IF EXISTS topic_member_nodes;
DROP TRIGGER IF EXISTS trg_topic_search_vector ON topics;
DROP FUNCTION IF EXISTS update_topic_search_vector;
DROP FUNCTION IF EXISTS generate_topic_slug;
DROP TABLE IF EXISTS topics CASCADE;
