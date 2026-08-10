// Package reference — concrete service implementation.
// Backed by ReferenceRepo + search.TopicSearchRepo (for context injection).
package reference

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/search"
)

// referenceService is the real ReferenceService implementation.
type referenceService struct {
	repo       ReferenceRepo
	searchRepo search.TopicSearchRepo
}

// NewReferenceService creates a ReferenceService backed by the given repos.
func NewReferenceService(repo ReferenceRepo, searchRepo search.TopicSearchRepo) ReferenceService {
	return &referenceService{repo: repo, searchRepo: searchRepo}
}

// Ensure referenceService satisfies the interface.
var _ ReferenceService = (*referenceService)(nil)

// ── ParseReferences (spec §2, §4.3) ───────────────────────────────────────

func (s *referenceService) ParseReferences(ctx context.Context, content string) ([]ParsedReference, error) {
	return ParseReferences(content), nil
}

// ── Autocomplete (spec §6.1) ──────────────────────────────────────────────

func (s *referenceService) Autocomplete(ctx context.Context, req ReferenceAutocompleteRequest) ([]ReferenceAutocompleteResult, error) {
	prefix := strings.TrimSpace(req.Prefix)
	if len(prefix) == 0 {
		return nil, ErrPrefixTooShort
	}
	if len(prefix) > 100 {
		return nil, ErrPrefixTooLong
	}

	include := req.Include
	if include == "" {
		include = "active"
	}
	if include != "active" && include != "archived" && include != "all" {
		return nil, ErrInvalidInclude
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 20 {
		limit = 20
	}

	return s.repo.AutocompleteTopics(ctx, req.TreeID, prefix, include, limit)
}

// ── ResolveReferences (spec §6.2, non-persisting) ─────────────────────────

func (s *referenceService) ResolveReferences(ctx context.Context, req ResolveReferencesRequest) (*ReferenceResolutionResult, error) {
	if len(req.Content) > 50000 {
		return nil, ErrContentTooLong
	}

	parsed := ParseReferences(req.Content)
	unique := DedupeBySlug(parsed)

	if len(unique) > HardCap {
		return nil, fmt.Errorf("%w: count=%d, hard_cap=%d", ErrReferencesTooMany, len(unique), HardCap)
	}

	result := &ReferenceResolutionResult{
		TreeID:     req.TreeID,
		References: []ResolvedReference{},
		NotFound:   []ParsedReference{},
	}

	totalNodesInScope := 0

	for _, ref := range unique {
		start := time.Now()
		topic, err := s.repo.GetTopicBySlug(ctx, req.TreeID, ref.Slug)
		durationMs := int(time.Since(start).Milliseconds())

		status := "resolved"
		var topicID *uuid.UUID
		var errCode *string

		if err != nil || topic == nil {
			// Lenient: not_found is not an error.
			result.NotFound = append(result.NotFound, ref)
			status = "not_found"
			if err != nil {
				errStr := err.Error()
				errCode = &errStr
			}
		} else if topic.Status == "deleted" {
			// Lenient default: deleted topics are treated as not_found.
			result.NotFound = append(result.NotFound, ref)
			status = "not_found"
		} else if topic.Status == "archived" {
			// Lenient: archived topics resolve but are flagged.
			result.References = append(result.References, ResolvedReference{
				Reference: ref,
				Topic: TopicSummary{
					ID:          topic.ID,
					TreeID:      topic.TreeID,
					Title:       topic.Title,
					Slug:        topic.Slug,
					Description: topic.Description,
					Status:      topic.Status,
					NodeCount:   topic.NodeCount,
					TopicTags:   topic.TopicTags,
					CreatedAt:   topic.CreatedAt,
					ArchivedAt:  topic.ArchivedAt,
				},
			})
			totalNodesInScope += int(topic.NodeCount)
			topicID = &topic.ID
		} else {
			result.References = append(result.References, ResolvedReference{
				Reference: ref,
				Topic: TopicSummary{
					ID:          topic.ID,
					TreeID:      topic.TreeID,
					Title:       topic.Title,
					Slug:        topic.Slug,
					Description: topic.Description,
					Status:      topic.Status,
					NodeCount:   topic.NodeCount,
					TopicTags:   topic.TopicTags,
					CreatedAt:   topic.CreatedAt,
				},
			})
			totalNodesInScope += int(topic.NodeCount)
			topicID = &topic.ID
		}

		// Log the resolution attempt (best-effort).
		_ = s.repo.InsertReferenceLog(ctx, ReferenceLogEntry{
			TreeID:     req.TreeID,
			RawRef:     ref.Raw,
			Slug:       ref.Slug,
			TopicID:    topicID,
			Status:     status,
			ErrorCode:  errCode,
			DurationMs: durationMs,
		})
	}

	result.TotalNodesInScope = totalNodesInScope

	// Soft cap warning (non-blocking).
	if len(unique) > SoftCap && len(unique) <= HardCap {
		result.TooMany = true
		result.Warning = fmt.Sprintf(
			"This message references %d topics. Including all of them may reduce focus. Consider narrowing to the most relevant 3-5 topics.",
			len(unique),
		)
	}

	return result, nil
}

// ── ResolveAtSend (spec §2, persistence) ──────────────────────────────────

func (s *referenceService) ResolveAtSend(ctx context.Context, treeID, nodeID uuid.UUID, content string, requesterID uuid.UUID) (*ReferenceResolutionResult, error) {
	if len(content) > 50000 {
		return nil, ErrContentTooLong
	}

	parsed := ParseReferences(content)
	unique := DedupeBySlug(parsed)

	// Hard cap: reject if more than 10 distinct references.
	if len(unique) > HardCap {
		return nil, fmt.Errorf("%w: count=%d, hard_cap=%d", ErrReferencesTooMany, len(unique), HardCap)
	}

	result := &ReferenceResolutionResult{
		NodeID:     nodeID,
		TreeID:     treeID,
		References: []ResolvedReference{},
		NotFound:   []ParsedReference{},
	}

	totalNodesInScope := 0
	var linksToPersist []ResolvedReferenceLink

	for _, ref := range unique {
		start := time.Now()
		topic, err := s.repo.GetTopicBySlug(ctx, treeID, ref.Slug)
		durationMs := int(time.Since(start).Milliseconds())

		status := "resolved"
		var topicID *uuid.UUID
		var errCode *string

		if err != nil || topic == nil || topic.Status == "deleted" {
			result.NotFound = append(result.NotFound, ref)
			status = "not_found"
			if err != nil {
				errStr := err.Error()
				errCode = &errStr
			}
		} else {
			summary := TopicSummary{
				ID:          topic.ID,
				TreeID:      topic.TreeID,
				Title:       topic.Title,
				Slug:        topic.Slug,
				Description: topic.Description,
				Status:      topic.Status,
				NodeCount:   topic.NodeCount,
				TopicTags:   topic.TopicTags,
				CreatedAt:   topic.CreatedAt,
				ArchivedAt:  topic.ArchivedAt,
			}
			result.References = append(result.References, ResolvedReference{
				Reference: ref,
				Topic:     summary,
			})
			totalNodesInScope += int(topic.NodeCount)
			topicID = &topic.ID

			// Build link for persistence.
			linksToPersist = append(linksToPersist, ResolvedReferenceLink{
				NodeID:     nodeID,
				TreeID:     treeID,
				TopicID:    topic.ID,
				RawRef:     ref.Raw,
				Slug:       ref.Slug,
				ResolvedBy: requesterID,
			})
		}

		// Log the resolution attempt (best-effort).
		_ = s.repo.InsertReferenceLog(ctx, ReferenceLogEntry{
			TreeID:     treeID,
			NodeID:     &nodeID,
			RawRef:     ref.Raw,
			Slug:       ref.Slug,
			TopicID:    topicID,
			Status:     status,
			ErrorCode:  errCode,
			DurationMs: durationMs,
		})
	}

	result.TotalNodesInScope = totalNodesInScope

	// Persist resolved reference links (spec §2: resolution at send time).
	if len(linksToPersist) > 0 {
		if err := s.repo.InsertResolvedRefs(ctx, linksToPersist); err != nil {
			log.Warn().Err(err).Msg("reference: failed to persist resolved refs")
			// Non-fatal: message is already persisted; refs just aren't queryable.
		}
	}

	// Soft cap warning.
	if len(unique) > SoftCap && len(unique) <= HardCap {
		result.TooMany = true
		result.Warning = fmt.Sprintf(
			"This message references %d topics. Including all of them may reduce focus. Consider narrowing to the most relevant 3-5 topics.",
			len(unique),
		)
	}

	return result, nil
}

// ── InjectWithReferences (spec §6.3) ─────────────────────────────────────

func (s *referenceService) InjectWithReferences(ctx context.Context, treeID uuid.UUID, req InjectWithReferencesRequest, requesterID uuid.UUID) (*search.MultiTopicContext, error) {
	if len(req.TopicIDs) == 0 && len(req.References) == 0 {
		return nil, ErrInvalidInput
	}

	// Resolve references to topic IDs.
	resolvedIDs := make(map[uuid.UUID]bool, len(req.TopicIDs)+len(req.References))
	for _, id := range req.TopicIDs {
		resolvedIDs[id] = true
	}

	var notFound []ParsedReference
	for _, rawRef := range req.References {
		parsed := ParseReferences(rawRef)
		for _, ref := range parsed {
			topic, err := s.repo.GetTopicBySlug(ctx, treeID, ref.Slug)
			if err != nil || topic == nil || topic.Status == "deleted" {
				notFound = append(notFound, ref)
				continue
			}
			resolvedIDs[topic.ID] = true
		}
	}

	// Combined topic limit.
	if len(resolvedIDs) > search.MaxTopics {
		return nil, ErrTooManyTopics
	}

	if len(resolvedIDs) == 0 {
		return nil, ErrInvalidInput
	}

	topicIDs := make([]uuid.UUID, 0, len(resolvedIDs))
	for id := range resolvedIDs {
		topicIDs = append(topicIDs, id)
	}

	maxNodes := req.MaxNodes
	if maxNodes <= 0 {
		maxNodes = search.DefaultMaxNodes
	}

	merged, _, err := search.CompileInjectContext(ctx, s.searchRepo, treeID, search.InjectContextRequest{
		TopicIDs: topicIDs,
		MaxNodes: maxNodes,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInjectionFailed, err)
	}

	_ = notFound // returned via the handler response, not here
	return merged, nil
}

// ── GetReferencedContext (spec §8.1-8.4) ─────────────────────────────────

// GetReferencedContext builds per-topic context for the context compiler's
// reference-handling path. For each topic: check the cache → if hit AND the
// cached context_hash matches the current topic's context hash, return the
// cached TopicContext (parsed via parseTopicContextPayload); otherwise build
// the TopicContext via the search inject machinery, cache it, and return.
func (s *referenceService) GetReferencedContext(ctx context.Context, treeID uuid.UUID, topicIDs []uuid.UUID, maxNodes int) ([]search.TopicContext, error) {
	if len(topicIDs) == 0 {
		return []search.TopicContext{}, nil
	}

	maxNodesPerTopic := maxNodes
	if maxNodesPerTopic <= 0 {
		maxNodesPerTopic = search.DefaultMaxNodes
	}

	// Build all topic contexts via the search inject machinery (which computes
	// the canonical context hash from node IDs — search.contextHash).
	// We use a single CompileInjectContext call for efficiency, then compare
	// each topic's computed hash against the cache.
	merged, results, err := search.CompileInjectContext(ctx, s.searchRepo, treeID, search.InjectContextRequest{
		TopicIDs: topicIDs,
		MaxNodes: maxNodesPerTopic,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInjectionFailed, err)
	}

	out := make([]search.TopicContext, 0, len(merged.Topics))

	for _, tc := range merged.Topics {
		// Check cache: is there a valid entry whose context_hash matches?
		cached, cacheErr := s.repo.GetReferenceCache(ctx, tc.TopicID)
		if cacheErr != nil {
			// Non-fatal: log and rebuild.
			cached = nil
		}

		if cached != nil && cached.ContextHash == tc.ContextHash {
			// Cache hit with matching hash — parse and return cached payload.
			parsed, parseErr := parseTopicContextPayload(cached.Payload)
			if parseErr == nil {
				out = append(out, parsed)
				continue
			}
			// Parse failed — fall through to rebuild + cache.
		}

		// Cache miss or hash mismatch — store the freshly computed context.
		payload, marshalErr := json.Marshal(tc)
		if marshalErr == nil {
			cacheErr := s.repo.UpsertReferenceCache(ctx, tc.TopicID, treeID, tc.ContextHash, tc.TotalNodes, payload)
			if cacheErr != nil {
				log.Warn().Err(cacheErr).Stringer("topic_id", tc.TopicID).
					Msg("reference: failed to cache topic context")
			}
		}

		out = append(out, tc)
	}

	_ = results // per-topic results not needed here
	return out, nil
}

// parseTopicContextPayload deserializes a cached TopicContext from JSON.
func parseTopicContextPayload(payload json.RawMessage) (search.TopicContext, error) {
	var tc search.TopicContext
	if err := json.Unmarshal(payload, &tc); err != nil {
		return tc, err
	}
	return tc, nil
}
