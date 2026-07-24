// Package service contains the business logic layer.
// TopicServiceImpl implements TopicService for topic CRUD, search,
// lifecycle, and context assembly. Spec: SPEC-TM-01 §4.4.
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
)

// TopicServiceImpl is the real implementation of TopicService.
type TopicServiceImpl struct {
	repo       db.TopicRepo
	memberRepo db.TopicMemberRepo
	treeRepo   db.TreeRepo
	nodeRepo   db.NodeRepo
}

// NewTopicServiceImpl creates a TopicServiceImpl with all required repos.
func NewTopicServiceImpl(
	repo db.TopicRepo,
	memberRepo db.TopicMemberRepo,
	treeRepo db.TreeRepo,
	nodeRepo db.NodeRepo,
) *TopicServiceImpl {
	return &TopicServiceImpl{
		repo:       repo,
		memberRepo: memberRepo,
		treeRepo:   treeRepo,
		nodeRepo:   nodeRepo,
	}
}

// CreateTopic validates inputs and creates a new topic.
func (s *TopicServiceImpl) CreateTopic(ctx context.Context, treeID, rootNodeID uuid.UUID, title, description string) (*TopicSummary, error) {
	if _, err := s.treeRepo.GetByID(ctx, treeID); err != nil {
		return nil, fmt.Errorf("service: tree not found: %w", err)
	}
	node, err := s.nodeRepo.GetByID(ctx, rootNodeID)
	if err != nil {
		return nil, fmt.Errorf("service: root node not found: %w", err)
	}
	if node.TreeID != treeID {
		return nil, errors.New("service: root node does not belong to the specified tree")
	}
	input := db.TopicCreateInput{TreeID: treeID, RootNodeID: rootNodeID, Title: title, Description: description}
	t, err := s.repo.Create(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("service: create topic: %w", err)
	}
	return topicToSummary(t), nil
}

// GetTopic retrieves a single topic by ID.
func (s *TopicServiceImpl) GetTopic(ctx context.Context, topicID uuid.UUID) (*TopicSummary, error) {
	t, err := s.repo.GetByID(ctx, topicID)
	if err != nil {
		return nil, fmt.Errorf("service: get topic: %w", err)
	}
	return topicToSummary(t), nil
}

// ListTopics lists topics for a tree with optional status filter.
func (s *TopicServiceImpl) ListTopics(ctx context.Context, treeID uuid.UUID, status string, limit, offset int) ([]TopicSummary, error) {
	topics, err := s.repo.GetByTree(ctx, treeID, status)
	if err != nil {
		return nil, fmt.Errorf("service: list topics: %w", err)
	}
	start := offset
	if start >= len(topics) {
		return []TopicSummary{}, nil
	}
	end := start + limit
	if limit <= 0 || end > len(topics) {
		end = len(topics)
	}
	summaries := make([]TopicSummary, 0, end-start)
	for _, t := range topics[start:end] {
		summaries = append(summaries, *topicToSummary(&t))
	}
	return summaries, nil
}

// UpdateTopic updates topic metadata (title, description, status).
func (s *TopicServiceImpl) UpdateTopic(ctx context.Context, topicID uuid.UUID, title, description, status *string) (*TopicSummary, error) {
	input := db.TopicUpdateInput{Title: title, Description: description}
	t, err := s.repo.Update(ctx, topicID, input)
	if err != nil {
		return nil, fmt.Errorf("service: update topic: %w", err)
	}
	if status != nil {
		switch *status {
		case "active":
			if err := s.repo.Restore(ctx, topicID); err != nil {
				return nil, fmt.Errorf("service: restore: %w", err)
			}
		case "archived":
			if err := s.repo.Archive(ctx, topicID); err != nil {
				return nil, fmt.Errorf("service: archive: %w", err)
			}
		default:
			return nil, fmt.Errorf("service: invalid status: %s", *status)
		}
		t, err = s.repo.GetByID(ctx, topicID)
		if err != nil {
			return nil, fmt.Errorf("service: re-read: %w", err)
		}
	}
	return topicToSummary(t), nil
}

// ArchiveTopic soft-deletes (archives) a topic.
func (s *TopicServiceImpl) ArchiveTopic(ctx context.Context, topicID uuid.UUID) error {
	return s.repo.Archive(ctx, topicID)
}

// topicToSummary converts a Topic to a TopicSummary.
func topicToSummary(t *db.Topic) *TopicSummary {
	return &TopicSummary{
		ID: t.ID, TreeID: t.TreeID, Title: t.Title, Slug: t.Slug,
		Description: t.Description, Status: t.Status,
		NodeCount: int(t.NodeCount), CreatedAt: t.CreatedAt,
	}
}
