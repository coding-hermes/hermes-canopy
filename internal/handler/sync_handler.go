// Package handler provides HTTP handlers for Canopy REST endpoints.
// SyncHandler implements the sync endpoints defined in SPEC-DM-02 §7.
package handler

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/sync"
)

// SyncHandler wires the sync HTTP routes to the SyncEngine interface.
type SyncHandler struct {
	engine sync.SyncEngine
}

// NewSyncHandler returns a handler wired to the given SyncEngine.
func NewSyncHandler(engine sync.SyncEngine) *SyncHandler {
	return &SyncHandler{engine: engine}
}

// Routes mounts the sync endpoints under /sync.
func (h *SyncHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.HandleSync)
	r.Post("/snapshot", h.HandleTriggerSnapshot)
	return r
}

// HandleSync serves GET /trees/{tree_id}/sync?sinceHash=<sha256>.
// Returns delta or full snapshot per SPEC-DM-02 §7.
func (h *SyncHandler) HandleSync(w http.ResponseWriter, r *http.Request) {
	treeID, ok := parseTreeID(w, r)
	if !ok {
		return
	}
	sinceHash := r.URL.Query().Get("sinceHash")

	delta, err := h.engine.ComputeDeltaForClient(r.Context(), treeID, sinceHash)
	if err != nil {
		h.writeSyncError(w, r, err)
		return
	}

	if delta.FromHash == delta.ToHash {
		// No changes since lastKnownHash
		w.WriteHeader(http.StatusNoContent)
		return
	}

	writeJSON(w, http.StatusOK, delta)
}

// HandleTriggerSnapshot serves POST /trees/{tree_id}/sync/snapshot.
// Manually triggers a snapshot computation.
func (h *SyncHandler) HandleTriggerSnapshot(w http.ResponseWriter, r *http.Request) {
	treeID, ok := parseTreeID(w, r)
	if !ok {
		return
	}

	snap, err := h.engine.CreateSnapshot(r.Context(), treeID)
	if err != nil {
		h.writeSyncError(w, r, err)
		return
	}

	writeJSON(w, http.StatusCreated, snap)
}

// writeSyncError translates sync errors to HTTP status codes.
func (h *SyncHandler) writeSyncError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errors.New("sync: no snapshots exist for tree")):
		writeError(w, 404, "NO_SNAPSHOTS", "no snapshots exist for tree")
	default:
		log.Ctx(r.Context()).Error().Err(err).Msg("sync request failed")
		writeError(w, 500, "INTERNAL_ERROR", "internal server error")
	}
}
