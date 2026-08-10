// Package service contains the business logic layer.
// TopicServiceImpl implements TopicService for topic CRUD, search,
// lifecycle, context assembly, and auto-detection (SPEC-TM-02).
// Spec: SPEC-TM-01 §4.4, SPEC-TM-02 §4.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sse"
)

// Proposal expiry constants (SPEC-TM-02 §4, §10).
const (
	// proposalExpiryMessages is how many new messages after which a pending
	// proposal expires (default 5 per spec).
	proposalExpiryMessages = 5
	// proposalExpiryDuration is the wall-clock TTL for pending proposals.
	proposalExpiryDuration = 24 * time.Hour
	// subjectCooldownDuration is how long a dismissed subject stays cooled down.
	subjectCooldownDuration = 1 * time.Hour
)

// Detection errors — mapped to HTTP error codes in the handler layer.
var (
	ErrProposalNotFound      = errors.New("topic proposal not found")
	ErrProposalExpired       = errors.New("topic proposal has expired")
	ErrProposalAlreadyResolved = errors.New("topic proposal is already resolved")
	ErrDetectionDisabled     = errors.New("topic detection is disabled for this tree")
	ErrProposalRateLimited   = errors.New("topic proposal rate limit reached")
	ErrProposalDuplicate     = errors.New("an existing topic already covers this node")
	ErrSubjectCooldown       = errors.New("topic detection is cooling down for this subject")
	ErrProposalRootInvalid   = errors.New("topic proposal root node is invalid")
	ErrProposalTitleRequired = errors.New("topic proposal title is required")
	ErrProposalTitleTooLong  = errors.New("topic proposal title must be 1-200 characters")
	ErrInvalidDetectionLevel = errors.New("detection level must be off, explicit_only, or full")
	ErrInvalidDetectionConfig = errors.New("invalid topic detection configuration")
)

// TopicServiceImpl is the real implementation of TopicService.
type TopicServiceImpl struct {
	repo         db.TopicRepo
	memberRepo   db.TopicMemberRepo
	treeRepo     db.TreeRepo
	nodeRepo     db.NodeRepo
	edgeRepo     db.EdgeRepo
	sseHub       sse.SSEHub
	proposalRepo db.TopicProposalRepo
	configRepo   db.DetectionConfigRepo
	cooldownRepo db.SubjectCooldownRepo
	analyzer     Analyzer
	now          func() time.Time

	// proposalMu serializes concurrent ConfirmProposal calls for the same
	// proposal ID to ensure idempotency (one topic created, both return it).
	proposalMu sync.Mutex
}

// NewTopicServiceImpl creates a TopicServiceImpl with all required repos.
// edgeRepo, sseHub, proposalRepo, configRepo, and cooldownRepo may be nil
// for callers that only need CRUD (legacy call sites). Detection methods
// will return ErrDetectionDisabled-equivalent no-ops when deps are absent.
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
		now:        func() time.Time { return time.Now().UTC() },
	}
}

// WithDetection wires the detection dependencies and returns the same
// instance for fluent construction. Used by main.go to add detection
// support after the base constructor.
func (s *TopicServiceImpl) WithDetection(
	edgeRepo db.EdgeRepo,
	sseHub sse.SSEHub,
	proposalRepo db.TopicProposalRepo,
	configRepo db.DetectionConfigRepo,
	cooldownRepo db.SubjectCooldownRepo,
) *TopicServiceImpl {
	s.edgeRepo = edgeRepo
	s.sseHub = sseHub
	s.proposalRepo = proposalRepo
	s.configRepo = configRepo
	s.cooldownRepo = cooldownRepo
	s.analyzer = NewKeywordAnalyzer()
	return s
}

