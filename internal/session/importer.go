package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
)

// TreeCreator is the subset of service.TreeService the importer needs.
// *service.TreeServiceImpl satisfies it.
type TreeCreator interface {
	CreateTree(ctx context.Context, params service.CreateTreeParams) (*service.Tree, error)
}

// NodeCreator is the subset of service.NodeService the importer needs.
// *service.NodeServiceImpl satisfies it.
type NodeCreator interface {
	Create(ctx context.Context, treeID uuid.UUID, input service.CreateNodeInput) (*service.CreateNodeResult, error)
}

// WatermarkStore persists the incremental import watermark.
type WatermarkStore interface {
	Load() (Watermark, error)
	Save(Watermark) error
}

// Watermark records the last imported session. The (LastStartedAt,
// LastSessionID) pair forms the total-order boundary: a session is new iff
// its (started_at, id) pair sorts strictly after it. LastImportedAt is the
// wall-clock time of the run that advanced the watermark.
type Watermark struct {
	LastSessionID  string    `json:"last_session_id"`
	LastStartedAt  float64   `json:"last_started_at"`
	LastImportedAt time.Time `json:"last_imported_at"`
}

// FileWatermarkStore persists the watermark as JSON at Path. Writes are
// atomic (temp file + rename) and the parent directory is created on demand.
type FileWatermarkStore struct {
	Path string
}

// Load reads the watermark. A missing file yields a zero watermark (all
// sessions new) rather than an error.
func (s *FileWatermarkStore) Load() (Watermark, error) {
	var wm Watermark
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return wm, nil
		}
		return wm, fmt.Errorf("session: read watermark %s: %w", s.Path, err)
	}
	if err := json.Unmarshal(data, &wm); err != nil {
		return wm, fmt.Errorf("session: parse watermark %s: %w", s.Path, err)
	}
	return wm, nil
}

// Save atomically writes the watermark to Path.
func (s *FileWatermarkStore) Save(wm Watermark) error {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("session: watermark dir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(wm, "", "  ")
	if err != nil {
		return fmt.Errorf("session: encode watermark: %w", err)
	}
	data = append(data, '\n')
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("session: write watermark %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.Path); err != nil {
		return fmt.Errorf("session: rename watermark %s: %w", s.Path, err)
	}
	return nil
}

// ImportOptions controls a single import run.
type ImportOptions struct {
	Limit           int  // max new sessions to import; 0 = unlimited
	IncludeArchived bool // import archived sessions too
	DryRun          bool // report what would be imported without writing
}

// ImportSummary reports what a run did (or, in dry-run, would do).
type ImportSummary struct {
	DryRun           bool
	SessionsImported int
	TreesCreated     int
	NodesCreated     int
	SkippedArchived  int
	Titles           []string // imported/would-import titles, oldest first
}

// Importer orchestrates state.db → Canopy ingestion.
type Importer struct {
	reader *Reader
	trees  TreeCreator
	nodes  NodeCreator
	store  WatermarkStore
	owner  uuid.UUID
}

// NewImporter wires the reader, Canopy service dependencies, and watermark
// store into an Importer. owner is the tree owner / node author for every
// imported entity.
func NewImporter(reader *Reader, trees TreeCreator, nodes NodeCreator, store WatermarkStore, owner uuid.UUID) *Importer {
	return &Importer{
		reader: reader,
		trees:  trees,
		nodes:  nodes,
		store:  store,
		owner:  owner,
	}
}

// Run imports new sessions into Canopy. Sessions are walked oldest-first
// in (started_at, id) order; only sessions strictly after the watermark are
// candidates, and Limit truncates the candidate set. In non-dry-run mode
// the watermark is persisted after the run — but only when at least one
// session was imported (skipped-archived sessions do not advance it).
func (i *Importer) Run(ctx context.Context, opts ImportOptions) (*ImportSummary, error) {
	wm, err := i.store.Load()
	if err != nil {
		return nil, err
	}

	sessions, err := i.reader.ListSessions(ctx)
	if err != nil {
		return nil, err
	}

	sum := &ImportSummary{DryRun: opts.DryRun}

	var pending []Session
	for _, s := range sessions {
		if !sessionAfter(s, &wm) {
			continue
		}
		if s.Archived && !opts.IncludeArchived {
			sum.SkippedArchived++
			continue
		}
		pending = append(pending, s)
	}
	if opts.Limit > 0 && len(pending) > opts.Limit {
		pending = pending[:opts.Limit]
	}

	lastImported := wm
	for _, s := range pending {
		msgs, err := i.reader.ListMessages(ctx, s.ID)
		if err != nil {
			return nil, fmt.Errorf("session: messages for %s: %w", s.ID, err)
		}
		spec := MapSession(s, msgs)
		sum.Titles = append(sum.Titles, spec.Title)

		if opts.DryRun {
			sum.SessionsImported++
			sum.TreesCreated++
			sum.NodesCreated += len(spec.Messages)
			continue
		}

		if err := i.importSession(ctx, s, spec, sum); err != nil {
			return nil, err
		}
		lastImported.LastSessionID = s.ID
		lastImported.LastStartedAt = timeToUnix(s.StartedAt)
		lastImported.LastImportedAt = time.Now().UTC()
	}

	if !opts.DryRun && sum.SessionsImported > 0 {
		if err := i.store.Save(lastImported); err != nil {
			return nil, err
		}
	}
	return sum, nil
}

// importSession creates the tree (with root node) and chains every mapped
// message as a reply child. Counters on sum are updated on success.
func (i *Importer) importSession(ctx context.Context, s Session, spec TreeSpec, sum *ImportSummary) error {
	tree, err := i.trees.CreateTree(ctx, service.CreateTreeParams{
		OwnerID:       i.owner,
		Title:         spec.Title,
		Description:   spec.Description,
		RootContent:   spec.RootContent,
		ContentFormat: service.FormatMarkdown,
		NodeType:      service.NodeTypeMessage,
	})
	if err != nil {
		return fmt.Errorf("session: create tree for %s: %w", s.ID, err)
	}
	sum.TreesCreated++

	parent := tree.RootNodeID
	for _, ns := range spec.Messages {
		meta, err := json.Marshal(ns.Metadata)
		if err != nil {
			return fmt.Errorf("session: encode metadata for %s: %w", s.ID, err)
		}
		res, err := i.nodes.Create(ctx, tree.ID, service.CreateNodeInput{
			ParentID:      parent,
			Content:       ns.Content,
			ContentFormat: string(service.NodeFormatMarkdown),
			NodeType:      string(service.NodeKindMessage),
			EdgeType:      string(service.NodeEdgeReply),
			AuthorID:      i.owner,
			TreeID:        tree.ID,
			Metadata:      meta,
		})
		if err != nil {
			return fmt.Errorf("session: create node for %s: %w", s.ID, err)
		}
		sum.NodesCreated++
		if res != nil && res.Node != nil {
			parent = res.Node.ID
		}
	}
	sum.SessionsImported++
	return nil
}

// sessionAfter reports whether s is strictly after the watermark in the
// (started_at, id) ordering. A zero watermark (never imported) accepts
// every session. Both sides derive started_at through the same unixToTime
// conversion, so equality is exact for the same source row.
func sessionAfter(s Session, wm *Watermark) bool {
	if wm == nil || wm.LastSessionID == "" {
		return true
	}
	started := timeToUnix(s.StartedAt)
	if started > wm.LastStartedAt {
		return true
	}
	if started == wm.LastStartedAt {
		return s.ID > wm.LastSessionID
	}
	return false
}
