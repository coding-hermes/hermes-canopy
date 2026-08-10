/**
 * Topic Detection API client tests (TM-02).
 * Tests the API client functions with mocked api helpers.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import {
  confirmProposal,
  dismissProposal,
  getDetectionConfig,
  updateDetectionConfig,
} from '../topicDetectionApi';

// Mock the api helpers
vi.mock('../api', () => ({
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  apiPut: vi.fn(),
}));

import { apiGet, apiPost, apiPut } from '../api';

describe('topicDetectionApi', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe('confirmProposal', () => {
    it('sends empty titleOverride on accept', async () => {
      vi.mocked(apiPost).mockResolvedValue({
        topic: { id: 't1', title: 'Test', slug: 'test' },
      });

      await confirmProposal('prop-1');

      expect(apiPost).toHaveBeenCalledWith(
        '/topic-proposals/prop-1/confirm',
        { titleOverride: '' },
      );
    });

    it('sends titleOverride when provided', async () => {
      vi.mocked(apiPost).mockResolvedValue({
        topic: { id: 't1', title: 'Custom', slug: 'custom' },
      });

      await confirmProposal('prop-1', 'Custom Title');

      expect(apiPost).toHaveBeenCalledWith(
        '/topic-proposals/prop-1/confirm',
        { titleOverride: 'Custom Title' },
      );
    });

    it('sends empty override when title is empty string', async () => {
      vi.mocked(apiPost).mockResolvedValue({
        topic: { id: 't1', title: 'Auto', slug: 'auto' },
      });

      await confirmProposal('prop-1', '');

      expect(apiPost).toHaveBeenCalledWith(
        '/topic-proposals/prop-1/confirm',
        { titleOverride: '' },
      );
    });

    it('encodes proposalId in the URL', async () => {
      vi.mocked(apiPost).mockResolvedValue({
        topic: { id: 't1', title: 'T', slug: 't' },
      });

      await confirmProposal('prop/with/slashes');

      expect(apiPost).toHaveBeenCalledWith(
        '/topic-proposals/prop%2Fwith%2Fslashes/confirm',
        expect.anything(),
      );
    });
  });

  describe('dismissProposal', () => {
    it('sends POST to dismiss endpoint with no body', async () => {
      vi.mocked(apiPost).mockResolvedValue(undefined);

      await dismissProposal('prop-1');

      expect(apiPost).toHaveBeenCalledWith(
        '/topic-proposals/prop-1/dismiss',
        undefined,
      );
    });

    it('encodes proposalId', async () => {
      vi.mocked(apiPost).mockResolvedValue(undefined);

      await dismissProposal('prop special');

      expect(apiPost).toHaveBeenCalledWith(
        '/topic-proposals/prop%20special/dismiss',
        undefined,
      );
    });
  });

  describe('getDetectionConfig', () => {
    it('builds GET URL with treeId', async () => {
      vi.mocked(apiGet).mockResolvedValue({
        auto_create: false,
        always_ask: true,
        detection_level: 'full',
        min_messages_per_topic: 3,
        proposal_cooldown: 10,
      });

      await getDetectionConfig('tree-1');

      expect(apiGet).toHaveBeenCalledWith('/trees/tree-1/topic-detection');
    });
  });

  describe('updateDetectionConfig', () => {
    it('sends PUT with partial patch', async () => {
      vi.mocked(apiPut).mockResolvedValue({
        auto_create: true,
        always_ask: false,
        detection_level: 'full',
        min_messages_per_topic: 3,
        proposal_cooldown: 10,
      });

      await updateDetectionConfig('tree-1', { auto_create: true });

      expect(apiPut).toHaveBeenCalledWith(
        '/trees/tree-1/topic-detection',
        { auto_create: true },
      );
    });

    it('sends full config when all fields present', async () => {
      vi.mocked(apiPut).mockResolvedValue({
        auto_create: false,
        always_ask: true,
        detection_level: 'off',
        min_messages_per_topic: 5,
        proposal_cooldown: 20,
      });

      await updateDetectionConfig('tree-1', {
        auto_create: false,
        always_ask: true,
        detection_level: 'off',
        min_messages_per_topic: 5,
        proposal_cooldown: 20,
      });

      expect(apiPut).toHaveBeenCalledWith('/trees/tree-1/topic-detection', {
        auto_create: false,
        always_ask: true,
        detection_level: 'off',
        min_messages_per_topic: 5,
        proposal_cooldown: 20,
      });
    });
  });
});

// ── Confidence band helper tests ─────────────────────────────────────────

describe('confidenceBand', () => {
  it('classifies low confidence', async () => {
    const { confidenceBand } = await import('../../types/topic-detection');
    expect(confidenceBand(0.5)).toBe('low');
    expect(confidenceBand(0.64)).toBe('low');
    expect(confidenceBand(0)).toBe('low');
  });

  it('classifies medium confidence at 0.65 boundary', async () => {
    const { confidenceBand } = await import('../../types/topic-detection');
    expect(confidenceBand(0.65)).toBe('medium');
    expect(confidenceBand(0.7)).toBe('medium');
    expect(confidenceBand(0.85)).toBe('medium');
  });

  it('classifies high confidence above 0.85', async () => {
    const { confidenceBand } = await import('../../types/topic-detection');
    expect(confidenceBand(0.86)).toBe('high');
    expect(confidenceBand(0.95)).toBe('high');
    expect(confidenceBand(1.0)).toBe('high');
  });
});