// detectionEnabled returns true if all detection dependencies are wired.
func (s *TopicServiceImpl) detectionEnabled() bool {
	return s.proposalRepo != nil && s.configRepo != nil && s.cooldownRepo != nil
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

// ── Auto-detection (SPEC-TM-02 §4) ──────────────────────────────────────

// AutoDetect implements the detection algorithm from SPEC-TM-02 §4.
// It is called after a node is persisted (NodeService.Create hook).
func (s *TopicServiceImpl) AutoDetect(ctx context.Context, node db.Node, contextNodes []db.Node) (*TopicProposal, error) {
	if !s.detectionEnabled() {
		return nil, nil
	}

	// Load config.
	cfgRecord, err := s.configRepo.Get(ctx, node.TreeID)
	if err != nil {
		return nil, fmt.Errorf("detection: get config: %w", err)
	}
	cfg := configRecordToConfig(cfgRecord)

	// Off → no-op.
	if cfg.DetectionLevel == DetectionLevelOff {
		return nil, nil
	}

	// Frequency limits (§4 algorithm).
	minSpacing := max(3, cfg.MinMessagesPerTopic)
	if cfgRecord.MessagesSinceProposal < minSpacing && cfgRecord.MessagesSinceProposal > 0 {
		return nil, nil
	}
	// Global cooldown is enforced later for non-explicit signals.

	// Evaluate signals.
	var signals []*Signal

	// Explicit signal — always evaluated (even in explicit_only mode).
	explicit := DetectExplicit(node)
	if explicit != nil {
		signals = append(signals, explicit)
	}

	if cfg.DetectionLevel == DetectionLevelFull {
		// Implicit signal.
		implicit := s.detectImplicitWithWindow(ctx, node, contextNodes, cfg)
		if implicit != nil {
			signals = append(signals, implicit)
		}

		// Structural signal.
		structural := DetectStructural(ctx, node, s.edgeRepo)
		if structural != nil {
			signals = append(signals, structural)
		}
	}

	if len(signals) == 0 {
		return nil, nil
	}

	// Highest confidence wins.
	candidate := HighestConfidenceSignal(signals)
	if candidate == nil {
		return nil, nil
	}

	// Explicit signals bypass frequency limits only if there hasn't been
	// a proposal in the last `minSpacing` messages. The global cooldown
	// still applies to prevent spam.
	globalCooldown := max(10, cfg.ProposalCooldown)
	if candidate.Type != DetectionExplicit {
		if cfgRecord.MessagesSinceProposal > 0 && cfgRecord.MessagesSinceProposal < globalCooldown {
			return nil, nil
		}
	}

	// Duplicate/covered check: existing active topic covering the root.
	if _, err := s.repo.GetByRootNode(ctx, candidate.RootNodeID); err == nil {
		return nil, nil // already covered
	}

	// Subject cooldown check.
	active, err := s.cooldownRepo.IsActive(ctx, node.TreeID, candidate.SubjectKey)
	if err != nil {
		log.Ctx(ctx).Warn().Err(err).Msg("detection: cooldown check failed")
	} else if active {
		return nil, nil
	}

	// Generate proposal.
	proposal, err := s.generateProposal(ctx, node, contextNodes, candidate)
	if err != nil {
		return nil, err
	}

	// Auto-create path.
	if cfg.AutoCreate && !cfg.AlwaysAsk {
		// Confirm immediately — creates the topic.
		created, err := s.ConfirmProposal(ctx, proposal.ID, proposal.Title)
		if err != nil {
			return proposal, fmt.Errorf("detection: auto-confirm: %w", err)
		}
		// Emit topic_created SSE event.
		s.broadcastTopicCreated(ctx, node.TreeID, proposal.ID, created)
		return proposal, nil
	}

	// Emit topic_proposed SSE event.
	s.broadcastTopicProposed(ctx, node.TreeID, proposal)

	// Update proposal tracking.
	msgsSince := 0
	if cfgRecord.LastProposalSeq > 0 {
		msgsSince = int(node.SequenceNum - cfgRecord.LastProposalSeq)
		if msgsSince < 0 {
			msgsSince = 0
		}
	}
	_ = s.configRepo.UpdateProposalTracking(ctx, node.TreeID, node.SequenceNum, msgsSince)

	return proposal, nil
}

// detectImplicitWithWindow splits context nodes into current (newest 3-5)
// and previous (preceding 10) windows, then runs implicit detection.
func (s *TopicServiceImpl) detectImplicitWithWindow(ctx context.Context, node db.Node, contextNodes []db.Node, cfg DetectionConfig) *Signal {
	// contextNodes is ordered newest-first (per NodeService hook).
	// Build current window: the node + up to 4 preceding = 5 max.
	current := []db.Node{node}
	for i := 0; i < len(contextNodes) && len(current) < 5; i++ {
		current = append(current, contextNodes[i])
	}

	// Previous window: up to 10 nodes before the current window.
	prevStart := len(current) - 1 // skip the node itself (already in current)
	var previous []db.Node
	for i := prevStart; i < len(contextNodes) && len(previous) < 10; i++ {
		previous = append(previous, contextNodes[i])
	}

	return DetectImplicit(current, previous, cfg, s.analyzer)
}

// generateProposal creates and persists a TopicProposal from a signal.
func (s *TopicServiceImpl) generateProposal(ctx context.Context, node db.Node, contextNodes []db.Node, sig *Signal) (*TopicProposal, error) {
	// Load the root node for title generation.
	rootNode := node
	if sig.RootNodeID != node.ID {
		rn, err := s.nodeRepo.GetByID(ctx, sig.RootNodeID)
		if err == nil {
			rootNode = *rn
		}
	}

	title := GenerateTitle(rootNode, contextNodes, 3)
	if title == "" {
		// Fall back to subject key.
		title = sig.SubjectKey
	}
	if title == "" {
		title = "New Topic"
	}

	expiresAt := s.now().Add(proposalExpiryDuration)
	proposal := &db.TopicProposal{
		TreeID:        node.TreeID,
		RootNodeID:    sig.RootNodeID,
		Title:         title,
		Description:   "",
		DetectionType: string(sig.Type),
		Confidence:    sig.Confidence,
		SubjectKey:    sig.SubjectKey,
		Status:        "pending",
		ExpiresAt:     expiresAt,
		Evidence:      MarshalEvidence(sig.Evidence),
	}

	saved, err := s.proposalRepo.Create(ctx, proposal)
	if err != nil {
		return nil, fmt.Errorf("detection: create proposal: %w", err)
	}
	return dbProposalToService(saved), nil
}

// ── Proposal lifecycle (SPEC-TM-02 §5) ──────────────────────────────────

// ConfirmProposal accepts a pending proposal and creates a topic.
func (s *TopicServiceImpl) ConfirmProposal(ctx context.Context, proposalID uuid.UUID, titleOverride string) (*db.Topic, error) {
	s.proposalMu.Lock()
	defer s.proposalMu.Unlock()

	proposal, err := s.proposalRepo.GetByID(ctx, proposalID)
	if err != nil {
		return nil, ErrProposalNotFound
	}

	// Idempotent: if already confirmed, return the existing topic.
	if proposal.Status == "confirmed" {
		// Try to find the topic by root node.
		if t, err := s.repo.GetByRootNode(ctx, proposal.RootNodeID); err == nil {
			return t, nil
		}
		return nil, ErrProposalAlreadyResolved
	}
	if proposal.Status != "pending" {
		return nil, ErrProposalAlreadyResolved
	}

	// Check expiry.
	if s.now().After(proposal.ExpiresAt) {
		_ = s.proposalRepo.UpdateStatus(ctx, proposalID, "expired", s.now())
		return nil, ErrProposalExpired
	}

	// Determine title.
	title := proposal.Title
	if titleOverride != "" {
		title = titleOverride
	}
	if title == "" {
		return nil, ErrProposalTitleRequired
	}
	if len([]rune(title)) > 200 {
		return nil, ErrProposalTitleTooLong
	}

	// Validate root node still exists and belongs to the tree.
	rootNode, err := s.nodeRepo.GetByID(ctx, proposal.RootNodeID)
	if err != nil || rootNode.TreeID != proposal.TreeID {
		return nil, ErrProposalRootInvalid
	}

	// Title uniqueness check (GetBySlug uses slug).
	slug := generateTopicSlug(title)
	if existing, err := s.repo.GetBySlug(ctx, proposal.TreeID, slug); err == nil && existing.ID != uuid.Nil {
		// Title conflict — keep proposal pending for correction.
		_ = existing
		return nil, fmt.Errorf("%w: title %q already exists", ErrProposalDuplicate, title)
	}

	// Create the topic.
	input := db.TopicCreateInput{
		TreeID:     proposal.TreeID,
		RootNodeID: proposal.RootNodeID,
		Title:      title,
	}
	topic, err := s.repo.Create(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProposalDuplicate, err)
	}

	// Mark proposal confirmed.
	_ = s.proposalRepo.UpdateStatus(ctx, proposalID, "confirmed", s.now())

	return topic, nil
}

