/**
 * Topic Search API client tests (TM-03).
 * Tests the API client functions with mocked fetch.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { searchTopics, getRecentTopics, getTopicPreview, injectContext } from '../topicSearchApi';

// Mock the api helpers
vi.mock('../api', () => ({
  apiGet: vi.fn(),
  apiPost: vi.fn(),
}));

import { apiGet, apiPost } from '../api';

describe('topicSearchApi', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe('searchTopics', () => {
    it('builds correct URL with query params', async () => {
      vi.mocked(apiGet).mockResolvedValue({ results: [], total: 0, query_time_ms: 0 });

      await searchTopics('tree-123', 'schema design', {
        status: 'all',
        limit: 10,
        offset: 5,
        sort: 'title',
      });

      expect(apiGet).toHaveBeenCalledWith(
        expect.stringContaining('/trees/tree-123/topics/search'),
      );
      const url = vi.mocked(apiGet).mock.calls[0][0];
      expect(url).toContain('q=schema+design');
      expect(url).toContain('status=all');
      expect(url).toContain('limit=10');
      expect(url).toContain('offset=5');
      expect(url).toContain('sort=title');
    });

    it('sends minimal query when no options', async () => {
      vi.mocked(apiGet).mockResolvedValue({ results: [], total: 0, query_time_ms: 0 });

      await searchTopics('tree-1', 'test');

      const url = vi.mocked(apiGet).mock.calls[0][0];
      expect(url).toContain('q=test');
    });
  });

  describe('getRecentTopics', () => {
    it('builds URL with limit', async () => {
      vi.mocked(apiGet).mockResolvedValue({ topics: [] });

      await getRecentTopics('tree-1', 5);

      expect(apiGet).toHaveBeenCalledWith('/trees/tree-1/topics/recent?limit=5');
    });

    it('omits limit when not provided', async () => {
      vi.mocked(apiGet).mockResolvedValue({ topics: [] });

      await getRecentTopics('tree-1');

      expect(apiGet).toHaveBeenCalledWith('/trees/tree-1/topics/recent');
    });
  });

  describe('getTopicPreview', () => {
    it('builds preview URL', async () => {
      vi.mocked(apiGet).mockResolvedValue({
        topic_id: 't1',
        title: 'Test',
        snippets: [],
        participant_count: 0,
        node_count: 0,
        last_active_at: '',
        last_active_rel: '',
      });

      await getTopicPreview('tree-1', 'topic-1');

      expect(apiGet).toHaveBeenCalledWith('/trees/tree-1/topics/topic-1/preview');
    });
  });

  describe('injectContext', () => {
    it('sends POST with topic IDs', async () => {
      vi.mocked(apiPost).mockResolvedValue({
        context: {
          topics: [],
          merged_text: '',
          total_nodes: 0,
          truncated: false,
        },
        event_id: 'evt-1',
      });

      await injectContext('tree-1', {
        topic_ids: ['topic-1', 'topic-2'],
        max_nodes: 500,
      });

      expect(apiPost).toHaveBeenCalledWith(
        '/trees/tree-1/context/inject',
        { topic_ids: ['topic-1', 'topic-2'], max_nodes: 500 },
      );
    });
  });
});
