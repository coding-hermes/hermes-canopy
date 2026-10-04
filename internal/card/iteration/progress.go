package iteration

import (
	"sort"
	"time"

	"github.com/google/uuid"
)

const maxHeaderSegments = 3

// ProgressSegmentStatus is the header-facing status vocabulary. CardProgress
// intentionally keeps the normalized four-state status from §8.3; approval and
// waiting are presentation states derived from the active card state.
type ProgressSegmentStatus string

const (
	ProgressSegmentPendingApproval ProgressSegmentStatus = "pending_approval"
	ProgressSegmentWaitingForUser  ProgressSegmentStatus = "waiting_for_user"
	ProgressSegmentRunning         ProgressSegmentStatus = "running"
	ProgressSegmentFailed          ProgressSegmentStatus = "failed"
	ProgressSegmentCancelled       ProgressSegmentStatus = "cancelled"
	ProgressSegmentCompleted       ProgressSegmentStatus = "completed"
)

// ProgressSegment is one header status segment. AllSegments in ProgressHeader
// contains every segment; Segments is the three-item header projection.
type ProgressSegment struct {
	CardID        uuid.UUID             `json:"cardId"`
	ParentCardID  *uuid.UUID            `json:"parentCardId,omitempty"`
	Type          ProgressType          `json:"type"`
	Title         string                `json:"title"`
	Current       int                   `json:"current"`
	Total         int                   `json:"total"`
	Status        ProgressSegmentStatus `json:"status"`
	Phase         string                `json:"phase,omitempty"`
	UpdatedAt     time.Time             `json:"updatedAt"`
	Indeterminate bool                  `json:"indeterminate"`
}

// ProgressGroup is the aggregate for one declared phase, or Ungrouped when a
// record has no phase. Current and Total exclude records whose total is zero.
type ProgressGroup struct {
	Name          string         `json:"name"`
	Phase         string         `json:"phase,omitempty"`
	Current       int            `json:"current"`
	Total         int            `json:"total"`
	Indeterminate int            `json:"indeterminate"`
	Records       []CardProgress `json:"records"`
}

// ProgressHeader is the data projection consumed by the side-panel header.
// Segments is capped for concise rendering; AllSegments preserves overflow for
// the panel list and is deliberately never truncated.
type ProgressHeader struct {
	Groups      []ProgressGroup   `json:"groups"`
	Segments    []ProgressSegment `json:"segments"`
	AllSegments []ProgressSegment `json:"allSegments"`
}

type progressRecord struct {
	progress      CardProgress
	segmentStatus ProgressSegmentStatus
	phaseOrdinal  *int
}

// AggregateHeader applies the §8.3 aggregation algorithm to normalized records.
// Callers that need waiting/approval presentation states should use the service
// method, which derives those states from materialized card state.
func AggregateHeader(records []CardProgress) ProgressHeader {
	internal := make([]progressRecord, len(records))
	for index, record := range records {
		internal[index] = progressRecord{
			progress:      record,
			segmentStatus: segmentStatusFor(record.Status),
			phaseOrdinal:  record.PhaseOrdinal,
		}
	}
	return aggregateProgressHeader(internal)
}

func segmentStatusFor(status ProgressStatus) ProgressSegmentStatus {
	switch status {
	case ProgressStatusFailed:
		return ProgressSegmentFailed
	case ProgressStatusCancelled:
		return ProgressSegmentCancelled
	case ProgressStatusCompleted:
		return ProgressSegmentCompleted
	default:
		return ProgressSegmentRunning
	}
}

func aggregateProgressHeader(records []progressRecord) ProgressHeader {
	groupsByKey := make(map[string]*ProgressGroup)
	groupOrdinals := make(map[string]*int)
	groupFirstUpdated := make(map[string]time.Time)
	groupOrder := make([]string, 0)

	all := make([]ProgressSegment, 0, len(records))
	for _, record := range records {
		phase := record.progress.Phase
		key := phase
		name := phase
		if phase == "" {
			key = "Ungrouped"
			name = "Ungrouped"
		}
		group, ok := groupsByKey[key]
		if !ok {
			group = &ProgressGroup{Name: name, Phase: phase, Records: make([]CardProgress, 0, 1)}
			groupsByKey[key] = group
			groupOrder = append(groupOrder, key)
			groupFirstUpdated[key] = record.progress.UpdatedAt
		} else if record.progress.UpdatedAt.Before(groupFirstUpdated[key]) {
			groupFirstUpdated[key] = record.progress.UpdatedAt
		}
		if record.phaseOrdinal != nil {
			if current, exists := groupOrdinals[key]; !exists || current == nil || *record.phaseOrdinal < *current {
				ordinal := *record.phaseOrdinal
				groupOrdinals[key] = &ordinal
			}
		}
		group.Records = append(group.Records, record.progress)
		if record.progress.Total > 0 {
			group.Current += record.progress.Current
			group.Total += record.progress.Total
		} else {
			group.Indeterminate++
		}

		segment := ProgressSegment{
			CardID: record.progress.CardID, ParentCardID: record.progress.ParentCardID,
			Type: record.progress.Type, Title: record.progress.Title,
			Current: record.progress.Current, Total: record.progress.Total,
			Status: record.segmentStatus, Phase: record.progress.Phase,
			UpdatedAt: record.progress.UpdatedAt, Indeterminate: record.progress.Total <= 0,
		}
		all = append(all, segment)
	}

	hasDeclaredOrdinal := false
	for _, ordinal := range groupOrdinals {
		if ordinal != nil {
			hasDeclaredOrdinal = true
			break
		}
	}
	sort.SliceStable(groupOrder, func(i, j int) bool {
		left, right := groupOrder[i], groupOrder[j]
		if hasDeclaredOrdinal {
			leftOrdinal, leftOK := groupOrdinals[left]
			rightOrdinal, rightOK := groupOrdinals[right]
			switch {
			case leftOK && rightOK && *leftOrdinal != *rightOrdinal:
				return *leftOrdinal < *rightOrdinal
			case leftOK != rightOK:
				return leftOK
			}
		}
		return groupFirstUpdated[left].Before(groupFirstUpdated[right])
	})

	groups := make([]ProgressGroup, 0, len(groupOrder))
	for _, key := range groupOrder {
		groups = append(groups, *groupsByKey[key])
	}

	// The priority order is intentionally independent from phase order: the
	// header must surface approval/waiting work before ordinary running work.
	sort.SliceStable(all, func(i, j int) bool {
		left, right := segmentPriority(all[i].Status), segmentPriority(all[j].Status)
		if left != right {
			return left < right
		}
		return false
	})
	visible := all
	if len(visible) > maxHeaderSegments {
		visible = visible[:maxHeaderSegments]
	}
	return ProgressHeader{Groups: groups, Segments: visible, AllSegments: all}
}

func segmentPriority(status ProgressSegmentStatus) int {
	switch status {
	case ProgressSegmentPendingApproval, ProgressSegmentWaitingForUser:
		return 0
	case ProgressSegmentRunning:
		return 1
	case ProgressSegmentFailed, ProgressSegmentCancelled:
		return 2
	default:
		return 3
	}
}