// DismissProposal rejects a pending proposal and records a subject cooldown.
func (s *TopicServiceImpl) DismissProposal(ctx context.Context, proposalID uuid.UUID) error {
	proposal, err := s.proposalRepo.GetByID(ctx, proposalID)
	if err != nil {
		return ErrProposalNotFound
	}
	if proposal.Status != "pending" {
		return ErrProposalAlreadyResolved
	}

	// Record subject cooldown.
	if s.cooldownRepo != nil {
		_ = s.cooldownRepo.Add(ctx, db.SubjectCooldown{
			TreeID:        proposal.TreeID,
			SubjectKey:    proposal.SubjectKey,
			CooldownUntil: s.now().Add(subjectCooldownDuration),
		})
	}

	return s.proposalRepo.UpdateStatus(ctx, proposalID, "dismissed", s.now())
}

// ListPendingProposals returns all pending proposals for a tree, expiring
// stale ones first.
func (s *TopicServiceImpl) ListPendingProposals(ctx context.Context, treeID uuid.UUID) ([]db.TopicProposal, error) {
	if !s.detectionEnabled() {
		return []db.TopicProposal{}, nil
	}
	// Sweep expired proposals.
	now := s.now()
	_ = s.sweepExpired(ctx, treeID, now)
	_ = now
	return s.proposalRepo.ListPending(ctx, treeID)
}

// sweepExpired marks proposals as expired if their expires_at has passed
// or if proposalExpiryMessages new messages have arrived since.
func (s *TopicServiceImpl) sweepExpired(ctx context.Context, treeID uuid.UUID, now time.Time) error {
	// Expire by time.
	pending, err := s.proposalRepo.ListPending(ctx, treeID)
	if err != nil {
		return err
	}
	for _, p := range pending {
		if now.After(p.ExpiresAt) {
			_ = s.proposalRepo.UpdateStatus(ctx, p.ID, "expired", now)
		}
	}
	// Expire by message count: proposals whose root is more than
	// proposalExpiryMessages sequence numbers behind the latest node.
	nodes, err := s.nodeRepo.GetByTree(ctx, treeID)
	if err != nil {
		return nil
	}
	if len(nodes) == 0 {
		return nil
	}
	latestSeq := nodes[len(nodes)-1].SequenceNum
	threshold := latestSeq - int64(proposalExpiryMessages)
	if threshold > 0 {
		_, _ = s.proposalRepo.ExpirePending(ctx, treeID, threshold)
	}
	return nil
}

// ── Detection config (SPEC-TM-02 §7) ────────────────────────────────────

// GetDetectionConfig returns the per-tree detection configuration.
func (s *TopicServiceImpl) GetDetectionConfig(ctx context.Context, treeID uuid.UUID) (DetectionConfig, error) {
	if !s.detectionEnabled() {
		return DefaultDetectionConfig(), nil
	}
	rec, err := s.configRepo.Get(ctx, treeID)
	if err != nil {
		return DefaultDetectionConfig(), fmt.Errorf("detection: get config: %w", err)
	}
	return configRecordToConfig(rec), nil
}

// UpdateDetectionConfig updates the per-tree detection configuration.
func (s *TopicServiceImpl) UpdateDetectionConfig(ctx context.Context, treeID uuid.UUID, cfg DetectionConfig) (DetectionConfig, error) {
	if !s.detectionEnabled() {
		return DefaultDetectionConfig(), ErrDetectionDisabled
	}
	// Validate.
	switch cfg.DetectionLevel {
	case DetectionLevelOff, DetectionLevelExplicitOnly, DetectionLevelFull:
	default:
		return DefaultDetectionConfig(), ErrInvalidDetectionLevel
	}
	if cfg.MinMessagesPerTopic < 1 {
		return DefaultDetectionConfig(), ErrInvalidDetectionConfig
	}
	if cfg.ProposalCooldown < 0 {
		return DefaultDetectionConfig(), ErrInvalidDetectionConfig
	}

	rec := db.DetectionConfigRecord{
		TreeID:              treeID,
		AutoCreate:          cfg.AutoCreate,
		AlwaysAsk:           cfg.AlwaysAsk,
		DetectionLevel:      cfg.DetectionLevel,
		MinMessagesPerTopic: cfg.MinMessagesPerTopic,
		ProposalCooldown:    cfg.ProposalCooldown,
	}
	saved, err := s.configRepo.Upsert(ctx, rec)
	if err != nil {
		return DefaultDetectionConfig(), fmt.Errorf("detection: update config: %w", err)
	}
	return configRecordToConfig(saved), nil
}

// PreviewProposal runs detection for a node without persisting a proposal.
func (s *TopicServiceImpl) PreviewProposal(ctx context.Context, treeID, nodeID uuid.UUID) (*TopicProposal, error) {
	if !s.detectionEnabled() {
		return nil, ErrDetectionDisabled
	}
	node, err := s.nodeRepo.GetByID(ctx, nodeID)
	if err != nil {
		return nil, ErrProposalRootInvalid
	}
	if node.TreeID != treeID {
		return nil, ErrProposalRootInvalid
	}

	// Load context nodes.
	allNodes, err := s.nodeRepo.GetByTree(ctx, treeID)
	if err != nil {
		return nil, fmt.Errorf("detection: load context: %w", err)
	}
	var contextNodes []db.Node
	for i := len(allNodes) - 1; i >= 0; i-- {
		if allNodes[i].ID == nodeID {
			contextNodes = append(contextNodes, allNodes[i-min(i, 10):i]...)
			break
		}
	}

	cfg, err := s.GetDetectionConfig(ctx, treeID)
	if err != nil {
		return nil, err
	}
	if cfg.DetectionLevel == DetectionLevelOff {
		return nil, nil
	}

	// Evaluate signals (same as AutoDetect but no persistence).
	var signals []*Signal
	if explicit := DetectExplicit(*node); explicit != nil {
		signals = append(signals, explicit)
	}
	if cfg.DetectionLevel == DetectionLevelFull {
		if implicit := s.detectImplicitWithWindow(ctx, *node, contextNodes, cfg); implicit != nil {
			signals = append(signals, implicit)
		}
		if structural := DetectStructural(ctx, *node, s.edgeRepo); structural != nil {
			signals = append(signals, structural)
		}
	}

	candidate := HighestConfidenceSignal(signals)
	if candidate == nil {
		return nil, nil
	}

	title := GenerateTitle(*node, contextNodes, 3)
	if title == "" {
		title = candidate.SubjectKey
	}
	if title == "" {
		title = "New Topic"
	}

	return &TopicProposal{
		TreeID:        treeID,
		RootNodeID:    candidate.RootNodeID,
		Title:         title,
		DetectionType: candidate.Type,
		Confidence:    candidate.Confidence,
		SubjectKey:    candidate.SubjectKey,
		Status:        "pending",
		ExpiresAt:     s.now().Add(proposalExpiryDuration),
	}, nil
}

// ── SSE helpers ──────────────────────────────────────────────────────────

func (s *TopicServiceImpl) broadcastTopicProposed(ctx context.Context, treeID uuid.UUID, proposal *TopicProposal) {
	if s.sseHub == nil {
		return
	}
	data, _ := jsonMarshal(map[string]any{
		"proposalId":    proposal.ID,
		"treeId":        treeID,
		"rootNodeId":    proposal.RootNodeID,
		"title":         proposal.Title,
		"detectionType": proposal.DetectionType,
		"confidence":    proposal.Confidence,
	})
	s.sseHub.Broadcast(treeID, sse.SSEEvent{
		TreeID:    treeID,
		Type:      "topic_proposed",
		Data:      data,
		Timestamp: s.now(),
	})
}

func (s *TopicServiceImpl) broadcastTopicCreated(ctx context.Context, treeID uuid.UUID, proposalID uuid.UUID, topic *db.Topic) {
	if s.sseHub == nil {
		return
	}
	data, _ := jsonMarshal(map[string]any{
		"proposalId": proposalID,
		"topic": map[string]any{
			"id":    topic.ID,
			"title": topic.Title,
			"slug":  topic.Slug,
		},
	})
	s.sseHub.Broadcast(treeID, sse.SSEEvent{
		TreeID:    treeID,
		Type:      "topic_created",
		Data:      data,
		Timestamp: s.now(),
	})
}

// ── Helpers ──────────────────────────────────────────────────────────────

// configRecordToConfig converts a DB record to the service-level DetectionConfig.
func configRecordToConfig(rec *db.DetectionConfigRecord) DetectionConfig {
	return DetectionConfig{
		AutoCreate:          rec.AutoCreate,
		AlwaysAsk:           rec.AlwaysAsk,
		DetectionLevel:      rec.DetectionLevel,
		MinMessagesPerTopic: rec.MinMessagesPerTopic,
		ProposalCooldown:    rec.ProposalCooldown,
	}
}

// generateTopicSlug creates a URL-safe slug from a title (matches DB generateSlug).
func generateTopicSlug(title string) string {
	lower := strings.ToLower(strings.TrimSpace(title))
	return strings.ReplaceAll(lower, " ", "-")
}

// jsonMarshal is a safe wrapper that never fails for simple map payloads.
func jsonMarshal(v any) (json.RawMessage, error) {
	return json.Marshal(v)
}

// dbProposalToService converts a persisted db.TopicProposal to the service-
// level TopicProposal type returned by AutoDetect/PreviewProposal.
func dbProposalToService(p *db.TopicProposal) *TopicProposal {
	if p == nil {
		return nil
	}
	return &TopicProposal{
		ID:            p.ID,
		TreeID:        p.TreeID,
		RootNodeID:    p.RootNodeID,
		Title:         p.Title,
		Description:   p.Description,
		DetectionType: DetectionType(p.DetectionType),
		Confidence:    p.Confidence,
		SubjectKey:    p.SubjectKey,
		Status:        p.Status,
		ExpiresAt:     p.ExpiresAt,
	}
}

// topicToSummary converts a Topic to a TopicSummary.
func topicToSummary(t *db.Topic) *TopicSummary {
	return &TopicSummary{
		ID: t.ID, TreeID: t.TreeID, Title: t.Title, Slug: t.Slug,
		Description: t.Description, Status: t.Status,
		NodeCount: int(t.NodeCount), CreatedAt: t.CreatedAt,
	}
}
